package den

import (
	"fmt"
	"path/filepath"

	"github.com/nurozen/context-marmot/internal/warren"
)

// Link modes understood by this binary. Only "edit" is wired end-to-end in
// the minimal `den link` slice; "link" and "live" stay reserved (P4).
const (
	LinkModeEdit = "edit"
	LinkModeLink = "link"
	LinkModeLive = "live"
)

var linkModes = map[string]bool{
	LinkModeEdit: true,
	LinkModeLink: true,
	LinkModeLive: true,
}

// ValidateLink checks a den link entry before it is persisted: the mode must
// be one of edit|link|live, the target must be non-empty, and warren-backed
// links (any of Warren/Project set, or mode=edit which is always
// warren-backed) must carry both a warren and a project id.
func ValidateLink(l Link) error {
	if !linkModes[l.Mode] {
		return fmt.Errorf("link mode must be one of edit|link|live, got %q", l.Mode)
	}
	if l.Target == "" {
		return fmt.Errorf("link target must not be empty")
	}
	if l.Mode == LinkModeEdit || l.Warren != "" || l.Project != "" {
		if l.Warren == "" || l.Project == "" {
			return fmt.Errorf("warren link needs both warren and project ids, got warren=%q project=%q", l.Warren, l.Project)
		}
	}
	return nil
}

// AddLink validates l and appends it to the den manifest's links, deduping on
// (Warren, Project, Mode) — and on Target for non-warren links. Returns
// added=false (no error, no write) when an equivalent link already exists.
// The whole read-modify-write runs under the manifest flock (UpdateManifest),
// so concurrent AddLink/RemoveLinks calls cannot drop each other's entries.
func AddLink(denID string, l Link) (added bool, err error) {
	if err := ValidateLink(l); err != nil {
		return false, err
	}
	err = UpdateManifest(denID, func(m *Manifest) (bool, error) {
		for _, existing := range m.Links {
			if existing.Mode != l.Mode || existing.Warren != l.Warren || existing.Project != l.Project {
				continue
			}
			if l.Warren == "" && existing.Target != l.Target {
				continue
			}
			return false, nil // equivalent link exists: no write, added stays false
		}
		m.Links = append(m.Links, l)
		added = true
		return true, nil
	})
	if err != nil {
		return false, fmt.Errorf("update den %q manifest: %w", denID, err)
	}
	return added, nil
}

// EnsureEditableMount marks warrenID/projectID as an active, editable mount
// in the warren workspace state stored directly in marmotDir (_warren.md),
// under the same cross-process flock convention updateWorkspaceState uses.
//
// This mirrors what warren.Mount + warren.SetEditable record, but those
// operate on a workspace root and resolve state at
// <workspaceRoot>/.marmot/_warren.md — they cannot address a den identity
// vault, whose state lives at <dens>/<id>/vault/_warren.md (the path
// warren.ActiveMounts reads for den serves). Caller is expected to have
// validated that the project exists in the warren manifest and is not
// author-side read-only.
//
// Idempotent: returns changed=false without writing when the project is
// already active and editable.
func EnsureEditableMount(marmotDir, warrenID, projectID string) (changed bool, err error) {
	return EnsureEditableMountAt(marmotDir, warrenID, projectID, "")
}

