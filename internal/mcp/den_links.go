package mcp

// Federated den-link queries (plan §6 + §15.5): a served den's _den.md links
// are resolved to (vault id, vault dir) pairs and fed to the VaultRegistry so
// context_query fans out across them, and each remote vault is searched with
// a query vector in THAT vault's embedding model (per-link embedding
// federation) — a remote store built with a different model would silently
// return nothing for the local query vector.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/nurozen/context-marmot/internal/config"
	"github.com/nurozen/context-marmot/internal/den"
	"github.com/nurozen/context-marmot/internal/embedding"
	"github.com/nurozen/context-marmot/internal/routes"
	"github.com/nurozen/context-marmot/internal/warren"
)

// denManifestFor probes the two den manifest placements relative to a served
// vault dir: a den identity vault is served from …/dens/<id>/vault with
// _den.md one level up; a links-only den is served from the den root with
// _den.md inside it (the same probe collectTopology uses). Returns nil when
// the served dir is not den-shaped.
func denManifestFor(marmotDir string) *den.Manifest {
	if marmotDir == "" {
		return nil
	}
	for _, p := range []string{
		filepath.Join(filepath.Dir(marmotDir), den.ManifestFileName),
		filepath.Join(marmotDir, den.ManifestFileName),
	} {
		m, _, err := den.LoadManifestAt(p)
		if err == nil && m != nil && m.DenID != "" {
			return m
		}
	}
	return nil
}

// denLinkKey identifies a link for resolution-state lookups (the manifest
// dedupes on the same tuple, so the key is unique per manifest).
func denLinkKey(l den.Link) string {
	return l.Mode + "\x00" + l.Target + "\x00" + l.Warren + "\x00" + l.Project
}

// LoadDenLinks resolves the served den's _den.md links into remote vaults and
// feeds them to the vault registry so context_query federates across them.
// Per-link failures degrade to an "unresolved" link (warned on stderr, shown
// in the MCP instructions), never a startup failure. A non-den serve is a
// no-op. Call after WithVaultRegistry/ReloadWarrenState; the den vault set
// survives later ReloadWarrenState rebuilds (see VaultRegistry.SetDenVaults).
func (e *Engine) LoadDenLinks() error {
	m := denManifestFor(e.MarmotDir)
	if m == nil || len(m.Links) == 0 {
		return nil
	}
	rt, _ := routes.Load() // best-effort, same posture as ReloadWarrenState
	if rt == nil {
		rt = routes.EmptyTable()
	}
	mounts, mountsErr := warren.ActiveMounts(e.MarmotDir)
	if mountsErr != nil {
		// Only edit-mode links need mounts; live/link resolution proceeds.
		fmt.Fprintf(os.Stderr, "warning: den links: warren mounts unavailable: %v\n", mountsErr)
	}

	extras := make(map[string]string)
	resolutions := make(map[string]string)
	overrides := make(map[string]den.LinkEmbedding)
	for _, l := range m.Links {
		vid, dir, err := resolveDenLink(l, rt, mounts)
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning: den link %s (mode=%s) unresolved: %v\n", l.Target, l.Mode, err)
			continue
		}
		resolutions[denLinkKey(l)] = vid
		if l.Embedding != (den.LinkEmbedding{}) {
			overrides[vid] = l.Embedding
		}
		// Edit-mode vault dirs are routed by the warren mount reload path
		// (ActiveMounts -> ReloadWarrenState -> routing table): the editable
		// mount copy must win, not the shared checkout. A link resolving to
		// the local vault id never claims a route (self shadowing). Likewise,
		// a pinned/live link whose vault ALSO has an active editable mount in
		// this den (--edit and --link to the same vault) must not claim a
		// denVaults entry: the registry would serve the shared-checkout
		// snapshot and the agent would stop seeing its own pending edits —
		// the mount path wins.
		if l.Mode != den.LinkModeEdit {
			if editMount := editableMountFor(mounts, l, vid); editMount != nil {
				fmt.Fprintf(os.Stderr, "note: den link %s (mode=%s) also has an editable mount for %s/%s; queries read the edit worktree (pending edits win over the pinned snapshot)\n",
					l.Target, l.Mode, editMount.WarrenID, editMount.ProjectID)
				continue
			}
		}
		if l.Mode != den.LinkModeEdit && vid != e.LocalVaultID && dir != "" {
			extras[vid] = dir
		}
	}

	e.denLinksMu.Lock()
	e.denLinkResolutions = resolutions
	e.denLinkEmbeddings = overrides
	e.denLinksMu.Unlock()

	if e.VaultRegistry != nil {
		e.VaultRegistry.SetDenVaults(extras)
	}
	return nil
}

