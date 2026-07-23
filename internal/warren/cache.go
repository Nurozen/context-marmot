package warren

// Cache resolution helpers for the shared warren cache (§5.2/§5.3): a bare
// mirror per warren at $MARMOT_HOME/warren-cache/<id>.git plus one shared
// provenance-pinned detached read checkout at
// $MARMOT_HOME/warren-cache/checkouts/<id>.
//
// This file stays exec-free (path derivation, registry reads, and plain file
// I/O only) — all git operations against the cache live in internal/gitx and
// are driven from cmd/marmot. The pin sidecar
// $MARMOT_HOME/warren-cache/checkouts/<id>.pin records the full commit hash
// the shared checkout is pinned at (single line, trailing newline). It is
// written by `warren add`/`warren sync` and read by sync (previous_commit)
// and future den links; a missing or unreadable pin means "unknown", which
// consumers must treat as stale.

import (
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/nurozen/context-marmot/internal/home"
	"github.com/nurozen/context-marmot/internal/warrenreg"
)

// CacheBarePath returns the shared bare mirror path for a warren id,
// $MARMOT_HOME/warren-cache/<id>.git. Derived, never persisted (the cache
// root can move without a migration — the warrenreg posture).
func CacheBarePath(warrenID string) string {
	return filepath.Join(home.WarrenCacheDir(), warrenID+".git")
}

// CacheCheckoutPath returns the shared read checkout path for a warren id,
// $MARMOT_HOME/warren-cache/checkouts/<id>.
func CacheCheckoutPath(warrenID string) string {
	return filepath.Join(home.WarrenCacheDir(), "checkouts", warrenID)
}

// CachePinPath returns the pin sidecar recording the commit the shared
// checkout is pinned at ($MARMOT_HOME/warren-cache/checkouts/<id>.pin).
func CachePinPath(warrenID string) string {
	return CacheCheckoutPath(warrenID) + ".pin"
}

// ReadCachePin returns the pinned commit for a warren's shared checkout, or
// "" when the pin is missing or unreadable (unknown == stale, crash-safe by
// definition).
func ReadCachePin(warrenID string) string {
	data, err := os.ReadFile(CachePinPath(warrenID))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// WriteCachePin records the commit the shared checkout is pinned at.
func WriteCachePin(warrenID, commit string) error {
	path := CachePinPath(warrenID)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(commit+"\n"), 0o644)
}

// CacheWorkspaceWarren synthesizes a workspace warren entry for a
// cache-backed warren: the id is in the global registry and its shared read
// checkout exists on disk. Consumers (warren status/mount, den links) use the
// synthesized entry exactly like a user-registered checkout — Path points at
// the shared checkout, no workspace registration required.
func CacheWorkspaceWarren(warrenID string) (WorkspaceWarren, bool) {
	if ValidateWarrenID(warrenID) != nil {
		return WorkspaceWarren{}, false
	}
	reg, err := warrenreg.Load()
	if err != nil {
		return WorkspaceWarren{}, false
	}
	if _, ok := reg.Warrens[warrenID]; !ok {
		return WorkspaceWarren{}, false
	}
	checkout := CacheCheckoutPath(warrenID)
	if !dirExists(checkout) {
		return WorkspaceWarren{}, false
	}
	return WorkspaceWarren{Path: checkout}, true
}

// CacheWarrenIDs returns every warren id in the global registry, sorted.
// Registry read failures degrade to nil (advisory listing callers only).
func CacheWarrenIDs() []string {
	reg, err := warrenreg.Load()
	if err != nil {
		return nil
	}
	ids := make([]string, 0, len(reg.Warrens))
	for id := range reg.Warrens {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// CacheStatuses derives project statuses for a cache-backed warren that is
// NOT registered in the workspace state, by evaluating the synthesized entry
// through the same statusFromState path registered warrens use — so `warren
// status` against a cache-backed warren can never disagree structurally with
// a registered one (it just has no active/editable/materialized rows).
func CacheStatuses(wsMarmotDir, warrenID string, entry WorkspaceWarren) ([]ProjectStatus, error) {
	state := &WorkspaceState{Warrens: map[string]WorkspaceWarren{warrenID: entry}}
	return statusFromState(wsMarmotDir, warrenID, state)
}
