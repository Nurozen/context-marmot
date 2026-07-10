// Package routes manages the global vault routing table at ~/.marmot/routes.yml.
// It maps vault IDs to filesystem paths, enabling cross-vault resolution
// independent of bridge manifest paths.
//
// The table location can be overridden with the MARMOT_ROUTES environment
// variable: a path value redirects the table, while "off", "none", or "0"
// disables the global table entirely (useful for hermetic tests and scratch
// vaults that must not inherit the user's registered vaults).
package routes

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/nurozen/context-marmot/internal/flock"
	"gopkg.in/yaml.v3"
)

// VaultEntry holds the filesystem location for a registered vault.
type VaultEntry struct {
	Path string `yaml:"path"`
}

// RoutingTable maps vault IDs to their filesystem locations.
// All exported methods are safe for concurrent use.
type RoutingTable struct {
	mu     sync.RWMutex
	Vaults map[string]VaultEntry `yaml:"vaults"`
}

// mu protects Load/Save within a single process. Inter-process safety
// is handled by atomic writes (tmp + rename).
var mu sync.RWMutex

// overridePath allows tests to redirect Load/Save away from ~/.marmot.
var overridePath string

// SetOverridePath sets a custom path for Load/Save, overriding ~/.marmot/routes.yml.
// Pass "" to revert to the default. Intended for testing.
func SetOverridePath(path string) {
	mu.Lock()
	defer mu.Unlock()
	overridePath = path
}

// defaultPathLocked returns the routing table path (caller must hold mu).
// Precedence: SetOverridePath > MARMOT_ROUTES env > ~/.marmot/routes.yml.
// MARMOT_ROUTES=off|none|0 disables the global routing table entirely
// (Load returns an empty table), which keeps hermetic tooling and fresh
// scratch vaults from inheriting the user's global vault registry.
// Any other non-empty MARMOT_ROUTES value is used as the routes file path.
func defaultPathLocked() string {
	if overridePath != "" {
		return overridePath
	}
	switch env := os.Getenv("MARMOT_ROUTES"); env {
	case "":
		// fall through to the default location
	case "off", "none", "0":
		return ""
	default:
		return env
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".marmot", "routes.yml")
}

// DefaultPath returns ~/.marmot/routes.yml (or the override if set).
func DefaultPath() string {
	mu.RLock()
	defer mu.RUnlock()
	return defaultPathLocked()
}

// Load reads the routing table from ~/.marmot/routes.yml.
// Returns an empty table if the file does not exist.
func Load() (*RoutingTable, error) {
	return LoadFrom(DefaultPath())
}

// LoadFrom reads the routing table from an explicit path.
func LoadFrom(path string) (*RoutingTable, error) {
	mu.RLock()
	defer mu.RUnlock()

	rt := &RoutingTable{Vaults: make(map[string]VaultEntry)}
	if path == "" {
		return rt, nil
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return rt, nil
		}
		return nil, fmt.Errorf("read routing table: %w", err)
	}

	if err := yaml.Unmarshal(data, rt); err != nil {
		return nil, fmt.Errorf("parse routing table: %w", err)
	}
	if rt.Vaults == nil {
		rt.Vaults = make(map[string]VaultEntry)
	}
	return rt, nil
}

// Save writes the routing table to ~/.marmot/routes.yml atomically.
func Save(rt *RoutingTable) error {
	return SaveTo(rt, DefaultPath())
}

// SaveTo writes the routing table to an explicit path atomically.
func SaveTo(rt *RoutingTable, path string) error {
	mu.Lock()
	defer mu.Unlock()

	if path == "" {
		return fmt.Errorf("empty routing table path (routing disabled via MARMOT_ROUTES?)")
	}

	return writeTableAtomic(rt, path)
}

// writeTableAtomic marshals rt and writes it to path via a uniquely named
// temp file + rename, so concurrent writers never collide on a shared tmp
// name. Caller must hold mu.
func writeTableAtomic(rt *RoutingTable, path string) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create routing table dir: %w", err)
	}

	data, err := yaml.Marshal(rt)
	if err != nil {
		return fmt.Errorf("marshal routing table: %w", err)
	}

	tmp, err := os.CreateTemp(dir, ".routes-*.yml.tmp")
	if err != nil {
		return fmt.Errorf("create routing table tmp: %w", err)
	}
	tmpPath := tmp.Name()
	success := false
	defer func() {
		if !success {
			_ = tmp.Close()
			_ = os.Remove(tmpPath)
		}
	}()
	if _, err := tmp.Write(data); err != nil {
		return fmt.Errorf("write routing table tmp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close routing table tmp: %w", err)
	}
	if err := os.Chmod(tmpPath, 0o644); err != nil {
		return fmt.Errorf("chmod routing table tmp: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("commit routing table: %w", err)
	}
	success = true
	return nil
}

// Update performs an atomic read-modify-write cycle on the routing table.
// The provided function receives the current table and may modify it.
// If fn returns nil, the modified table is saved atomically.
//
// The RMW cycle runs under an exclusive cross-process flock (sibling
// routes.yml.lock file) inside the package mu, so concurrent marmot
// processes cannot drop each other's route registrations. Lock ordering is
// fixed (process mu, then file flock) — no inversion is possible.
func Update(fn func(rt *RoutingTable) error) error {
	mu.Lock()
	defer mu.Unlock()

	path := defaultPathLocked()
	if path == "" {
		return fmt.Errorf("empty routing table path (routing disabled via MARMOT_ROUTES?)")
	}

	return flock.WithLock(path+".lock", func() error {
		// Read under lock (bypass LoadFrom which also takes mu)
		rt := &RoutingTable{Vaults: make(map[string]VaultEntry)}
		data, err := os.ReadFile(path)
		if err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("read routing table: %w", err)
		}
		if err == nil {
			if err := yaml.Unmarshal(data, rt); err != nil {
				return fmt.Errorf("parse routing table: %w", err)
			}
			if rt.Vaults == nil {
				rt.Vaults = make(map[string]VaultEntry)
			}
		}

		if err := fn(rt); err != nil {
			return err
		}

		return writeTableAtomic(rt, path)
	})
}

// Get returns the filesystem path for a vault ID, or ("", false) if not found.
func (rt *RoutingTable) Get(vaultID string) (string, bool) {
	if rt == nil {
		return "", false
	}
	rt.mu.RLock()
	defer rt.mu.RUnlock()
	if rt.Vaults == nil {
		return "", false
	}
	entry, ok := rt.Vaults[vaultID]
	if !ok {
		return "", false
	}
	return entry.Path, true
}

// Set registers or updates a vault path.
func (rt *RoutingTable) Set(vaultID, path string) {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if rt.Vaults == nil {
		rt.Vaults = make(map[string]VaultEntry)
	}
	rt.Vaults[vaultID] = VaultEntry{Path: path}
}

// Remove deletes a vault entry. Returns true if it existed.
func (rt *RoutingTable) Remove(vaultID string) bool {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if rt.Vaults == nil {
		return false
	}
	_, existed := rt.Vaults[vaultID]
	delete(rt.Vaults, vaultID)
	return existed
}

// List returns a copy of all vault_id -> path mappings.
func (rt *RoutingTable) List() map[string]string {
	rt.mu.RLock()
	defer rt.mu.RUnlock()
	result := make(map[string]string, len(rt.Vaults))
	for id, entry := range rt.Vaults {
		result[id] = entry.Path
	}
	return result
}