// resolveDenLink maps one den link to the (vault id, vault dir) it serves.
//   - edit: the editable warren mount already registered in workspace state —
//     resolution only reports its vault id (the mount path participates in
//     cross-vault reads via ReloadWarrenState's routing table).
//   - live: Target is a routed vault id (routes first) or a den id whose
//     identity vault at $MARMOT_HOME/dens/<target>/vault answers.
//   - link: a pinned warren project served from the warren's shared cache
//     checkout; vault id from project metadata (_warren.md), else _config.md.
func resolveDenLink(l den.Link, rt *routes.RoutingTable, mounts []warren.ProjectStatus) (vaultID, vaultDir string, err error) {
	switch l.Mode {
	case den.LinkModeEdit:
		for _, mnt := range mounts {
			if mnt.WarrenID == l.Warren && mnt.ProjectID == l.Project && mnt.VaultID != "" {
				return mnt.VaultID, mnt.Path, nil
			}
		}
		return "", "", fmt.Errorf("no available mount for %s/%s in workspace state", l.Warren, l.Project)
	case den.LinkModeLive:
		if p, ok := rt.Get(l.Target); ok {
			return l.Target, p, nil
		}
		dir := den.VaultPath(l.Target)
		if vid := warren.LocalVaultID(dir); vid != "" {
			return vid, dir, nil
		}
		return "", "", fmt.Errorf("target %q is neither a routed vault id nor a den with an identity vault", l.Target)
	case den.LinkModeLink:
		entry, ok := warren.CacheWorkspaceWarren(l.Warren)
		if !ok {
			return "", "", fmt.Errorf("warren %q has no shared cache checkout (run 'marmot warren sync %s')", l.Warren, l.Warren)
		}
		manifest, _, merr := warren.LoadManifest(entry.Path)
		if merr != nil {
			return "", "", fmt.Errorf("warren manifest at %s: %w", entry.Path, merr)
		}
		for _, p := range manifest.Projects {
			if p.ProjectID != l.Project && !slices.Contains(p.Aliases, l.Project) {
				continue
			}
			dir := filepath.Join(entry.Path, filepath.FromSlash(p.Path))
			if meta, _, mdErr := warren.LoadProjectMetadata(dir); mdErr == nil && meta.VaultID != "" {
				return meta.VaultID, dir, nil
			}
			if vid := warren.LocalVaultID(dir); vid != "" {
				return vid, dir, nil
			}
			return "", "", fmt.Errorf("project %q at %s has no vault identity (_warren.md vault_id or _config.md)", l.Project, dir)
		}
		return "", "", fmt.Errorf("project %q not found in warren %q manifest", l.Project, l.Warren)
	default:
		return "", "", fmt.Errorf("unknown link mode %q", l.Mode)
	}
}

// editableMountFor returns the active EDITABLE warren mount that shadows a
// non-edit den link (nil when none): same vault id, or same warren/project
// pair for warren-backed links. Used by LoadDenLinks to keep the edit
// worktree authoritative when a den holds both --edit and --link into the
// same vault.
func editableMountFor(mounts []warren.ProjectStatus, l den.Link, vid string) *warren.ProjectStatus {
	for i := range mounts {
		mnt := &mounts[i]
		if !mnt.Editable {
			continue
		}
		if vid != "" && mnt.VaultID == vid {
			return mnt
		}
		if l.Warren != "" && l.Project != "" && mnt.WarrenID == l.Warren && mnt.ProjectID == l.Project {
			return mnt
		}
	}
	return nil
}

