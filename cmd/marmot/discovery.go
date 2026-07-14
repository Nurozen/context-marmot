package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/nurozen/context-marmot/internal/den"
	"github.com/nurozen/context-marmot/internal/routes"
)

// resolveVaultDir implements the dens-aware vault resolution chain:
//  1. explicit --dir (caller passes non-empty)
//  2. reverse route: cwd → routes.yml projects: → den/vault id → path
//  3. .marmot-vault pointer in cwd (and parents)
//  4. legacy walk-up for in-repo .marmot/ (discoverVault)
//
// Returns a filesystem path suitable for serve/ui/status. May return defaultDir
// when nothing is found (legacy behaviour).
func resolveVaultDir(explicitDir string) string {
	if explicitDir != "" {
		return explicitDir
	}
	if p := resolveFromCWDRoutes(); p != "" {
		return p
	}
	if p := resolveFromPointerWalk(); p != "" {
		return p
	}
	return discoverVault()
}

// resolveDenID maps a den id to its identity vault path (…/dens/<id>/vault),
// or the den root when no vault exists (links-only dens).
func resolveDenID(denID string) (string, error) {
	denID = strings.TrimSpace(denID)
	if denID == "" {
		return "", fmt.Errorf("empty den id")
	}
	if err := den.ValidateDenID(denID); err != nil {
		return "", err
	}
	denPath := den.Path(denID)
	if _, err := os.Stat(denPath); err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("den %q not found at %s", denID, denPath)
		}
		return "", err
	}
	vaultPath := filepath.Join(denPath, den.VaultDirName)
	if info, err := os.Stat(vaultPath); err == nil && info.IsDir() {
		return vaultPath, nil
	}
	// Links-only dens: serve from den root (no identity vault).
	return denPath, nil
}

func resolveFromCWDRoutes() string {
	cwd, err := os.Getwd()
	if err != nil {
		return ""
	}
	key, err := routes.NormalizeProjectKey(cwd)
	if err != nil {
		key = cwd
	}
	rt, err := routes.Load()
	if err != nil || rt == nil {
		return ""
	}
	id, ok := rt.GetProject(key)
	if !ok || id == "" {
		return ""
	}
	// Prefer den vault if this id is a den; else treat as vault id in routes vaults.
	if p, err := resolveDenID(id); err == nil {
		return p
	}
	if vaultPath, ok := rt.Get(id); ok && vaultPath != "" {
		return vaultPath
	}
	return ""
}

// projectRootForVaultDir returns a source project root when dir is a den
// identity vault ($MARMOT_HOME/dens/<id>/vault). Prefer the first existing
// registered project path from the den manifest.
func projectRootForVaultDir(vaultDir string) string {
	abs, err := filepath.Abs(vaultDir)
	if err != nil {
		abs = vaultDir
	}
	// Expect …/dens/<id>/vault
	base := filepath.Base(abs)
	if base != den.VaultDirName {
		return ""
	}
	denPath := filepath.Dir(abs)
	denID := filepath.Base(denPath)
	if filepath.Base(filepath.Dir(denPath)) != "dens" {
		return ""
	}
	if err := den.ValidateDenID(denID); err != nil {
		return ""
	}
	info, err := den.Status(denID)
	if err != nil {
		return ""
	}
	// Prefer an existing project path from the den manifest (kept in sync by
	// RelocateProject / set-project). Never return a missing path — that would
	// break MCP source resolution after a failed or partial move.
	for _, p := range info.Projects {
		if st, err := os.Stat(p); err == nil && st.IsDir() {
			return p
		}
	}
	// Manifest may lag: scan reverse routes for this den and pick an existing path.
	if rt, lerr := routes.Load(); lerr == nil && rt != nil {
		for path, id := range rt.ListProjects() {
			if id != denID {
				continue
			}
			if st, err := os.Stat(path); err == nil && st.IsDir() {
				return path
			}
		}
	}
	return ""
}

func resolveFromPointerWalk() string {
	dir, err := os.Getwd()
	if err != nil {
		return ""
	}
	for {
		id, err := den.ReadPointer(dir)
		if err == nil && id != "" {
			if p, err := resolveDenID(id); err == nil {
				return p
			}
			// Pointer may name a vault registered in routes.
			if rt, err := routes.Load(); err == nil && rt != nil {
				if vaultPath, ok := rt.Get(id); ok && vaultPath != "" {
					return vaultPath
				}
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}
