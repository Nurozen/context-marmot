package mcp

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/nurozen/context-marmot/internal/namespace"
	"github.com/nurozen/context-marmot/internal/routes"
	"github.com/nurozen/context-marmot/internal/warren"
)

// ReloadWarrenState re-reads routes.yml and the workspace _warren.md, folds
// active warren mounts into a fresh routing table, recomputes warren runtime
// bridges, and rebuilds the vault registry in place. Safe to call on a live
// engine at any time; concurrent searches keep working against the registry
// (see VaultRegistry.Rebuild). It is the single freshness path behind the
// refresh endpoint, the `marmot warren refresh` command, and the daemon
// owner's _warren.md watcher.
//
// Engines without a vault registry (zero-value unit-test engines) and
// engines that have begun Close are a no-op.
func (e *Engine) ReloadWarrenState() error {
	if e.VaultRegistry == nil || e.closing.Load() {
		return nil
	}
	// One reload at a time: each reload reads state (routes.yml, _warren.md)
	// and then applies it; interleaved reloads could apply a stale snapshot
	// last (see the reloadMu field doc).
	e.reloadMu.Lock()
	defer e.reloadMu.Unlock()
	rt, _ := routes.Load() // best-effort; a broken routes.yml must not brick reloads
	if rt == nil {
		rt = routes.EmptyTable()
	}
	mounts, err := warren.ActiveMounts(e.MarmotDir)
	if err != nil {
		// Fail-open like startup: a broken workspace _warren.md must not
		// brick local queries; warrenRuntimeBridges warns about the missing
		// bridge enforcement below.
		err = fmt.Errorf("load warren mounts: %w", err)
	} else {
		for _, mount := range mounts {
			if mount.VaultID == "" || !mount.Available {
				continue
			}
			if mount.SelfAlias {
				// Self-alias: the live local vault answers for this vault ID.
				// A route to the warren copy would shadow it with a stale
				// snapshot (the pre-R1 bug); routes.yml may legitimately map
				// this ID to our own live path (that is how OTHER workspaces
				// resolve us) — leave whatever is there alone. Keyed on the
				// fresh SelfAlias (derived from _config.md by ActiveMounts),
				// not e.LocalVaultID, so a daemon whose cached ID went stale
				// still skips correctly.
				continue
			}
			if prev, ok := rt.Get(mount.VaultID); ok && prev != mount.Path {
				fmt.Fprintf(os.Stderr, "warning: vault ID %q claimed by both %s and %s; using %s\n", mount.VaultID, prev, mount.Path, mount.Path)
			}
			rt.Set(mount.VaultID, mount.Path)
		}
		if len(mounts) > 0 {
			fmt.Fprintf(os.Stderr, "warren: %d active project mounts loaded\n", len(mounts))
		}
	}
	bridges, declared := warrenRuntimeBridges(e.MarmotDir, mounts)
	e.setWarrenBridges(bridges, declared)
	e.VaultRegistry.Rebuild(e.crossVaultBridges(), rt)
	return err
}

// setWarrenBridges replaces the engine's warren runtime bridges and
// recomposes NSManager.CrossVaultBridges as fileBridges ++ warrenBridges,
// so repeated reloads never duplicate (buildEngine used to append once at
// startup). When bridges appear on a previously namespace-less engine an
// empty manager is created so cross-vault edge validation engages.
func (e *Engine) setWarrenBridges(bridges []*namespace.Bridge, declared bool) {
	e.nsMgrMu.Lock()
	defer e.nsMgrMu.Unlock()
	e.warrenBridges = bridges
	if e.NSManager == nil {
		if len(bridges) == 0 && !declared {
			return
		}
		e.NSManager = emptyNamespaceManager(e.MarmotDir)
		e.fileCrossVaultBridges = nil
	}
	merged := append([]*namespace.Bridge(nil), e.fileCrossVaultBridges...)
	merged = append(merged, bridges...)
	e.NSManager.CrossVaultBridges = merged
}

// crossVaultBridges returns a snapshot of the manager's cross-vault bridges
// (file-declared plus warren runtime) for seeding the vault registry.
func (e *Engine) crossVaultBridges() []*namespace.Bridge {
	e.nsMgrMu.RLock()
	defer e.nsMgrMu.RUnlock()
	if e.NSManager == nil {
		return nil
	}
	return append([]*namespace.Bridge(nil), e.NSManager.CrossVaultBridges...)
}

