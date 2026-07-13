package warren

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// BurrowProvenance pins what a burrow cache was materialized from. It is
// written by Materialize after the atomic cache swap succeeds, as a sibling
// of the cache (<...>/warrens/<warrenID>/projects/<projectID>/provenance.md),
// so `warren refresh --pull` can re-materialize only stale caches and
// `warren status` can say how old a cache is. A cache without provenance
// (crash between swap and write, pre-provenance binary) is simply treated as
// stale and re-copied on the next refresh — no torn state is possible.
type BurrowProvenance struct {
	// SourceCommit is the warren checkout's HEAD at materialize time; empty
	// when the warren is not a git repository (staleness then degrades to
	// "unknown age; re-materialize on refresh").
	SourceCommit string `yaml:"source_commit" json:"source_commit"`
	// SourcePath is the project's path from the warren manifest.
	SourcePath string `yaml:"source_path" json:"source_path"`
	// MaterializedAt is the RFC3339 UTC time the cache was written.
	MaterializedAt string `yaml:"materialized_at" json:"materialized_at"`
	// ManifestVersion records the manifest schema the writing binary spoke.
	ManifestVersion int `yaml:"manifest_version" json:"manifest_version"`
}

// burrowProvenancePath returns the provenance file next to (not inside) the
// cache's .marmot dir, so DropMaterialized's projects/<p>/ removal deletes
// both together and the copier never sees it as vault content.
func burrowProvenancePath(workspaceMarmotDir, warrenID, projectID string) string {
	return filepath.Join(filepath.Dir(materializedProjectPath(workspaceMarmotDir, warrenID, projectID)), "provenance.md")
}

// LoadBurrowProvenance reads a burrow cache's provenance record. A missing
// file returns os.ErrNotExist (callers treat any error as "stale").
func LoadBurrowProvenance(workspaceMarmotDir, warrenID, projectID string) (*BurrowProvenance, error) {
	data, err := os.ReadFile(burrowProvenancePath(workspaceMarmotDir, warrenID, projectID))
	if err != nil {
		return nil, fmt.Errorf("read burrow provenance: %w", err)
	}
	var p BurrowProvenance
	if _, err := parseMarkdownYAML(data, &p); err != nil {
		return nil, fmt.Errorf("parse burrow provenance: %w", err)
	}
	return &p, nil
}

// SaveBurrowProvenance writes a burrow cache's provenance record atomically.
func SaveBurrowProvenance(workspaceMarmotDir, warrenID, projectID string, p *BurrowProvenance) error {
	if p == nil {
		p = &BurrowProvenance{}
	}
	return writeMarkdownYAML(burrowProvenancePath(workspaceMarmotDir, warrenID, projectID), p, "")
}

// newBurrowProvenance stamps a provenance record for a just-swapped cache.
func newBurrowProvenance(project Project, sourceCommit string) *BurrowProvenance {
	return &BurrowProvenance{
		SourceCommit:    sourceCommit,
		SourcePath:      project.Path,
		MaterializedAt:  time.Now().UTC().Format(time.RFC3339),
		ManifestVersion: CurrentManifestVersion,
	}
}
