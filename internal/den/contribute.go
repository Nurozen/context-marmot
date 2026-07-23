package den

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/nurozen/context-marmot/internal/classifier"
	"github.com/nurozen/context-marmot/internal/config"
	"github.com/nurozen/context-marmot/internal/embedding"
	"github.com/nurozen/context-marmot/internal/llm"
	"github.com/nurozen/context-marmot/internal/node"
	"github.com/nurozen/context-marmot/internal/warren"
)

// FlowCounts summarizes a contribute (or promote) flow's per-node outcomes.
type FlowCounts struct {
	Added      int `json:"added"`
	Updated    int `json:"updated"`
	Superseded int `json:"superseded"`
	Noop       int `json:"noop"`
}

// Changes is the number of operations that mutate the target (everything
// except NOOP). Zero changes means contribute has nothing to commit.
func (c FlowCounts) Changes() int { return c.Added + c.Updated + c.Superseded }

// ContributeOp describes one planned (dry-run) or applied contribute
// operation for a single den node.
type ContributeOp struct {
	Action   string // "add" | "update" | "supersede" | "noop"
	NodeID   string // den-side node id
	TargetID string // target-side node id (differs from NodeID on classifier update/supersede)
}

// String renders the op in the dry-run vocabulary the CLI prints.
func (op ContributeOp) String() string {
	switch op.Action {
	case "supersede":
		return fmt.Sprintf("supersede %s with %s", op.TargetID, op.NodeID)
	case "update":
		return fmt.Sprintf("update node %s", op.TargetID)
	case "add":
		return fmt.Sprintf("add node %s", op.NodeID)
	default:
		return fmt.Sprintf("noop node %s", op.NodeID)
	}
}

// ContributeResult is the engine outcome: counts, the per-node op list (in
// deterministic node-id order), and non-fatal warnings.
type ContributeResult struct {
	Counts   FlowCounts
	Ops      []ContributeOp
	Warnings []string

	// CreatedFiles and ModifiedFiles record — in write order, as absolute
	// paths inside the target mount — exactly which node files the applied
	// run created (did not exist before) or modified (existed before).
	// Dry-run leaves both empty. The cmd layer uses them to restore the
	// checkout precisely on failure recovery (`git clean` the created files,
	// `git checkout --` the modified ones), and Contribute returns the
	// partial result alongside a non-nil error so a mid-run failure still
	// reports every file it touched.
	CreatedFiles  []string
	ModifiedFiles []string
}

// recordWrite tracks the file effect of one node write for failure recovery.
func (r *ContributeResult) recordWrite(path string, existed bool) {
	if existed {
		for _, p := range r.CreatedFiles {
			if p == path {
				return // created earlier in this run; recovery removes it
			}
		}
		r.ModifiedFiles = append(r.ModifiedFiles, path)
		return
	}
	r.CreatedFiles = append(r.CreatedFiles, path)
}

// newClassifierLLM builds the target vault's classifier LLM. Test seam over
// config.NewClassifierLLM so unit tests can inject a fake llm.Provider.
var newClassifierLLM = config.NewClassifierLLM

// targetGraph adapts the target project's node.Store to
// classifier.GraphReader (lookup by node id, best-effort).
type targetGraph struct{ store *node.Store }

func (g targetGraph) GetNode(id string) (*node.Node, bool) {
	path, err := g.store.SafeNodePath(id)
	if err != nil {
		return nil, false
	}
	if _, statErr := os.Stat(path); statErr != nil {
		return nil, false
	}
	n, err := g.store.LoadNode(path)
	if err != nil {
		return nil, false
	}
	return n, true
}