// DenLinkResolvedVaultID reports the vault id a den link resolved to at
// LoadDenLinks time ("", false = unresolved or never loaded).
func (e *Engine) DenLinkResolvedVaultID(l den.Link) (string, bool) {
	e.denLinksMu.RLock()
	defer e.denLinksMu.RUnlock()
	vid, ok := e.denLinkResolutions[denLinkKey(l)]
	return vid, ok
}

// DenLinkedVaultIDs returns the deduped, sorted vault ids the served den's
// links resolved to at LoadDenLinks time (the local vault id excluded). The
// HTTP search handler fans /api/search out across exactly this set so den-link
// federation matches context_query; empty for a non-den serve.
func (e *Engine) DenLinkedVaultIDs() []string {
	e.denLinksMu.RLock()
	defer e.denLinksMu.RUnlock()
	seen := make(map[string]bool, len(e.denLinkResolutions))
	ids := make([]string, 0, len(e.denLinkResolutions))
	for _, vid := range e.denLinkResolutions {
		if vid == "" || vid == e.LocalVaultID || seen[vid] {
			continue
		}
		seen[vid] = true
		ids = append(ids, vid)
	}
	slices.Sort(ids)
	return ids
}

// DenLinkStatus is one _den.md link plus its resolution state, shaped for API
// consumers (/api/warrens den_links). State is "resolved" or "unresolved".
type DenLinkStatus struct {
	Target          string `json:"target"`
	Mode            string `json:"mode"`
	State           string `json:"state"`
	ResolvedVaultID string `json:"resolved_vault_id,omitempty"`
}

// DenLinkStatuses reads the served den's manifest (same probe as the MCP
// instructions topology) and reports each link with its LoadDenLinks
// resolution state. Nil when the served dir is not den-shaped.
func (e *Engine) DenLinkStatuses() []DenLinkStatus {
	m := denManifestFor(e.MarmotDir)
	if m == nil {
		return nil
	}
	out := make([]DenLinkStatus, 0, len(m.Links))
	for _, l := range m.Links {
		st := DenLinkStatus{Target: l.Target, Mode: l.Mode, State: "unresolved"}
		if vid, ok := e.DenLinkResolvedVaultID(l); ok {
			st.State = "resolved"
			st.ResolvedVaultID = vid
		}
		out = append(out, st)
	}
	return out
}

// denLinkEmbeddingFor returns the per-link embedding override for a resolved
// den-link vault, if the link declared one.
func (e *Engine) denLinkEmbeddingFor(vaultID string) (den.LinkEmbedding, bool) {
	e.denLinksMu.RLock()
	defer e.denLinksMu.RUnlock()
	le, ok := e.denLinkEmbeddings[vaultID]
	return le, ok
}

// remoteQueryState caches per-request query vectors (by model) and per-vault
// embedders so one context_query builds each at most once. Deliberately
// request-scoped, not engine-global: link embedders can hold credentials
// resolved from env at query time and must not outlive the request.
type remoteQueryState struct {
	vecs      map[string][]float32          // model -> query vector
	embedders map[string]embedding.Embedder // vault id -> embedder
}

// remoteQueryVector picks the query vector + model for searching a remote
// vault's embedding store. When the store carries the local model, the local
// query vector is reused. On a model mismatch, the vault's own embedder (den
// link override first, else the remote vault's _config.md) re-embeds the
// query; any failure skips the vault with a once-per-vault warning naming the
// mismatch, so local results always survive.
func (e *Engine) remoteQueryVector(ctx context.Context, vaultID string, store *embedding.Store, query string, st *remoteQueryState) ([]float32, string, bool) {
	localModel := e.Embedder.Model()
	models, err := store.Models()
	if err != nil || len(models) == 0 {
		// Introspection failure or empty store: search with the local model,
		// matching pre-federation behavior (an empty store returns nothing).
		return st.vecs[localModel], localModel, true
	}
	if slices.Contains(models, localModel) {
		return st.vecs[localModel], localModel, true
	}

	emb, err := e.remoteEmbedderFor(vaultID, st)
	if err != nil {
		e.warnVaultOnce(vaultID, "vault %q embeddings use model(s) %s but the local query model is %q and no per-link embedder is available (%v); excluded from context_query", vaultID, strings.Join(models, ","), localModel, err)
		return nil, "", false
	}
	remoteModel := emb.Model()
	if !slices.Contains(models, remoteModel) {
		e.warnVaultOnce(vaultID, "vault %q embeddings use model(s) %s but its resolved embedder produces %q (local model %q); excluded from context_query", vaultID, strings.Join(models, ","), remoteModel, localModel)
		return nil, "", false
	}
	if vec, ok := st.vecs[remoteModel]; ok {
		return vec, remoteModel, true
	}
	vec, err := embedWithContext(ctx, emb, query)
	if err != nil {
		e.warnVaultOnce(vaultID, "vault %q per-link query embedding (%s) failed: %v; excluded from context_query", vaultID, remoteModel, err)
		return nil, "", false
	}
	st.vecs[remoteModel] = vec
	return vec, remoteModel, true
}

