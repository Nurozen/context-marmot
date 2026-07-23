// Package warrenreg manages the global warren registry at
// $MARMOT_HOME/warrens.yml (decided OQ4: a separate file from routes.yml —
// different write cadence; routes stays a pure id/path lookup table).
//
// The registry maps warren IDs to their canonical clone URL and default
// branch. Bare-mirror paths are deliberately NOT persisted: derive them at
// read time from home.WarrenCacheDir() so the cache root can move without a
// migration.
//
// Concurrency mirrors internal/routes: an in-process mutex plus an exclusive
// cross-process flock on a sidecar warrens.yml.lock for read-modify-write,
// and atomic tmp+rename writes.
package warrenreg

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/nurozen/context-marmot/internal/flock"
	"github.com/nurozen/context-marmot/internal/home"
	"gopkg.in/yaml.v3"
)

// CurrentVersion is the registry schema write ceiling. Reads stay permissive
// (a newer file loads best-effort); writes refuse when the on-disk version is
// newer than this binary understands, because fixed-struct YAML parsing would
// silently strip unknown fields on a Load->Save round-trip (the
// warren.CurrentManifestVersion pattern).
const CurrentVersion = 1

// Entry holds the registration record for one warren.
type Entry struct {
	URL           string `yaml:"url"`
	DefaultBranch string `yaml:"default_branch,omitempty"`
}

// Registry is the on-disk schema of $MARMOT_HOME/warrens.yml.
type Registry struct {
	Version int              `yaml:"version"`
	Warrens map[string]Entry `yaml:"warrens"`
}

// mu protects Load/Update within a single process. Inter-process safety is
// handled by the flock sidecar in Update and atomic writes (tmp + rename).
var mu sync.Mutex

// Path returns the registry location, $MARMOT_HOME/warrens.yml.
func Path() string {
	return home.WarrensRegistryPath()
}

// normalize ensures the map is non-nil and a zero version is stamped current.
func (r *Registry) normalize() {
	if r.Version == 0 {
		r.Version = CurrentVersion
	}
	if r.Warrens == nil {
		r.Warrens = make(map[string]Entry)
	}
}

// Load reads the registry from $MARMOT_HOME/warrens.yml.
// A missing file yields an empty registry, not an error.
func Load() (*Registry, error) {
	mu.Lock()
	defer mu.Unlock()
	return loadLocked(Path())
}

// loadLocked reads and parses the registry file. Caller must hold mu (and the
// flock, when called from Update).
func loadLocked(path string) (*Registry, error) {
	reg := &Registry{}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			reg.normalize()
			return reg, nil
		}
		return nil, fmt.Errorf("read warren registry: %w", err)
	}
	if err := yaml.Unmarshal(data, reg); err != nil {
		return nil, fmt.Errorf("parse warren registry %s: %w", path, err)
	}
	reg.normalize()
	return reg, nil
}

// Update performs an atomic read-modify-write cycle on the registry. fn
// receives the current registry and may modify it; if fn returns nil the
// result is written atomically. The cycle runs under an exclusive
// cross-process flock (sidecar warrens.yml.lock) inside the package mu, so
// concurrent marmot processes cannot drop each other's registrations. Lock
// ordering is fixed (process mu, then file flock) — no inversion is possible.
//
// Update refuses to write a registry whose on-disk version exceeds
// CurrentVersion (this binary would silently drop fields it does not know).
func Update(fn func(reg *Registry) error) error {
	mu.Lock()
	defer mu.Unlock()

	path := Path()
	return flock.WithLock(path+".lock", func() error {
		reg, err := loadLocked(path)
		if err != nil {
			return err
		}
		if reg.Version > CurrentVersion {
			return fmt.Errorf("warren registry %s is version %d, newer than this marmot understands (%d); refusing to write — upgrade marmot", path, reg.Version, CurrentVersion)
		}
		if err := fn(reg); err != nil {
			return err
		}
		return writeAtomic(reg, path)
	})
}

// writeAtomic marshals reg and commits it to path via a uniquely named temp
// file + rename, so concurrent writers never collide on a shared tmp name.
// Caller must hold mu.
func writeAtomic(reg *Registry, path string) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create warren registry dir: %w", err)
	}

	reg.normalize()
	data, err := yaml.Marshal(reg)
	if err != nil {
		return fmt.Errorf("marshal warren registry: %w", err)
	}

	tmp, err := os.CreateTemp(dir, ".warrens-*.yml.tmp")
	if err != nil {
		return fmt.Errorf("create warren registry tmp: %w", err)
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
		return fmt.Errorf("write warren registry tmp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close warren registry tmp: %w", err)
	}
	if err := os.Chmod(tmpPath, 0o644); err != nil {
		return fmt.Errorf("chmod warren registry tmp: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("commit warren registry: %w", err)
	}
	success = true
	return nil
}
