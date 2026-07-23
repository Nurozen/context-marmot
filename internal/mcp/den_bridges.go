package mcp

// Den-scoped bridge loading (plan §7): when the served dir is a den vault, the
// den's own _bridges/ (a consumer-owned cross-vault edge policy, sibling of
// _den.md and vault/) is loaded ADDITIONALLY and merged into the namespace
// manager's cross-vault bridges, so cross-vault edge validation and federated
// traversal honor edges the den author declared without touching any served
// vault. The den bridge set is engine-construction state: it survives later
// warren reloads because setWarrenBridges re-appends e.denBridges on every
// recompose.

import (
	"path/filepath"

	"github.com/nurozen/context-marmot/internal/den"
	"github.com/nurozen/context-marmot/internal/namespace"
)

// denRootFor returns the den directory (the dir holding _den.md, sibling of the
// identity vault) for a served vault dir, probing the same two placements
// denManifestFor uses: a den identity vault served from …/dens/<id>/vault with
// _den.md one level up, or a links-only den root served with _den.md inside it.
// The bool is false when the served dir is not den-shaped.
func denRootFor(marmotDir string) (string, bool) {
	if marmotDir == "" {
		return "", false
	}
	up := filepath.Dir(marmotDir)
	if m, _, err := den.LoadManifestAt(filepath.Join(up, den.ManifestFileName)); err == nil && m != nil && m.DenID != "" {
		return up, true
	}
	if m, _, err := den.LoadManifestAt(filepath.Join(marmotDir, den.ManifestFileName)); err == nil && m != nil && m.DenID != "" {
		return marmotDir, true
	}
	return "", false
}

// LoadDenBridges loads the served den's _bridges/ manifests and merges them into
// NSManager.CrossVaultBridges (after file-declared and warren runtime bridges),
// then rebuilds the vault registry so cross-vault edge validation and federated
// traversal see them. A non-den serve is a no-op; a missing/malformed _bridges
// dir yields no bridges (per-file errors already skipped by the loader). Call
// after LoadDenLinks so the registry's den-link vault set is already seeded.
func (e *Engine) LoadDenBridges() error {
	root, ok := denRootFor(e.MarmotDir)
	if !ok {
		return nil
	}
	bridges, err := den.ListBridgesAt(root)
	if err != nil {
		return err
	}
	e.setDenBridges(bridges)
	// Rebuild the registry through the warren reload path so the routing table
	// keeps active warren mounts folded in (a bare routes.Load() would drop
	// edit-mode mount routes). ReloadWarrenState's setWarrenBridges re-appends
	// e.denBridges, so the merged bridge set is authoritative afterwards.
	if e.VaultRegistry != nil {
		return e.ReloadWarrenState()
	}
	return nil
}

// setDenBridges stores the den bridge set and recomposes
// NSManager.CrossVaultBridges = fileCrossVaultBridges ++ warrenBridges ++
// denBridges. When den bridges appear on a previously namespace-less engine an
// empty manager is created so cross-vault edge validation engages (mirrors
// setWarrenBridges). Registry-less unit-test engines rely on this recompose
// because ReloadWarrenState is a no-op without a registry.
func (e *Engine) setDenBridges(bridges []*namespace.Bridge) {
	e.nsMgrMu.Lock()
	defer e.nsMgrMu.Unlock()
	e.denBridges = bridges
	if e.NSManager == nil {
		if len(bridges) == 0 {
			return
		}
		e.NSManager = emptyNamespaceManager(e.MarmotDir)
		e.fileCrossVaultBridges = nil
	}
	merged := append([]*namespace.Bridge(nil), e.fileCrossVaultBridges...)
	merged = append(merged, e.warrenBridges...)
	merged = append(merged, e.denBridges...)
	e.NSManager.CrossVaultBridges = merged
}
