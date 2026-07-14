// Package routes manages the global vault routing table at ~/.marmot/routes.yml.
// It maps vault IDs to filesystem paths, enabling cross-vault resolution
// independent of bridge manifest paths. It also maps project paths to
// den-or-vault ids (projects: reverse table).
//
// The table location can be overridden with the MARMOT_ROUTES environment
// variable: a path value redirects the table, while "off", "none", or "0"
// disables the global table entirely (useful for hermetic tests and scratch
// vaults that must not inherit the user's registered vaults).
//
// When MARMOT_ROUTES is unset, the default path is $MARMOT_HOME/routes.yml
// (default MARMOT_HOME = ~/.marmot).
package routes

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/nurozen/context-marmot/internal/flock"
	"github.com/nurozen/context-marmot/internal/home"
	"gopkg.in/yaml.v3"
)

// VaultEntry holds the filesystem location for a registered vault.
type VaultEntry struct {
	Path string `yaml:"path"`
}

// RoutingTable maps vault IDs to their filesystem locations and project
// paths to den-or-vault ids. All exported methods are safe for concurrent use.
type RoutingTable struct {
	mu       sync.RWMutex
	Vaults   map[string]VaultEntry `yaml:"vaults"`
	Projects map[string]string     `yaml:"projects"` // abs project path → den-or-vault id
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
// Precedence: SetOverridePath > MARMOT_ROUTES env > $MARMOT_HOME/routes.yml.
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
		// fall through to the default location under MARMOT_HOME
	case "off", "none", "0":
		return ""
	default:
		return env
	}
	return home.RoutesPath()
}

// DefaultPath returns $MARMOT_HOME/routes.yml (or the override if set).
func DefaultPath() string {
	mu.RLock()
	defer mu.RUnlock()
	return defaultPathLocked()
}

// normalize ensures Vaults and Projects maps are non-nil.
// Called after every unmarshal path so new maps cannot be forgotten.
func (rt *RoutingTable) normalize() {
	if rt.Vaults == nil {
		rt.Vaults = make(map[string]VaultEntry)
	}
	if rt.Projects == nil {
		rt.Projects = make(map[string]string)
	}
}

// EmptyTable returns a fresh routing table with no vaults or projects registered.
func EmptyTable() *RoutingTable {
	return &RoutingTable{
		Vaults:   make(map[string]VaultEntry),
		Projects: make(map[string]string),
	}
}

// Load reads the routing table from $MARMOT_HOME/routes.yml.
// Returns an empty table if the file does not exist.
func Load() (*RoutingTable, error) {
	return LoadFrom(DefaultPath())
}

// LoadFrom reads the routing table from an explicit path.
func LoadFrom(path string) (*RoutingTable, error) {
	mu.RLock()
	defer mu.RUnlock()

	rt := EmptyTable()
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
	rt.normalize()
	return rt, nil
}

// Save writes the routing table to $MARMOT_HOME/routes.yml atomically.
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

	if rt != nil {
		rt.normalize()
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
		rt := EmptyTable()
		data, err := os.ReadFile(path)
		if err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("read routing table: %w", err)
		}
		if err == nil {
			if err := yaml.Unmarshal(data, rt); err != nil {
				return fmt.Errorf("parse routing table: %w", err)
			}
			rt.normalize()
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

// NormalizeProjectKey canonicalizes a project path for routes write AND lookup.
// Clean + Abs + EvalSymlinks (when possible). Falls back to Clean+Abs if
// EvalSymlinks fails (e.g. path does not exist yet).
func NormalizeProjectKey(path string) (string, error) {
	if path == "" {
		return "", fmt.Errorf("empty project path")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve project path: %w", err)
	}
	abs = filepath.Clean(abs)
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		return resolved, nil
	}
	// Path may not exist yet (den create before mkdir of project); still store Clean abs.
	return abs, nil
}

// GetProject returns the den-or-vault id for a project path, or ("", false).
// The path is normalized via NormalizeProjectKey before lookup.
func (rt *RoutingTable) GetProject(projectPath string) (string, bool) {
	if rt == nil {
		return "", false
	}
	key, err := NormalizeProjectKey(projectPath)
	if err != nil {
		return "", false
	}
	rt.mu.RLock()
	defer rt.mu.RUnlock()
	if rt.Projects == nil {
		return "", false
	}
	id, ok := rt.Projects[key]
	return id, ok
}

// SetProject registers or updates a project path → den-or-vault id mapping.
// The path is normalized via NormalizeProjectKey.
func (rt *RoutingTable) SetProject(projectPath, denOrVaultID string) {
	key, err := NormalizeProjectKey(projectPath)
	if err != nil {
		// Best-effort: store cleaned input so callers that already Abs'd still work.
		key = filepath.Clean(projectPath)
	}
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if rt.Projects == nil {
		rt.Projects = make(map[string]string)
	}
	rt.Projects[key] = denOrVaultID
}

// RemoveProject deletes a project path entry. Returns true if it existed.
func (rt *RoutingTable) RemoveProject(projectPath string) bool {
	key, err := NormalizeProjectKey(projectPath)
	if err != nil {
		key = filepath.Clean(projectPath)
	}
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if rt.Projects == nil {
		return false
	}
	_, existed := rt.Projects[key]
	delete(rt.Projects, key)
	return existed
}

// ListProjects returns a copy of all project_path -> den-or-vault-id mappings.
func (rt *RoutingTable) ListProjects() map[string]string {
	rt.mu.RLock()
	defer rt.mu.RUnlock()
	result := make(map[string]string, len(rt.Projects))
	for p, id := range rt.Projects {
		result[p] = id
	}
	return result
}