// RemoteQueryState is the exported handle over the request-scoped per-model
// query-vector / per-vault embedder cache. The HTTP search handler (package
// api) uses it to mirror context_query's per-vault-model federation without
// reaching into mcp internals.
type RemoteQueryState = remoteQueryState

// NewRemoteQueryState seeds a request-scoped state with the local model's
// query vector, so remote vaults that carry the local model reuse it and only
// mismatched vaults trigger a re-embed. Call once per HTTP search request.
func (e *Engine) NewRemoteQueryState(localVec []float32) *RemoteQueryState {
	return &remoteQueryState{
		vecs:      map[string][]float32{e.Embedder.Model(): localVec},
		embedders: make(map[string]embedding.Embedder),
	}
}

// RemoteQueryVector exposes remoteQueryVector to package api so the HTTP search
// path picks each remote vault's query vector + model exactly as context_query
// does (reuse local vector on model match, re-embed via the vault's own
// embedder on mismatch, skip-with-warning on failure).
func (e *Engine) RemoteQueryVector(ctx context.Context, vaultID string, store *embedding.Store, query string, st *RemoteQueryState) ([]float32, string, bool) {
	return e.remoteQueryVector(ctx, vaultID, store, query, st)
}

// remoteEmbedderFor builds (or returns the request-cached) embedder for a
// remote vault: the den link's embedding override when declared (key_ref is
// env-refs-only in v1), otherwise the remote vault's own _config.md via
// config.NewEmbedderFromVault.
func (e *Engine) remoteEmbedderFor(vaultID string, st *remoteQueryState) (embedding.Embedder, error) {
	if emb, ok := st.embedders[vaultID]; ok {
		return emb, nil
	}
	var emb embedding.Embedder
	if override, ok := e.denLinkEmbeddingFor(vaultID); ok && (override.Provider != "" || override.Model != "") {
		key, err := resolveEmbeddingKeyRef(override.KeyRef)
		if err != nil {
			return nil, err
		}
		built, err := embedding.NewEmbedder(override.Provider, override.Model, key)
		if err != nil {
			return nil, fmt.Errorf("den link embedding override: %w", err)
		}
		emb = built
	} else {
		if e.VaultRegistry == nil {
			return nil, fmt.Errorf("no vault registry")
		}
		cfg, err := e.VaultRegistry.ResolveConfig(vaultID)
		if err != nil {
			return nil, err
		}
		built, err := config.NewEmbedderFromVault(cfg)
		if err != nil {
			return nil, err
		}
		emb = built
	}
	st.embedders[vaultID] = emb
	return emb, nil
}

// resolveEmbeddingKeyRef resolves a den link's embedding key_ref. v1 supports
// env references only ("env:VAR"); empty means no key.
func resolveEmbeddingKeyRef(ref string) (string, error) {
	if ref == "" {
		return "", nil
	}
	name, ok := strings.CutPrefix(ref, "env:")
	if !ok || name == "" {
		return "", fmt.Errorf("unsupported key_ref %q (only env:VAR is supported)", ref)
	}
	val := os.Getenv(name)
	if val == "" {
		return "", fmt.Errorf("key_ref env var %s is unset", name)
	}
	return val, nil
}