func emptyNamespaceManager(dir string) *namespace.Manager {
	return &namespace.Manager{
		VaultDir:   dir,
		Namespaces: make(map[string]*namespace.Namespace),
		Bridges:    make(map[string]*namespace.Bridge),
	}
}

// warrenRuntimeBridges derives cross-vault bridges from warren manifests for
// the currently active mounts. The bool reports whether any warren declared
// bridges at all (even if none are active), which forces bridge policy
// enforcement on.
func warrenRuntimeBridges(marmotDir string, mounts []warren.ProjectStatus) ([]*namespace.Bridge, bool) {
	state, _, err := warren.LoadWorkspaceStateFromMarmot(marmotDir)
	if err != nil {
		// Fail-open is today's semantic (a broken warren must not brick local
		// queries), but the missing enforcement must not be silent.
		fmt.Fprintf(os.Stderr, "warning: warren workspace state unreadable (%s): %v — cross-vault bridge policy NOT enforced\n", marmotDir, err)
		return nil, false
	}
	// Self-alias bridge endpoints resolve to the live workspace vault, never
	// the warren copy (which would serve a stale snapshot).
	absMarmotDir, err := filepath.Abs(marmotDir)
	if err != nil {
		absMarmotDir = marmotDir
	}

	active := make(map[string]map[string]warren.ProjectStatus)
	for _, mount := range mounts {
		if !mount.Active || !mount.Available || mount.VaultID == "" {
			continue
		}
		projects := active[mount.WarrenID]
		if projects == nil {
			projects = make(map[string]warren.ProjectStatus)
			active[mount.WarrenID] = projects
		}
		projects[mount.ProjectID] = mount
	}

	declared := false
	merged := make(map[string]*namespace.Bridge)
	relationSets := make(map[string]map[string]bool)
	for warrenID, entry := range state.Warrens {
		manifest, _, err := warren.LoadManifest(entry.Path)
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning: warren %q bridge manifest unreadable (%s): %v — cross-vault bridge policy NOT enforced for this warren\n", warrenID, entry.Path, err)
			continue
		}
		if len(manifest.Bridges) > 0 {
			declared = true
		}
		activeProjects := active[warrenID]
		if len(activeProjects) == 0 {
			continue
		}
		for _, bridge := range manifest.Bridges {
			source, sourceOK := activeProjects[bridge.Source]
			target, targetOK := activeProjects[bridge.Target]
			if !sourceOK || !targetOK || source.VaultID == "" || target.VaultID == "" {
				continue
			}
			if source.VaultID == target.VaultID {
				// Both endpoints resolve to one vault (e.g. both alias the
				// local vault): a self-bridge is meaningless — skip, don't
				// synthesize.
				continue
			}
			// A self-alias endpoint serves from the live workspace vault; the
			// vault IDs stay unchanged so cross-vault edge validation still
			// matches the bridge by ID.
			sourcePath, targetPath := source.Path, target.Path
			if source.SelfAlias {
				sourcePath = absMarmotDir
			}
			if target.SelfAlias {
				targetPath = absMarmotDir
			}
			key := runtimeBridgeKey(source.VaultID, target.VaultID)
			runtimeBridge, ok := merged[key]
			if !ok {
				runtimeBridge = &namespace.Bridge{
					Source:          bridge.Source,
					Target:          bridge.Target,
					SourceVaultID:   source.VaultID,
					TargetVaultID:   target.VaultID,
					SourceVaultPath: sourcePath,
					TargetVaultPath: targetPath,
				}
				merged[key] = runtimeBridge
				relationSets[key] = make(map[string]bool)
			}
			for _, relation := range bridge.Relations {
				if relation == "" || relationSets[key][relation] {
					continue
				}
				relationSets[key][relation] = true
				runtimeBridge.AllowedRelations = append(runtimeBridge.AllowedRelations, relation)
			}
		}
	}

	bridges := make([]*namespace.Bridge, 0, len(merged))
	for _, bridge := range merged {
		bridges = append(bridges, bridge)
	}
	return bridges, declared
}

func runtimeBridgeKey(a, b string) string {
	if a > b {
		a, b = b, a
	}
	return a + "\x00" + b
}
