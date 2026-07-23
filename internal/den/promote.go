package den

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/nurozen/context-marmot/internal/classifier"
	"github.com/nurozen/context-marmot/internal/config"
	"github.com/nurozen/context-marmot/internal/embedding"
	"github.com/nurozen/context-marmot/internal/node"
	"github.com/nurozen/context-marmot/internal/warren"
)

// Promote folds a dying den vault's ACTIVE nodes into a LIVE local target den
// vault's identity store. It is the exec-free engine behind
// `marmot den destroy --promote <target>`: the CLI owns lifecycle (locating
// vaults, tearing the source down); Promote only writes.
//
// It shares the classify-and-apply core with Contribute (deterministic id
// pass, then a classifier second pass with the same NOOP/UPDATE/SUPERSEDE/ADD
// vocabulary and supersede ordering), but the write backend differs on two
// axes:
//
//   - Writes go through the target's node.Store directly (SaveNode), not
//     warren.WriteEditableNodeFile — the target is a live local vault the
//     operator owns, not a staged warren checkout.
//   - The target's embeddings.db IS maintained. Every written node is embedded
//     with the TARGET vault's embedder and upserted; a supersede flips the
//     retired node's embedding row to superseded (mirroring the live MCP
//     supersede path, which keeps the row and calls UpdateStatus). Contribute
//     deliberately never touches embeddings; Promote must, because there is no
//     later consume/reindex step — the source vault is about to be destroyed.
//
// Degradation ladder mirrors Contribute's, so knowledge is never lost:
//   - source vault unreadable -> hard error (nothing to fold).
//   - target _config.md unreadable or embedder unavailable -> warn,
//     classification AND embedding disabled, unmatched nodes become plain ADDs
//     (node files still written).
//   - embeddings.db absent -> fresh target: classifier nil (plain ADDs, no
//     warning) but the store is still CREATED so new vectors are upserted.
//   - any per-node embedding failure -> warn (the node file is already
//     durably written), never a hard failure.
//
// Namespace mapping is v1-identity (nodes keep their namespace/id). With
// dryRun both passes run read-only and nothing is written — not even the
// embeddings.db is created. On error the returned result is non-nil and
// partial (CreatedFiles/ModifiedFiles cover every file written before the
// failure).
func Promote(ctx context.Context, sourceVaultDir, targetVaultDir string, dryRun bool) (*ContributeResult, error) {
	res := &ContributeResult{Warnings: []string{}}

	srcStore := node.NewStore(sourceVaultDir)
	metas, err := srcStore.ListActiveNodes()
	if err != nil {
		return nil, fmt.Errorf("list source vault nodes: %w", err)
	}
	sort.Slice(metas, func(i, j int) bool { return metas[i].ID < metas[j].ID })

	tgtStore := node.NewStore(targetVaultDir)
	graph := targetGraph{store: tgtStore}

	// Classification + embedding environment over the TARGET vault. Every
	// setup failure only disables classification/embedding (unmatched ids
	// become plain ADDs) — it never fails the promote.
	var embedder embedding.Embedder
	var targetCfg *config.VaultConfig
	if cfg, cfgErr := config.Load(targetVaultDir); cfgErr != nil {
		res.Warnings = append(res.Warnings, fmt.Sprintf("target vault config unreadable (%v); classification disabled, unmatched nodes added directly", cfgErr))
	} else if emb, embErr := config.NewEmbedderFromVault(cfg); embErr != nil {
		res.Warnings = append(res.Warnings, fmt.Sprintf("target embedder unavailable (%v); classification disabled, unmatched nodes added directly", embErr))
	} else {
		embedder = emb
		targetCfg = cfg
	}

	ap := promoteApplier{store: tgtStore, embedder: embedder}
	var cls *classifier.Classifier
	if embedder != nil {
		dbPath := filepath.Join(targetVaultDir, ".marmot-data", "embeddings.db")
		_, statErr := os.Stat(dbPath)
		dbExists := statErr == nil
		if dryRun {
			// Read-only: never create the store (dry-run writes nothing, not
			// even the embeddings.db). Classify against the existing db only.
			if dbExists {
				if embStore, dbErr := embedding.NewStoreReadOnly(dbPath); dbErr != nil {
					res.Warnings = append(res.Warnings, fmt.Sprintf("target embeddings unreadable (%v); unmatched nodes added directly", dbErr))
				} else {
					defer func() { _ = embStore.Close() }()
					clsLLM, _ := newClassifierLLM(targetCfg, targetVaultDir)
					cls = &classifier.Classifier{Store: embStore, Embedder: embedder, LLM: clsLLM}
				}
			}
		} else {
			// Open (creating if absent) the target's embeddings.db read-write:
			// one handle, opened once, serves both the classifier reads and the
			// applier's upserts (single process — no lock wedges).
			if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
				return res, fmt.Errorf("create target data dir: %w", err)
			}
			embStore, dbErr := embedding.NewStore(dbPath)
			if dbErr != nil {
				res.Warnings = append(res.Warnings, fmt.Sprintf("target embeddings unwritable (%v); unmatched nodes added directly, embeddings not updated", dbErr))
			} else {
				defer func() { _ = embStore.Close() }()
				ap.embStore = embStore
				// Only classify when the target already had embeddings: a fresh
				// target has nothing to match against, so unmatched ids are
				// plain ADDs (no wasted classify calls, mirroring contribute's
				// fresh-target path) while their vectors are still upserted.
				if dbExists {
					clsLLM, _ := newClassifierLLM(targetCfg, targetVaultDir)
					cls = &classifier.Classifier{Store: embStore, Embedder: embedder, LLM: clsLLM}
				}
			}
		}
	}

	for i := range metas {
		meta := metas[i]
		n, loadErr := srcStore.LoadNode(meta.FilePath)
		if loadErr != nil {
			res.Warnings = append(res.Warnings, fmt.Sprintf("skipped unreadable source node %s: %v", meta.ID, loadErr))
			continue
		}
		if n.ID == "" {
			n.ID = meta.ID
		}
		op, opErr := applyClassified(ctx, res, graph, cls, ap, n, dryRun)
		if opErr != nil {
			return res, opErr
		}
		res.Ops = append(res.Ops, op)
		switch op.Action {
		case "add":
			res.Counts.Added++
		case "update":
			res.Counts.Updated++
		case "supersede":
			res.Counts.Superseded++
		default:
			res.Counts.Noop++
		}
	}
	return res, nil
}

