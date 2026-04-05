// Package routes manages the global vault routing table at ~/.marmot/routes.yml.
// It maps vault IDs to filesystem paths, enabling cross-vault resolution
// independent of bridge manifest paths.
package routes

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"gopkg.in/yaml.v3"
)

// VaultEntry holds the filesystem location for a registered vault.
type VaultEntry struct {
	Path string `yaml:"path"`
}

// RoutingTable maps vault IDs to their filesystem locations.
type RoutingTable struct {
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

// DefaultPath returns ~/.marmot/routes.yml (or the override if set).
func DefaultPath() string {
	if overridePath != "" {
		return overridePath
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".marmot", "routes.yml")
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
		return fmt.Errorf("empty routing table path")
	}

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create routing table dir: %w", err)
	}

	data, err := yaml.Marshal(rt)
	if err != nil {
		return fmt.Errorf("marshal routing table: %w", err)
	}

	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("write routing table tmp: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("commit routing table: %w", err)
	}
	return nil
}

// Get returns the filesystem path for a vault ID, or ("", false) if not found.
func (rt *RoutingTable) Get(vaultID string) (string, bool) {
	if rt == nil || rt.Vaults == nil {
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
	if rt.Vaults == nil {
		rt.Vaults = make(map[string]VaultEntry)
	}
	rt.Vaults[vaultID] = VaultEntry{Path: path}
}

// Remove deletes a vault entry. Returns true if it existed.
func (rt *RoutingTable) Remove(vaultID string) bool {
	if rt.Vaults == nil {
		return false
	}
	_, existed := rt.Vaults[vaultID]
	delete(rt.Vaults, vaultID)
	return existed
}

// List returns a copy of all vault_id → path mappings.
func (rt *RoutingTable) List() map[string]string {
	result := make(map[string]string, len(rt.Vaults))
	for id, entry := range rt.Vaults {
		result[id] = entry.Path
	}
	return result
}
