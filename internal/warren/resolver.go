package warren

// Reference resolution (§15.5): map a reference-repo spec (URL and/or local
// checkout path) onto known knowledge sources. Shared by `marmot resolve`
// (thin diagnostic verb) and den link/create `--ref` handling. Exec-free
// like the rest of the package: it only reads the global warren registry,
// cached checkout manifests, and checkout _config.md files.

import (
	"fmt"
	"os"
	"path/filepath"
)

// Resolution Via values (also the JSON `resolved_via` vocabulary pinned by
// testdata/contracts).
const (
	ResolvedViaWarrenURL     = "warren-url"
	ResolvedViaCheckoutVault = "checkout-vault"
	ResolvedViaNone          = "none"
)

// RefSpec describes one reference repo (`--ref name=<n>,url=<u>,path=<p>,ref=<r>`).
type RefSpec struct {
	Name   string
	URL    string
	Path   string
	GitRef string
}

// Resolution is the outcome of resolving one RefSpec.
type Resolution struct {
	// Via is one of the ResolvedVia* constants.
	Via string
	// WarrenID/ProjectID identify the matched cache-backed warren project
	// (Via == warren-url). WarrenID is the registry cache id — the id links
	// and mounts address the warren by.
	WarrenID  string
	ProjectID string
	// VaultID is the matched knowledge vault id: the warren project's
	// vault_id for warren-url matches (best-effort, may be empty when the
	// checkout metadata is unreadable), or the in-checkout vault's for
	// checkout-vault matches.
	VaultID string
	// Detail is a human-readable explanation of how (or why not) the spec
	// resolved.
	Detail string
}

// ResolveReference resolves ref in plan §15.5 order:
//
//	(a) canonical-URL match of ref.URL against every registered (cache-backed)
//	    warren's project source_url values (manifest v3) → warren-url;
//	(b) else an in-checkout vault at <ref.Path>/.marmot/_config.md with a
//	    vault_id → checkout-vault (knowledge snapshot = code snapshot);
//	(c) else none.
//
// v1 scope: leg (a) iterates the global registry (cache-backed warrens
// only); legacy workspace-registered warrens are not consulted.
func ResolveReference(ref RefSpec) Resolution {
	if canon := CanonicalRepoURL(ref.URL); canon != "" {
		for _, warrenID := range CacheWarrenIDs() {
			checkout := CacheCheckoutPath(warrenID)
			manifest, _, err := LoadManifest(checkout)
			if err != nil {
				continue // unmaterialized or broken cache entry: not a match
			}
			for _, project := range manifest.Projects {
				if project.SourceURL == "" || CanonicalRepoURL(project.SourceURL) != canon {
					continue
				}
				res := Resolution{
					Via:       ResolvedViaWarrenURL,
					WarrenID:  warrenID,
					ProjectID: project.ProjectID,
					Detail:    fmt.Sprintf("source_url %s matches warren %q project %q", canon, warrenID, project.ProjectID),
				}
				if meta, _, metaErr := LoadProjectMetadata(filepath.Join(checkout, filepath.FromSlash(project.Path))); metaErr == nil {
					res.VaultID = meta.VaultID
				}
				if project.SourceCommit != "" {
					res.Detail += fmt.Sprintf(" (vault snapshot from source commit %s)", project.SourceCommit)
				}
				return res
			}
		}
	}
	if ref.Path != "" {
		marmotDir := checkoutMarmotDir(ref.Path)
		if vaultID := LocalVaultID(marmotDir); vaultID != "" {
			return Resolution{
				Via:     ResolvedViaCheckoutVault,
				VaultID: vaultID,
				Detail:  fmt.Sprintf("in-checkout vault %q at %s", vaultID, marmotDir),
			}
		}
	}
	return Resolution{Via: ResolvedViaNone, Detail: noneDetail(ref)}
}

// checkoutMarmotDir maps a reference checkout path to its vault dir:
// <path>/.marmot by convention, or path itself when it already is a .marmot
// dir (holds _config.md).
func checkoutMarmotDir(path string) string {
	conventional := filepath.Join(path, MarmotDirName)
	if _, err := os.Stat(filepath.Join(conventional, "_config.md")); err == nil {
		return conventional
	}
	if _, err := os.Stat(filepath.Join(path, "_config.md")); err == nil {
		return path
	}
	return conventional
}

func noneDetail(ref RefSpec) string {
	switch {
	case ref.URL == "" && ref.Path == "":
		return "reference has neither url nor path to resolve by"
	case ref.URL != "" && ref.Path != "":
		return fmt.Sprintf("no registered warren project has source_url %s and no vault at %s", CanonicalRepoURL(ref.URL), filepath.Join(ref.Path, MarmotDirName))
	case ref.URL != "":
		return fmt.Sprintf("no registered warren project has source_url %s", CanonicalRepoURL(ref.URL))
	default:
		return fmt.Sprintf("no vault at %s", filepath.Join(ref.Path, MarmotDirName))
	}
}