// EnsureEditableMountAt is EnsureEditableMount with an explicit warren
// checkout path. When warrenPath is non-empty the workspace-state entry is
// created if absent and its Path is (re)pointed at warrenPath — the
// cache-backed den link flow uses this to route the den's mounts of that
// warren through its dedicated edit worktree (per-den state makes the
// per-(warren,den) worktree granularity natural: every edit link this den
// holds into the warren reads and writes through the same worktree). With an
// empty warrenPath the entry must already exist and its Path is left alone
// (legacy registered-checkout behavior).
func EnsureEditableMountAt(marmotDir, warrenID, projectID, warrenPath string) (changed bool, err error) {
	if err := warren.ValidateWarrenID(warrenID); err != nil {
		return false, err
	}
	if err := warren.ValidateProjectID(projectID); err != nil {
		return false, err
	}
	statePath := filepath.Join(marmotDir, warren.ManifestFileName)
	err = warren.UpdateWorkspaceStateInMarmot(marmotDir, func(state *warren.WorkspaceState) (bool, error) {
		entry, ok := state.Warrens[warrenID]
		if !ok && warrenPath == "" {
			return false, fmt.Errorf("warren %q is not registered in %s", warrenID, statePath)
		}
		repointed := warrenPath != "" && entry.Path != warrenPath
		if repointed {
			entry.Path = warrenPath
		}
		active := containsString(entry.ActiveProjects, projectID)
		editable := containsString(entry.EditableProjects, projectID)
		if active && editable && !repointed {
			return false, nil
		}
		if !active {
			entry.ActiveProjects = append(entry.ActiveProjects, projectID)
		}
		if !editable {
			entry.EditableProjects = append(entry.EditableProjects, projectID)
		}
		state.Warrens[warrenID] = entry
		changed = true
		return true, nil
	})
	if err != nil {
		return false, err
	}
	return changed, nil
}

// MatchesRef reports whether a link answers to ref: its Target verbatim, or
// its <warren>/<project> pair (older edit links may carry only Target).
func (l Link) MatchesRef(ref string) bool {
	if ref == "" {
		return false
	}
	if l.Target == ref {
		return true
	}
	return l.Warren != "" && l.Project != "" && l.Warren+"/"+l.Project == ref
}

// RemoveLinks removes every manifest link matching ref (any mode) from the
// den and returns the removed entries. removed is empty (no error, no write)
// when nothing matched. The whole read-modify-write runs under the manifest
// flock (UpdateManifest) so a concurrent AddLink cannot be lost.
func RemoveLinks(denID, ref string) (removed []Link, err error) {
	err = UpdateManifest(denID, func(m *Manifest) (bool, error) {
		kept := m.Links[:0]
		for _, l := range m.Links {
			if l.MatchesRef(ref) {
				removed = append(removed, l)
				continue
			}
			kept = append(kept, l)
		}
		if len(removed) == 0 {
			return false, nil
		}
		m.Links = kept
		if len(m.Links) == 0 {
			m.Links = nil
		}
		return true, nil
	})
	if err != nil {
		return nil, fmt.Errorf("update den %q manifest: %w", denID, err)
	}
	return removed, nil
}

// RemoveMount drops warrenID/projectID from the active + editable project
// lists of the warren workspace state stored in marmotDir (the inverse of
// EnsureEditableMountAt). The warren entry itself stays registered — other
// links of this den may still mount it — and any edit worktree/branch is
// deliberately untouched (den destroy owns worktree cleanup). Idempotent:
// changed=false when the project was not mounted (including a missing state
// file or unregistered warren).
func RemoveMount(marmotDir, warrenID, projectID string) (changed bool, err error) {
	err = warren.UpdateWorkspaceStateInMarmot(marmotDir, func(state *warren.WorkspaceState) (bool, error) {
		entry, ok := state.Warrens[warrenID]
		if !ok {
			return false, nil
		}
		active := removeString(entry.ActiveProjects, projectID)
		editable := removeString(entry.EditableProjects, projectID)
		if len(active) == len(entry.ActiveProjects) && len(editable) == len(entry.EditableProjects) {
			return false, nil
		}
		entry.ActiveProjects = active
		entry.EditableProjects = editable
		state.Warrens[warrenID] = entry
		changed = true
		return true, nil
	})
	if err != nil {
		return false, err
	}
	return changed, nil
}

func removeString(list []string, s string) []string {
	out := list[:0]
	for _, item := range list {
		if item != s {
			out = append(out, item)
		}
	}
	return out
}

func containsString(list []string, s string) bool {
	for _, item := range list {
		if item == s {
			return true
		}
	}
	return false
}
