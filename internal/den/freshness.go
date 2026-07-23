package den

import (
	"fmt"
	"os"

	"github.com/nurozen/context-marmot/internal/routes"
	"github.com/nurozen/context-marmot/internal/warren"
)

// Cheap, exec-free link freshness (plan §9). These helpers read only local
// files (routes.yml, warren cache pins, den manifests) — no git — so the MCP
// instructions builder and other in-process consumers can annotate den link
// lines best-effort. The CLI's `den status` layers real git ahead/behind
// numbers on top (git execution stays in cmd/marmot).

// LiveTargetReachable reports whether a live den link target answers: either
// the global routing table maps it to a vault dir, or it is a den id whose
// identity vault exists. Mirrors the resolution order resolveDenLink uses at
// serve time.
func LiveTargetReachable(target string) bool {
	if target == "" {
		return false
	}
	if rt, err := routes.Load(); err == nil && rt != nil {
		if _, ok := rt.Get(target); ok {
			return true
		}
	}
	if warren.LocalVaultID(VaultPath(target)) != "" {
		return true
	}
	return false
}

// PinnedLinkSourceCommit returns the source_commit recorded for a pinned
// link's warren project (manifest v3 provenance): the commit of the SOURCE
// repository the project vault was snapshotted from. Empty when the warren
// cache is unavailable, the project is unknown, or no provenance is recorded.
func PinnedLinkSourceCommit(l Link) string {
	if l.Mode != LinkModeLink || l.Warren == "" || l.Project == "" {
		return ""
	}
	checkout := warren.CacheCheckoutPath(l.Warren)
	if fi, err := os.Stat(checkout); err != nil || !fi.IsDir() {
		return ""
	}
	manifest, _, err := warren.LoadManifest(checkout)
	if err != nil {
		return ""
	}
	for _, p := range manifest.Projects {
		if p.ProjectID != l.Project && !containsString(p.Aliases, l.Project) {
			continue
		}
		return p.SourceCommit
	}
	return ""
}

// LinkFreshnessNote renders a best-effort freshness annotation for one den
// link, suitable for appending to a human link line ("" = nothing to say):
//
//   - pinned links: "pinned@<short>", plus "stale" when the recorded pin
//     differs from the warren's current cache pin (the shared checkout moved
//     on since this link was pinned);
//   - live links: "unreachable" when neither a route nor a den identity
//     vault answers for the target;
//   - edit links: "" (pending-edit counts need git; the CLI reports them).
func LinkFreshnessNote(l Link) string {
	switch l.Mode {
	case LinkModeLink:
		pin := l.PinnedRef
		if pin == "" {
			pin = warren.ReadCachePin(l.Warren)
		}
		if pin == "" {
			return ""
		}
		note := "pinned@" + shortCommit(pin)
		if cur := warren.ReadCachePin(l.Warren); cur != "" && cur != pin {
			note += ", stale"
		}
		return note
	case LinkModeLive:
		if !LiveTargetReachable(l.Target) {
			return "unreachable"
		}
		return ""
	default:
		return ""
	}
}

// shortCommit abbreviates a commit sha for human output.
func shortCommit(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}

// SkewNote renders the pinned-link source-commit skew disclosure (plan §9):
// a warren project vault is a knowledge snapshot taken at some source-repo
// commit; when provenance records it, status surfaces it verbatim so
// consumers can judge knowledge-vs-code skew. "" when unknown.
func SkewNote(l Link) string {
	sc := PinnedLinkSourceCommit(l)
	if sc == "" {
		return ""
	}
	return fmt.Sprintf("vault snapshot from source commit %s", shortCommit(sc))
}