// Contribute folds the den vault's ACTIVE nodes into the target warren
// project mount. It performs no git operations (the cmd layer owns staging;
// internal packages stay exec-free); it writes node files through
// warren.WriteEditableNodeFile (which keeps the fail-closed readonly
// re-read) — including the supersede retirement, so every mutation of the
// target passes the same per-write policy re-check. It NEVER touches the
// target's embeddings.db: git carries the markdown, and consumers
// regenerate embeddings on consume (reindex/reembed after the contribute PR
// merges). Classifier READS of the target's embeddings.db stay read-only.
//
// On error the returned result is non-nil and partial: its
// CreatedFiles/ModifiedFiles cover every file written before the failure,
// so the caller can restore the checkout exactly.
//
// Per node the pipeline is:
//  1. Deterministic pass (idempotency backbone): same id in the target and
//     identical content -> NOOP; same id, different content -> UPDATE.
//  2. Classifier pass for ids absent from the target, using the TARGET
//     vault's embedder and embeddings.db. NOOP -> skip, UPDATE/SUPERSEDE ->
//     apply against TargetNodeID, ADD -> create. Any classifier or
//     embedding-setup error degrades to ADD with a warning; classification
//     never fails the whole contribute.
//
// Namespace mapping is v1-identity: nodes are written under their existing
// namespace/id unchanged.
//
// With dryRun the same passes run read-only and nothing is written.
func Contribute(ctx context.Context, vaultDir string, mount warren.ProjectStatus, dryRun bool) (*ContributeResult, error) {
	res := &ContributeResult{Warnings: []string{}}

	if !mount.Editable {
		return nil, fmt.Errorf("warren project %q is not editable in this workspace", mount.ProjectID)
	}
	// Author-side write-policy backstop before ANY mutation, mirroring
	// warren.WriteEditableNodeFile: an up-front fail-closed manifest check
	// makes the refusal visible before the engine plans anything (each write
	// re-checks again through WriteEditableNodeFile).
	if !dryRun && mount.WarrenPath != "" {
		manifest, _, loadErr := warren.LoadManifest(mount.WarrenPath)
		if loadErr != nil {
			return nil, fmt.Errorf("warren manifest unreadable at %s; refusing contribute to project %q: %w", mount.WarrenPath, mount.ProjectID, loadErr)
		}
		for _, project := range manifest.Projects {
			if project.ProjectID == mount.ProjectID && project.ReadOnly {
				return nil, fmt.Errorf("warren author marked project %q read-only; edits must go through the warren repository itself", mount.ProjectID)
			}
		}
	}

	srcStore := node.NewStore(vaultDir)
	metas, err := srcStore.ListActiveNodes()
	if err != nil {
		return nil, fmt.Errorf("list den vault nodes: %w", err)
	}
	sort.Slice(metas, func(i, j int) bool { return metas[i].ID < metas[j].ID })

	tgtStore := node.NewStore(mount.Path)
	graph := targetGraph{store: tgtStore}
	ap := contributeApplier{store: tgtStore, mount: mount}

	// Classification environment over the TARGET vault. Every setup failure
	// only disables classification (unmatched ids become ADDs) — it never
	// fails the contribute.
	var embedder embedding.Embedder
	var targetCfg *config.VaultConfig
	if cfg, cfgErr := config.Load(mount.Path); cfgErr != nil {
		res.Warnings = append(res.Warnings, fmt.Sprintf("target vault config unreadable (%v); classification disabled, unmatched nodes added directly", cfgErr))
	} else if emb, embErr := config.NewEmbedderFromVault(cfg); embErr != nil {
		res.Warnings = append(res.Warnings, fmt.Sprintf("target embedder unavailable (%v); classification disabled, unmatched nodes added directly", embErr))
	} else {
		embedder = emb
		targetCfg = cfg
	}
	var cls *classifier.Classifier
	if embedder != nil {
		dbPath := filepath.Join(mount.Path, ".marmot-data", "embeddings.db")
		if _, statErr := os.Stat(dbPath); statErr == nil {
			embStore, dbErr := embedding.NewStoreReadOnly(dbPath)
			if dbErr != nil {
				res.Warnings = append(res.Warnings, fmt.Sprintf("target embeddings unreadable (%v); unmatched nodes added directly", dbErr))
			} else {
				defer func() { _ = embStore.Close() }()
				// The TARGET vault's configured classifier LLM (same
				// construction as serve/reindex: openai/anthropic when the
				// key resolves, else nil → embedding-distance fallback).
				// Classify errors still degrade to ADD+warning per node.
				clsLLM, _ := newClassifierLLM(targetCfg, mount.Path)
				cls = &classifier.Classifier{Store: embStore, Embedder: embedder, LLM: clsLLM}
			}
		}
		// A missing embeddings.db is the normal fresh-target case: no
		// warning, unmatched ids are plain ADDs.
	}

	for i := range metas {
		meta := metas[i]
		n, loadErr := srcStore.LoadNode(meta.FilePath)
		if loadErr != nil {
			res.Warnings = append(res.Warnings, fmt.Sprintf("skipped unreadable den node %s: %v", meta.ID, loadErr))
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

// applier is the write backend the shared classify-and-apply core drives.
// Contribute writes node files only (through warren.WriteEditableNodeFile,
// no embeddings.db touch); Promote writes through the target's live node
// store AND maintains its embeddings.db. Each method owns its own embedding
// side effect (if any) and its per-node degradation, and records the file
// effect on res for failure recovery.
type applier interface {
	// writeNode persists n active into the target and records the file
	// effect. A returned error is fatal to the current node (the core may
	// wrap it); embedding-shaped problems degrade to a warning on res
	// instead of an error.
	writeNode(res *ContributeResult, n *node.Node) error
	// retireNode persists the already-retired node (status/valid_until/
	// superseded_by set by the core) into the target, recording the file
	// effect and applying the backend's embedding-row policy.
	retireNode(res *ContributeResult, retired *node.Node) error
}

// applyClassified classifies and (unless dryRun) applies a single source node
// against the target via ap. It is the classification core shared by
// Contribute (file-only, no embeddings) and Promote (live store +
// embeddings.db): deterministic id pass first, classifier second pass for
// unmatched ids, with the same NOOP/UPDATE/SUPERSEDE/ADD vocabulary and the
// same supersede ordering (replacement written before the old node is
// retired; a retirement failure degrades to a warning).
func applyClassified(ctx context.Context, res *ContributeResult, graph targetGraph, cls *classifier.Classifier, ap applier, n *node.Node, dryRun bool) (ContributeOp, error) {
	// Deterministic first pass: same id present in the target. A superseded
	// target with identical content still counts as an update (reactivation),
	// never a noop.
	if existing, ok := graph.GetNode(n.ID); ok {
		if existing.IsActive() && sameNodeContent(existing, n) {
			return ContributeOp{Action: "noop", NodeID: n.ID, TargetID: existing.ID}, nil
		}
		op := ContributeOp{Action: "update", NodeID: n.ID, TargetID: existing.ID}
		if dryRun {
			return op, nil
		}
		out := *n
		out.Status = node.StatusActive
		if existing.ValidFrom != "" {
			out.ValidFrom = existing.ValidFrom // keep the target's creation time
		}
		return op, ap.writeNode(res, &out)
	}

	// Classifier second pass for ids absent from the target.
	action := llm.ActionADD
	targetID := ""
	if cls != nil {
		cres, cerr := cls.Classify(ctx, n, graph)
		if cerr != nil {
			res.Warnings = append(res.Warnings, fmt.Sprintf("classify %s failed (%v); adding directly", n.ID, cerr))
		} else {
			action = cres.Action
			targetID = cres.TargetNodeID
		}
	}
	switch action {
	case llm.ActionNOOP:
		if _, ok := graph.GetNode(targetID); ok {
			return ContributeOp{Action: "noop", NodeID: n.ID, TargetID: targetID}, nil
		}
		// Target vanished under us: fall through to ADD (mirrors MCP).
	case llm.ActionUPDATE:
		if tn, ok := graph.GetNode(targetID); ok {
			op := ContributeOp{Action: "update", NodeID: n.ID, TargetID: targetID}
			if dryRun {
				return op, nil
			}
			// Apply the den content onto the target node: same-concept,
			// richer content. Identity fields (id, namespace, edges,
			// valid_from) stay the target's.
			tn.Summary = n.Summary
			tn.Context = n.Context
			if n.Type != "" {
				tn.Type = n.Type
			}
			if len(n.Tags) > 0 {
				tn.Tags = n.Tags
			}
			tn.Status = node.StatusActive
			return op, ap.writeNode(res, tn)
		}
	case llm.ActionSUPERSEDE:
		if targetID != "" && targetID != n.ID {
			if old, ok := graph.GetNode(targetID); ok {
				op := ContributeOp{Action: "supersede", NodeID: n.ID, TargetID: targetID}
				if dryRun {
					return op, nil
				}
				// Write the REPLACEMENT first: a failure here leaves the
				// target untouched (never a superseded node with no
				// successor). Only then retire the old node; a retirement
				// failure after a successful replacement write degrades to a
				// warning — the replacement exists, and the retirement is
				// retryable.
				out := *n
				out.Status = node.StatusActive
				if out.ValidFrom == "" {
					out.ValidFrom = time.Now().UTC().Format(time.RFC3339)
				}
				if err := ap.writeNode(res, &out); err != nil {
					return op, fmt.Errorf("supersede %s with %s: %w", targetID, n.ID, err)
				}
				retired := *old
				retired.Status = node.StatusSuperseded
				retired.ValidUntil = time.Now().UTC().Format(time.RFC3339)
				retired.SupersededBy = n.ID
				if err := ap.retireNode(res, &retired); err != nil {
					res.Warnings = append(res.Warnings, fmt.Sprintf(
						"replacement %s written but retiring %s failed (%v); re-running contribute will no-op — retire node %s manually (context_delete it, or supersede it via normal tooling)", n.ID, targetID, err, targetID))
				}
				return op, nil
			}
		}
	}

	// ADD (default and every degraded path).
	op := ContributeOp{Action: "add", NodeID: n.ID}
	if dryRun {
		return op, nil
	}
	out := *n
	out.Status = node.StatusActive
	if out.ValidFrom == "" {
		out.ValidFrom = time.Now().UTC().Format(time.RFC3339)
	}
	return op, ap.writeNode(res, &out)
}

// contributeApplier is the file-only write backend: node files go through
// warren.WriteEditableNodeFile (per-write fail-closed policy re-check) and
// the target's embeddings.db is NEVER touched — git carries the markdown and
// consumers regenerate embeddings on consume (reindex/reembed after the
// contribute PR merges).
type contributeApplier struct {
	store *node.Store
	mount warren.ProjectStatus
}

func (a contributeApplier) writeNode(res *ContributeResult, n *node.Node) error {
	path, existed := targetNodeFile(a.store, n.ID)
	if err := warren.WriteEditableNodeFile(a.mount, n); err != nil {
		return fmt.Errorf("write node %s: %w", n.ID, err)
	}
	if path != "" {
		res.recordWrite(path, existed)
	}
	return nil
}

func (a contributeApplier) retireNode(res *ContributeResult, retired *node.Node) error {
	path, existed := targetNodeFile(a.store, retired.ID)
	if err := warren.WriteEditableNodeFile(a.mount, retired); err != nil {
		return err
	}
	if path != "" {
		res.recordWrite(path, existed)
	}
	return nil
}

// targetNodeFile resolves a target node id to its on-disk file and whether
// it already exists (best-effort; an unresolvable id yields "").
func targetNodeFile(tgtStore *node.Store, id string) (path string, existed bool) {
	p, err := tgtStore.SafeNodePath(id)
	if err != nil {
		return "", false
	}
	_, statErr := os.Stat(p)
	return p, statErr == nil
}

// sameNodeContent reports whether the target node already carries the den
// node's content. It compares the fields the contribute write path actually
// persists (type, tags, summary, context, edges) — RawBody is deliberately
// excluded because node.RenderNode canonicalizes the body to
// summary/relationships/context sections, so raw-body comparison would
// defeat idempotency for hand-written den files.
func sameNodeContent(a, b *node.Node) bool {
	if a.Type != b.Type || a.Summary != b.Summary || a.Context != b.Context {
		return false
	}
	if len(a.Tags) != len(b.Tags) {
		return false
	}
	for i := range a.Tags {
		if a.Tags[i] != b.Tags[i] {
			return false
		}
	}
	if len(a.Edges) != len(b.Edges) {
		return false
	}
	for i := range a.Edges {
		if a.Edges[i].Target != b.Edges[i].Target || a.Edges[i].Relation != b.Edges[i].Relation {
			return false
		}
	}
	return true
}