// promoteApplier writes into a live local target vault: node files through
// the target node.Store, and — when an embeddings store is open — the
// target's embeddings.db, embedding each written node with the target
// vault's embedder. embStore/embedder are nil when embedding is disabled
// (config/embedder/store setup failed, or dry-run); in that case node files
// are still written and only the embedding side effect is skipped.
type promoteApplier struct {
	store    *node.Store
	embStore *embedding.Store
	embedder embedding.Embedder
}

func (a promoteApplier) writeNode(res *ContributeResult, n *node.Node) error {
	path, existed := targetNodeFile(a.store, n.ID)
	if err := a.store.SaveNode(n); err != nil {
		return fmt.Errorf("write node %s: %w", n.ID, err)
	}
	if path != "" {
		res.recordWrite(path, existed)
	}
	a.upsertEmbedding(res, n)
	return nil
}

func (a promoteApplier) retireNode(res *ContributeResult, retired *node.Node) error {
	path, existed := targetNodeFile(a.store, retired.ID)
	if err := a.store.SaveNode(retired); err != nil {
		return err
	}
	if path != "" {
		res.recordWrite(path, existed)
	}
	// Mirror the live MCP supersede path: keep the embedding row, flip its
	// status to superseded (best-effort — a stale row only affects ranking
	// and is corrected on the next reindex).
	if a.embStore != nil {
		_ = a.embStore.UpdateStatus(retired.ID, node.StatusSuperseded)
	}
	return nil
}

// upsertEmbedding embeds n with the target vault's embedder and upserts it
// into the target's embeddings.db using the same embed-text formula and hash
// as the live write paths (warren.EmbedText + sha256 hex). The node file is
// already durably written, so every failure here degrades to a per-node
// warning — knowledge is never lost, and the vector regenerates on the next
// reindex.
func (a promoteApplier) upsertEmbedding(res *ContributeResult, n *node.Node) {
	if a.embStore == nil || a.embedder == nil {
		return
	}
	text := warren.EmbedText(n)
	if text == "" {
		return
	}
	vec, err := a.embedder.Embed(text)
	if err != nil {
		res.Warnings = append(res.Warnings, fmt.Sprintf("node %s written but embedding failed (%v); it will be re-embedded on the next reindex", n.ID, err))
		return
	}
	// UpsertChecked, not Upsert: the target vault's store may already hold
	// rows from a different embedding model, and one mixed row would poison
	// every future search of it (F2) — refuse and warn instead.
	if err := a.embStore.UpsertChecked(n.ID, vec, sha256Hex(text), a.embedder.Model()); err != nil {
		res.Warnings = append(res.Warnings, fmt.Sprintf("node %s written but embedding upsert failed (%v); it will be re-embedded on the next reindex", n.ID, err))
	}
}

// sha256Hex is the summary-hash formula shared with the MCP/warren write
// paths (staleness detection keys on it).
func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}
