package mcp

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/nurozen/context-marmot/internal/den"
	"github.com/nurozen/context-marmot/internal/warren"
)

// topologySnapshot is the data rendered into the MCP initialize
// `instructions` field. It is collected ONCE at server construction
// (decided OQ11: snapshot-at-init, no live refresh — freshness that
// matters, like pending edit counts, also flows through tool results).
type topologySnapshot struct {
	// VaultID is the local identity vault id ("" = unidentified vault).
	VaultID string
	// NamespaceCount is the number of known namespaces (minimum 1: the
	// implicit "default" namespace in single-namespace mode).
	NamespaceCount int
	// Den is non-nil when the served directory is a den identity vault
	// (…/dens/<id>/vault with _den.md one level up) or a links-only den
	// root (_den.md in the served dir itself).
	Den *denTopology
	// Mounts are the active warren mounts visible from the served vault.
	Mounts []mountTopology
}

type denTopology struct {
	ID       string
	Lifetime string
	Links    []denLinkStatus
}

// denLinkStatus is a den link plus its resolution state from LoadDenLinks:
// Resolved links name the federated vault (@vault-id); unresolved links are
// declared in _den.md but not queryable (missing checkout, unknown target,
// or link resolution never ran on this engine).
type denLinkStatus struct {
	den.Link
	ResolvedVaultID string
	Resolved        bool
	// Freshness is the cheap exec-free annotation from den.LinkFreshnessNote
	// (§9): pinned@<commit>[, stale] for pinned links, unreachable for dead
	// live targets, "" when there is nothing to say. Best-effort — the full
	// git-backed numbers live in `marmot den status`.
	Freshness string
}

type mountTopology struct {
	VaultID   string // qualified id agents use (@vault-id/…)
	ProjectID string
	Editable  bool
	SelfAlias bool
}

// collectTopology gathers the topology snapshot from the engine and the
// filesystem around its MarmotDir. Every probe is best-effort: a missing or
// unreadable den manifest / warren state simply omits that section.
func collectTopology(e *Engine) topologySnapshot {
	snap := topologySnapshot{NamespaceCount: 1}
	if e == nil {
		return snap
	}
	snap.VaultID = e.LocalVaultID
	if n := len(e.NamespaceNames()); n > 0 {
		snap.NamespaceCount = n
	}

	// Den detection: a den identity vault is served from …/dens/<id>/vault
	// with _den.md beside the vault dir; a links-only den is served from the
	// den root itself with _den.md inside it.
	if e.MarmotDir != "" {
		for _, manifestPath := range []string{
			filepath.Join(filepath.Dir(e.MarmotDir), den.ManifestFileName),
			filepath.Join(e.MarmotDir, den.ManifestFileName),
		} {
			m, _, err := den.LoadManifestAt(manifestPath)
			if err != nil || m.DenID == "" {
				continue
			}
			links := make([]denLinkStatus, 0, len(m.Links))
			for _, l := range m.Links {
				ls := denLinkStatus{Link: l, Freshness: den.LinkFreshnessNote(l)}
				if vid, ok := e.DenLinkResolvedVaultID(l); ok {
					ls.Resolved, ls.ResolvedVaultID = true, vid
				}
				links = append(links, ls)
			}
			snap.Den = &denTopology{ID: m.DenID, Lifetime: m.Lifetime, Links: links}
			break
		}

		if mounts, err := warren.ActiveMounts(e.MarmotDir); err == nil {
			for _, m := range mounts {
				snap.Mounts = append(snap.Mounts, mountTopology{
					VaultID:   m.VaultID,
					ProjectID: m.ProjectID,
					Editable:  m.Editable,
					SelfAlias: m.SelfAlias,
				})
			}
			sort.Slice(snap.Mounts, func(i, j int) bool {
				return snap.Mounts[i].VaultID < snap.Mounts[j].VaultID
			})
		}
	}
	return snap
}

// renderInstructions renders the topology as compact plain text for the
// initialize `instructions` field.
func renderInstructions(snap topologySnapshot) string {
	var b strings.Builder
	b.WriteString("ContextMarmot knowledge-graph server. Topology snapshot at connect (may go stale during long sessions; tool results carry fresh state).\n")

	if snap.VaultID != "" {
		fmt.Fprintf(&b, "Vault: %s (%d namespace", snap.VaultID, snap.NamespaceCount)
		if snap.NamespaceCount != 1 {
			b.WriteString("s")
		}
		b.WriteString(")\n")
	} else {
		b.WriteString("Vault: unidentified (no vault_id configured)\n")
	}

	if snap.Den != nil {
		fmt.Fprintf(&b, "Den: %s (lifetime: %s)\n", snap.Den.ID, snap.Den.Lifetime)
		for _, l := range snap.Den.Links {
			note := ""
			if l.Freshness != "" {
				note = " [" + l.Freshness + "]"
			}
			if l.Resolved {
				fmt.Fprintf(&b, "  link: %s mode=%s (resolved: @%s)%s\n", l.Target, l.Mode, l.ResolvedVaultID, note)
			} else {
				fmt.Fprintf(&b, "  link: %s mode=%s (unresolved)%s\n", l.Target, l.Mode, note)
			}
		}
	}

	if len(snap.Mounts) > 0 {
		b.WriteString("Warren mounts:\n")
		for _, m := range snap.Mounts {
			access := "read-only"
			switch {
			case m.SelfAlias:
				access = "self — this workspace's live vault"
			case m.Editable:
				access = "editable"
			}
			fmt.Fprintf(&b, "  @%s (%s)\n", m.VaultID, access)
		}
	}

	b.WriteString("Write policy: own vault = full CRUD; @vault-id on editable links/mounts = updates to existing nodes only (summary/context/tags); read-only links reject writes. Cross-vault results use qualified IDs (@vault-id/node-id).")
	return b.String()
}

// buildInstructions is the server-construction entry point.
func buildInstructions(e *Engine) string {
	return renderInstructions(collectTopology(e))
}
