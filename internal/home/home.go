// Package home resolves the single MARMOT_HOME root used for dens, routes,
// warren-cache, and other machine-local marmot state.
//
// Precedence: SetOverride > MARMOT_HOME env > ~/.marmot.
package home

import (
	"os"
	"path/filepath"
	"sync"
)

var (
	mu       sync.RWMutex
	override string
)

// SetOverride redirects Dir() away from MARMOT_HOME / ~/.marmot.
// Pass "" to clear. Intended for tests.
func SetOverride(path string) {
	mu.Lock()
	defer mu.Unlock()
	override = path
}

// Dir returns the absolute marmot home root.
// Empty string is only returned when no home can be resolved (rare).
// Relative MARMOT_HOME / override values are resolved to absolute paths so
// create and serve agree regardless of process cwd.
func Dir() string {
	mu.RLock()
	o := override
	mu.RUnlock()
	raw := o
	if raw == "" {
		if env := os.Getenv("MARMOT_HOME"); env != "" {
			raw = env
		}
	}
	if raw == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		raw = filepath.Join(home, ".marmot")
	}
	abs, err := filepath.Abs(raw)
	if err != nil {
		return raw
	}
	return abs
}

// DensDir returns $MARMOT_HOME/dens.
func DensDir() string {
	return filepath.Join(Dir(), "dens")
}

// RoutesPath returns $MARMOT_HOME/routes.yml (default routes location;
// still subject to MARMOT_ROUTES override in package routes).
func RoutesPath() string {
	return filepath.Join(Dir(), "routes.yml")
}

// WarrenCacheDir returns $MARMOT_HOME/warren-cache.
func WarrenCacheDir() string {
	return filepath.Join(Dir(), "warren-cache")
}
