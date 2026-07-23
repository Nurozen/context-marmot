// Package warren manages git-backed Warren manifests and local workspace
// mount/edit state.
package warren

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"

	"github.com/nurozen/context-marmot/internal/embedding"
	"github.com/nurozen/context-marmot/internal/flock"
	"github.com/nurozen/context-marmot/internal/frontmatter"
	"github.com/nurozen/context-marmot/internal/node"
	"github.com/nurozen/context-marmot/internal/routes"
	"gopkg.in/yaml.v3"
)

const (
	ManifestFileName = "_warren.md"
	MarmotDirName    = ".marmot"
)

// CurrentManifestVersion is the newest warren manifest schema this binary
// fully understands. Version 2 added the per-project author-side `readonly`
// write policy. Version 3 added the per-project `source_url` /
// `source_commit` provenance fields captured at import (§15.4): they power
// reference-repo → warren-project resolution and honest skew reporting.
// Read paths stay permissive: LoadManifest warns on a newer version and
// keeps working best-effort. Write paths refuse (see checkManifestWritable):
// parsing goes through fixed structs, so a binary that does not know a field
// would silently strip it on a Load->Save round-trip — the version ceiling
// turns that silent data loss into a refusal. A manifest is only written as
// version 3 when some project actually carries source fields (see
// normalizeManifest), so source-free manifests keep round-tripping at their
// loaded version and stay editable by older binaries.
const CurrentManifestVersion = 3

// warnWriter receives degradation warnings (unreadable manifests/metadata).
// Package-level so tests can capture it; production always uses stderr.
var warnWriter io.Writer = os.Stderr

// Manifest describes a Warren repository.
type Manifest struct {
	WarrenID string    `yaml:"warren_id" json:"warren_id"`
	Version  int       `yaml:"version" json:"version"`
	Projects []Project `yaml:"projects,omitempty" json:"projects,omitempty"`
	Bridges  []Bridge  `yaml:"bridges,omitempty" json:"bridges,omitempty"`
}

// Project describes one project vault inside a Warren repository.
type Project struct {
	ProjectID string   `yaml:"project_id" json:"project_id"`
	Path      string   `yaml:"path" json:"path"`
	Aliases   []string `yaml:"aliases,omitempty" json:"aliases,omitempty"`
	// ReadOnly is the warren author's write policy: consumers cannot make
	// the project editable or write nodes back to it. Setting it bumps the
	// manifest to version 2 so pre-readonly binaries refuse to rewrite (and
	// silently strip) it. Manifest schema v2.
	ReadOnly bool `yaml:"readonly,omitempty" json:"readonly,omitempty"`
	// SourceURL is the canonical repo URL (see CanonicalRepoURL) of the
	// source checkout this project's vault was imported from. Captured at
	// `warren project import`; powers reference-repo → warren-project
	// resolution (§15.5) and source-skew reporting. Setting it bumps the
	// manifest to version 3. Manifest schema v3.
	SourceURL string `yaml:"source_url,omitempty" json:"source_url,omitempty"`
	// SourceCommit is the source checkout's HEAD commit at import time.
	// Manifest schema v3.
	SourceCommit string `yaml:"source_commit,omitempty" json:"source_commit,omitempty"`
}

// Bridge describes curated cross-project relations in a Warren.
type Bridge struct {
	Source    string   `yaml:"source" json:"source"`
	Target    string   `yaml:"target" json:"target"`
	Relations []string `yaml:"relations" json:"relations"`
}

// ProjectMetadata lives in projects/<id>/.marmot/_warren.md.
type ProjectMetadata struct {
	ProjectID string   `yaml:"project_id" json:"project_id"`
	WarrenID  string   `yaml:"warren_id" json:"warren_id"`
	VaultID   string   `yaml:"vault_id" json:"vault_id"`
	Aliases   []string `yaml:"aliases,omitempty" json:"aliases,omitempty"`
}

// WorkspaceState lives in a local workspace .marmot/_warren.md.
type WorkspaceState struct {
	Warrens map[string]WorkspaceWarren `yaml:"warrens,omitempty"`
}

// WorkspaceWarren records a local workspace's mounted/editable projects for a Warren.
type WorkspaceWarren struct {
	Path             string   `yaml:"path" json:"path"`
	ActiveProjects   []string `yaml:"active_projects,omitempty" json:"active_projects,omitempty"`
	EditableProjects []string `yaml:"editable_projects,omitempty" json:"editable_projects,omitempty"`
	Materialized     bool     `yaml:"materialized,omitempty" json:"materialized,omitempty"`
}

// ProjectStatus summarizes the local workspace state for a Warren project.
type ProjectStatus struct {
	WarrenID string `json:"warren_id"`
	// WarrenPath is the registered warren checkout root, carried so write
	// paths (WriteEditableNode) can re-read the manifest's author-side
	// write policy at write time. Empty on statuses built before the field
	// existed; the policy re-check is skipped then.
	WarrenPath   string `json:"warren_path,omitempty"`
	ProjectID    string `json:"project_id"`
	Path         string `json:"path"`
	VaultID      string `json:"vault_id,omitempty"`
	Registered   bool   `json:"registered"`
	Active       bool   `json:"active"`
	Editable     bool   `json:"editable"`
	Materialized bool   `json:"materialized"`
	Available    bool   `json:"available"`
	// SelfAlias reports that this project's vault_id matches the live local
	// vault's: the mount is served as an alias of the live vault — it claims
	// no route and is never editable or materialized. Derived fresh from
	// _config.md on every status build, so it can skew from an engine's
	// cached LocalVaultID until restart when vault_id is edited under a live
	// daemon (the same restart requirement every cached config field has).
	SelfAlias bool `json:"self_alias,omitempty"`
}

// Provenance describes where an API/search node came from.
type Provenance struct {
	Source      string `json:"source,omitempty"`
	WarrenID    string `json:"warren_id,omitempty"`
	ProjectID   string `json:"project_id,omitempty"`
	VaultID     string `json:"vault_id,omitempty"`
	MarmotDir   string `json:"marmot_dir,omitempty"`
	QualifiedID string `json:"qualified_id,omitempty"`
	Editable    bool   `json:"editable,omitempty"`
}

// DoctorReport describes consistency findings for a Warren manifest.
type DoctorReport struct {
	WarrenID string        `json:"warren_id"`
	Issues   []DoctorIssue `json:"issues,omitempty"`
}

// OK reports whether Doctor found no issues.
func (r DoctorReport) OK() bool {
	for _, issue := range r.Issues {
		if issue.Severity == "error" {
			return false
		}
	}
	return true
}

// DoctorIssue describes one Warren consistency problem.
type DoctorIssue struct {
	Severity  string `json:"severity"`
	Code      string `json:"code"`
	Message   string `json:"message"`
	ProjectID string `json:"project_id,omitempty"`
	Path      string `json:"path,omitempty"`
}

// ImportOptions controls how an existing local .marmot vault is copied into a Warren.
type ImportOptions struct {
	IncludeHeat bool
	NoObsidian  bool
	VaultID     string
	// SourceURL/SourceCommit record where the imported vault's source
	// checkout came from (manifest v3 provenance, §15.4). SourceURL is
	// canonicalized via CanonicalRepoURL before it is written; empty values
	// leave the fields unset and the manifest version untouched.
	SourceURL    string
	SourceCommit string
}

// Init creates or normalizes the root Warren manifest.
func Init(root, warrenID string) (*Manifest, error) {
	if warrenID == "" {
		warrenID = GenerateProjectID(filepath.Base(root))
	}
	if err := ValidateWarrenID(warrenID); err != nil {
		return nil, err
	}
	path, err := manifestPath(root)
	if err != nil {
		return nil, err
	}
	var manifest *Manifest
	err = flock.WithLock(path+".lock", func() error {
		m, body, err := LoadManifest(root)
		if err != nil {
			if !errors.Is(err, os.ErrNotExist) {
				return err
			}
			m, body = &Manifest{WarrenID: warrenID}, ""
		} else if m.WarrenID != warrenID {
			return fmt.Errorf("warren already initialized as %q", m.WarrenID)
		}
		if err := checkManifestWritable(m); err != nil {
			return err
		}
		if err := SaveManifest(root, m, body); err != nil {
			return err
		}
		manifest = m
		return nil
	})
	if err != nil {
		return nil, err
	}
	return manifest, nil
}

// AddProject registers a project in the Warren manifest and writes project metadata.
func AddProject(root string, project Project) (*Manifest, error) {
	project.ProjectID = strings.TrimSpace(project.ProjectID)
	if project.ProjectID == "" {
		project.ProjectID = generateProjectIDFromPath(project.Path)
	}
	if project.Path == "" {
		project.Path = defaultProjectPath(project.ProjectID)
	}
	project.Path = filepath.ToSlash(filepath.Clean(project.Path))
	project.Aliases = uniqueSorted(project.Aliases)
	if err := ValidateProjectID(project.ProjectID); err != nil {
		return nil, err
	}
	manifest, err := updateManifest(root, func(m *Manifest) error {
		for _, existing := range m.Projects {
			if existing.ProjectID == project.ProjectID {
				return fmt.Errorf("project %q already exists", project.ProjectID)
			}
		}
		if err := preflightProjectMetadata(root, m.WarrenID, project); err != nil {
			return err
		}
		m.Projects = append(m.Projects, project)
		return nil
	})
	if err != nil {
		return nil, err
	}
	if err := ensureProjectMetadata(root, manifest.WarrenID, project); err != nil {
		return nil, err
	}
	return manifest, nil
}

// ImportProject copies a local .marmot vault into the Warren and registers it.
//
// The whole load -> validate -> copy -> rename -> save sequence runs under
// the manifest flock: imports are rare, and correctness requires that the
// manifest snapshot the import validated against is still current when it is
// saved. This means the lock is held for the duration of the vault copy.
func ImportProject(root, sourceMarmotDir string, project Project, opts ImportOptions) (*Manifest, error) {
	path, err := manifestPath(root)
	if err != nil {
		return nil, err
	}
	var saved *Manifest
	err = flock.WithLock(path+".lock", func() error {
		m, err := importProjectLocked(root, sourceMarmotDir, project, opts)
		if err != nil {
			return err
		}
		saved = m
		return nil
	})
	if err != nil {
		return nil, err
	}
	return saved, nil
}

// importProjectLocked is ImportProject's body; the caller must hold the
// manifest flock.
func importProjectLocked(root, sourceMarmotDir string, project Project, opts ImportOptions) (*Manifest, error) {
	manifest, body, err := LoadManifest(root)
	if err != nil {
		return nil, err
	}
	if err := checkManifestWritable(manifest); err != nil {
		return nil, err
	}
	source, err := filepath.Abs(sourceMarmotDir)
	if err != nil {
		return nil, fmt.Errorf("resolve source .marmot path: %w", err)
	}
	info, err := os.Stat(source)
	if err != nil {
		return nil, fmt.Errorf("stat source .marmot: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("source %q is not a directory", sourceMarmotDir)
	}
	sourceReal, err := filepath.EvalSymlinks(source)
	if err != nil {
		return nil, fmt.Errorf("resolve source .marmot symlinks: %w", err)
	}
	if _, err := os.Stat(filepath.Join(source, "_config.md")); err != nil {
		return nil, fmt.Errorf("source .marmot is missing _config.md: %w", err)
	}
	project.ProjectID = strings.TrimSpace(project.ProjectID)
	if project.ProjectID == "" {
		project.ProjectID = generateProjectIDFromPath(source)
	}
	if project.Path == "" {
		project.Path = defaultProjectPath(project.ProjectID)
	}
	project.Path = filepath.ToSlash(filepath.Clean(project.Path))
	project.Aliases = uniqueSorted(project.Aliases)
	// Manifest v3 provenance: canonicalize at write time so every stored
	// source_url is directly comparable (resolver matches canonical forms).
	if url := CanonicalRepoURL(opts.SourceURL); url != "" {
		project.SourceURL = url
	}
	if commit := strings.TrimSpace(opts.SourceCommit); commit != "" {
		project.SourceCommit = commit
	}
	if err := ValidateProjectID(project.ProjectID); err != nil {
		return nil, err
	}
	for _, existing := range manifest.Projects {
		if existing.ProjectID == project.ProjectID {
			return nil, fmt.Errorf("project %q already exists", project.ProjectID)
		}
		if filepath.ToSlash(filepath.Clean(existing.Path)) == project.Path {
			return nil, fmt.Errorf("project path %q is already registered to project %q", project.Path, existing.ProjectID)
		}
	}
	targetRel, err := validateProjectPath(root, project.Path)
	if err != nil {
		return nil, err
	}
	if filepath.IsAbs(targetRel) {
		return nil, fmt.Errorf("import destination path %q must stay inside the Warren root", project.Path)
	}
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve Warren root: %w", err)
	}
	rootReal, err := filepath.EvalSymlinks(rootAbs)
	if err != nil {
		return nil, fmt.Errorf("resolve Warren root symlinks: %w", err)
	}
	target := filepath.Join(rootAbs, targetRel)
	if samePath(source, target) {
		return nil, fmt.Errorf("source and destination must differ")
	}
	if pathContains(sourceReal, target) || pathContains(target, sourceReal) {
		return nil, fmt.Errorf("source and destination paths must not overlap")
	}
	if _, err := os.Lstat(target); err == nil {
		return nil, fmt.Errorf("destination %q already exists", project.Path)
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("stat destination: %w", err)
	}
	vaultID := strings.TrimSpace(opts.VaultID)
	if vaultID == "" {
		vaultID = sourceVaultID(source)
	}
	if vaultID == "" {
		vaultID = project.ProjectID
	}
	if err := ValidateProjectID(vaultID); err != nil {
		return nil, err
	}
	parent := filepath.Dir(target)
	parentReal, err := ensureImportParent(rootAbs, rootReal, parent)
	if err != nil {
		return nil, err
	}
	targetReal := filepath.Join(parentReal, filepath.Base(target))
	if pathContains(sourceReal, targetReal) || pathContains(targetReal, sourceReal) {
		return nil, fmt.Errorf("source and destination paths must not overlap")
	}
	tmp, err := os.MkdirTemp(parent, ".warren-import-*.tmp")
	if err != nil {
		return nil, fmt.Errorf("create import temp dir: %w", err)
	}
	success := false
	defer func() {
		if !success {
			_ = os.RemoveAll(tmp)
		}
	}()
	// Flush the source DB's WAL so the sidecar-excluding copy below cannot
	// lose un-checkpointed writes (e.g. from a marmot serve holding the DB).
	checkpointEmbeddings(source)
	if err := copyMarmotVault(source, tmp, opts); err != nil {
		return nil, err
	}
	meta := &ProjectMetadata{
		ProjectID: project.ProjectID,
		WarrenID:  manifest.WarrenID,
		VaultID:   vaultID,
		Aliases:   project.Aliases,
	}
	if err := SaveProjectMetadata(tmp, meta, ""); err != nil {
		return nil, err
	}
	if err := os.Rename(tmp, target); err != nil {
		return nil, fmt.Errorf("commit imported project: %w", err)
	}
	success = true
	manifest.Projects = append(manifest.Projects, project)
	if err := SaveManifest(rootAbs, manifest, body); err != nil {
		_ = os.RemoveAll(target)
		return nil, err
	}
	saved, _, err := LoadManifest(rootAbs)
	if err != nil {
		return nil, err
	}
	return saved, nil
}

// ListProjects returns normalized Warren projects.
func ListProjects(root string) ([]Project, error) {
	manifest, _, err := LoadManifest(root)
	if err != nil {
		return nil, err
	}
	if len(manifest.Projects) == 0 {
		return []Project{}, nil
	}
	return append([]Project(nil), manifest.Projects...), nil
}

// RemoveProject removes a project from the Warren manifest and drops related bridges.
func RemoveProject(root, projectID string) (*Manifest, error) {
	if err := ValidateProjectID(projectID); err != nil {
		return nil, err
	}
	return updateManifest(root, func(manifest *Manifest) error {
		projects := manifest.Projects[:0]
		found := false
		for _, project := range manifest.Projects {
			if project.ProjectID == projectID {
				found = true
				continue
			}
			projects = append(projects, project)
		}
		if !found {
			return fmt.Errorf("project %q does not exist", projectID)
		}
		manifest.Projects = projects
		bridges := manifest.Bridges[:0]
		for _, bridge := range manifest.Bridges {
			if bridge.Source != projectID && bridge.Target != projectID {
				bridges = append(bridges, bridge)
			}
		}
		manifest.Bridges = bridges
		return nil
	})
}

// RenameResult reports what RenameProject changed beyond the manifest edit.
type RenameResult struct {
	Manifest *Manifest
	// Moved is true when the conventional projects/<oldID>/ directory was
	// renamed to projects/<newID>/ (and the manifest path updated with it).
	Moved bool
	// OldDir and NewDir are the warren-relative directories of the move
	// (set only when Moved).
	OldDir string
	NewDir string
	// PathKept is the unchanged manifest path when no move happened
	// (keepPath requested, unconventional layout, or missing directory).
	PathKept string
	// Repointed lists other projects whose checkouts were nested under the
	// moved projects/<oldID>/ directory; their manifest paths were rewritten
	// to the new prefix in the same transaction so the move never strands
	// them (set only when Moved).
	Repointed []string
	// VaultID is the project's checkout metadata vault_id after the rename
	// ("" when the checkout metadata is unreadable). Rename never rewrites
	// it: vault_id is the identity key (what warren mounts route by and
	// what identifies a workspace's own project) and must stay stable
	// across renames — change it only by re-importing with --vault-id.
	VaultID string
}

// RenameProject renames a project ID in the manifest, project metadata, and
// bridges. When the project lives at the conventional
// projects/<oldID>/.marmot path (and keepPath is false), the project
// directory is moved to projects/<newID>/ as well — the move happens before
// the manifest write, so a failed move leaves the manifest consistent.
// The checkout's vault_id is deliberately left untouched.
func RenameProject(root, oldID, newID string, keepPath bool) (*RenameResult, error) {
	if err := ValidateProjectID(oldID); err != nil {
		return nil, err
	}
	if err := ValidateProjectID(newID); err != nil {
		return nil, err
	}
	if oldID == newID {
		return nil, fmt.Errorf("new project ID must differ from old project ID")
	}
	result := &RenameResult{}
	var renamed Project
	manifest, err := updateManifest(root, func(manifest *Manifest) error {
		idx := -1
		for i := range manifest.Projects {
			if manifest.Projects[i].ProjectID == newID {
				return fmt.Errorf("project %q already exists", newID)
			}
			if manifest.Projects[i].ProjectID == oldID {
				idx = i
			}
		}
		if idx < 0 {
			return fmt.Errorf("project %q does not exist", oldID)
		}
		project := &manifest.Projects[idx]
		project.ProjectID = newID
		oldPath := filepath.ToSlash(project.Path)
		oldDir := "projects/" + oldID
		conventional := !filepath.IsAbs(project.Path) && strings.HasPrefix(oldPath, oldDir+"/")
		sourceDir := filepath.Join(root, filepath.FromSlash(oldDir))
		if conventional {
			if fi, statErr := os.Stat(sourceDir); statErr != nil || !fi.IsDir() {
				conventional = false // torn or never-created checkout: rename the ID only
			}
		}
		if !keepPath && conventional {
			newDir := "projects/" + newID
			target := filepath.Join(root, filepath.FromSlash(newDir))
			if _, statErr := os.Stat(target); statErr == nil {
				return fmt.Errorf("cannot move project directory: %s already exists in the warren — remove it or re-run with --keep-path", newDir)
			} else if !errors.Is(statErr, os.ErrNotExist) {
				return fmt.Errorf("stat %s: %w", newDir, statErr)
			}
			// Move first, manifest write last (updateManifest saves after
			// this callback returns): a failed move aborts before the
			// manifest commit point.
			if err := os.Rename(sourceDir, target); err != nil {
				return fmt.Errorf("move project directory: %w", err)
			}
			project.Path = filepath.ToSlash(filepath.Join(newDir, strings.TrimPrefix(oldPath, oldDir+"/")))
			result.Moved, result.OldDir, result.NewDir = true, oldDir, newDir
			// The move relocates everything under projects/<oldID>/ — any
			// other project whose checkout is nested there (the manifest
			// accepts arbitrary relative paths) moved with it, so repoint
			// its manifest path in the same transaction.
			for i := range manifest.Projects {
				if i == idx {
					continue
				}
				nested := filepath.ToSlash(manifest.Projects[i].Path)
				if !filepath.IsAbs(manifest.Projects[i].Path) && strings.HasPrefix(nested, oldDir+"/") {
					manifest.Projects[i].Path = filepath.ToSlash(filepath.Join(newDir, strings.TrimPrefix(nested, oldDir+"/")))
					result.Repointed = append(result.Repointed, manifest.Projects[i].ProjectID)
				}
			}
		} else {
			result.PathKept = oldPath
		}
		renamed = *project
		for i := range manifest.Bridges {
			if manifest.Bridges[i].Source == oldID {
				manifest.Bridges[i].Source = newID
			}
			if manifest.Bridges[i].Target == oldID {
				manifest.Bridges[i].Target = newID
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if err := ensureProjectMetadata(root, manifest.WarrenID, renamed); err != nil {
		return nil, err
	}
	if meta, _, metaErr := LoadProjectMetadata(filepath.Join(root, filepath.FromSlash(renamed.Path))); metaErr == nil && meta != nil {
		result.VaultID = meta.VaultID
	}
	result.Manifest = manifest
	return result, nil
}

// SetProjectReadOnly flips the warren author's write policy for one project.
// ReadOnly projects cannot be made editable by consumers (SetEditable) and
// reject write-backs (WriteEditableNode) even under stale mount state.
func SetProjectReadOnly(root, projectID string, readOnly bool) (*Manifest, error) {
	if err := ValidateProjectID(projectID); err != nil {
		return nil, err
	}
	return updateManifest(root, func(manifest *Manifest) error {
		for i := range manifest.Projects {
			if manifest.Projects[i].ProjectID == projectID {
				manifest.Projects[i].ReadOnly = readOnly
				return nil
			}
		}
		return fmt.Errorf("project %q does not exist", projectID)
	})
}

// AddBridge adds or merges allowed relations for a cross-project bridge.
func AddBridge(root string, bridge Bridge) (*Manifest, error) {
	normalizeBridge(&bridge)
	if err := validateBridge(bridge); err != nil {
		return nil, err
	}
	return updateManifest(root, func(manifest *Manifest) error {
		known := projectSet(manifest.Projects)
		if !known[bridge.Source] {
			return fmt.Errorf("bridge source project %q does not exist", bridge.Source)
		}
		if !known[bridge.Target] {
			return fmt.Errorf("bridge target project %q does not exist", bridge.Target)
		}
		merged := false
		for i := range manifest.Bridges {
			if manifest.Bridges[i].Source == bridge.Source && manifest.Bridges[i].Target == bridge.Target {
				manifest.Bridges[i].Relations = uniqueSorted(append(manifest.Bridges[i].Relations, bridge.Relations...))
				merged = true
				break
			}
		}
		if !merged {
			manifest.Bridges = append(manifest.Bridges, bridge)
		}
		return nil
	})
}

// ListBridges returns normalized Warren bridges.
func ListBridges(root string) ([]Bridge, error) {
	manifest, _, err := LoadManifest(root)
	if err != nil {
		return nil, err
	}
	if len(manifest.Bridges) == 0 {
		return []Bridge{}, nil
	}
	return append([]Bridge(nil), manifest.Bridges...), nil
}

// RemoveBridge removes a bridge, or only the supplied relations when provided.
func RemoveBridge(root, source, target string, relations ...string) (*Manifest, error) {
	if err := ValidateProjectID(source); err != nil {
		return nil, err
	}
	if err := ValidateProjectID(target); err != nil {
		return nil, err
	}
	relations = uniqueSorted(relations)
	for _, relation := range relations {
		if err := validateRelation(relation); err != nil {
			return nil, err
		}
	}
	return updateManifest(root, func(manifest *Manifest) error {
		found := false
		bridges := manifest.Bridges[:0]
		for _, bridge := range manifest.Bridges {
			if bridge.Source != source || bridge.Target != target {
				bridges = append(bridges, bridge)
				continue
			}
			found = true
			if len(relations) == 0 {
				continue
			}
			bridge.Relations = removeNames(bridge.Relations, relations)
			if len(bridge.Relations) > 0 {
				bridges = append(bridges, bridge)
			}
		}
		if !found {
			return fmt.Errorf("bridge %q -> %q does not exist", source, target)
		}
		manifest.Bridges = bridges
		return nil
	})
}

// Doctor validates that the Warren manifest points to coherent project metadata.
func Doctor(root string) (DoctorReport, error) {
	manifest, _, err := LoadManifest(root)
	if err != nil {
		return DoctorReport{}, err
	}
	report := DoctorReport{WarrenID: manifest.WarrenID}
	known := projectSet(manifest.Projects)
	vaultIDs := make(map[string]string)
	// The manifest flock file lives next to _warren.md inside the (usually
	// git-backed) warren repo; committing it would fight other clones' locks.
	// Only meaningful when the root actually is a git repo (fixtures and
	// plain-dir warrens stay quiet).
	if _, err := os.Stat(filepath.Join(root, ".git")); err == nil && !gitignoreHasEntry(root, ManifestFileName+".lock") {
		report.Issues = append(report.Issues, DoctorIssue{
			Severity: "info",
			Code:     "lockfile_not_ignored",
			Message:  fmt.Sprintf("add %s.lock to .gitignore (the manifest flock file must not be committed)", ManifestFileName),
			Path:     ".gitignore",
		})
	}
	if dirExists(filepath.Join(root, ".marmot-data", "warrens")) {
		report.Issues = append(report.Issues, DoctorIssue{
			Severity: "warning",
			Code:     "materialized_cache_in_warren",
			Message:  "materialized Warren cache path exists inside the Warren repository",
			Path:     ".marmot-data/warrens",
		})
	}
	projectModels := make(map[string][]string)
	for _, project := range manifest.Projects {
		projectPath := filepath.Join(root, filepath.FromSlash(project.Path))
		// An absolute path: only resolves on the author's machine; it is
		// legal at runtime (validateProjectPath accepts it) but breaks every
		// clone of the warren.
		if filepath.IsAbs(filepath.FromSlash(project.Path)) {
			report.Issues = append(report.Issues, DoctorIssue{
				Severity:  "warning",
				Code:      "absolute_project_path",
				Message:   fmt.Sprintf("project %q uses an absolute path; the warren will not work when cloned elsewhere", project.ProjectID),
				ProjectID: project.ProjectID,
				Path:      project.Path,
			})
		}
		info, statErr := os.Stat(projectPath)
		if statErr != nil {
			report.Issues = append(report.Issues, DoctorIssue{
				Severity:  "error",
				Code:      "project_missing",
				Message:   fmt.Sprintf("project %q path is not available", project.ProjectID),
				ProjectID: project.ProjectID,
				Path:      project.Path,
			})
			continue
		}
		if !info.IsDir() {
			report.Issues = append(report.Issues, DoctorIssue{
				Severity:  "error",
				Code:      "project_not_directory",
				Message:   fmt.Sprintf("project %q path is not a directory", project.ProjectID),
				ProjectID: project.ProjectID,
				Path:      project.Path,
			})
			continue
		}
		meta, _, metaErr := LoadProjectMetadata(projectPath)
		if metaErr != nil {
			report.Issues = append(report.Issues, DoctorIssue{
				Severity:  "warning",
				Code:      "metadata_unreadable",
				Message:   fmt.Sprintf("project %q metadata could not be read: %v", project.ProjectID, metaErr),
				ProjectID: project.ProjectID,
				Path:      project.Path,
			})
			continue
		}
		if meta.ProjectID != project.ProjectID {
			report.Issues = append(report.Issues, DoctorIssue{
				Severity:  "error",
				Code:      "project_id_mismatch",
				Message:   fmt.Sprintf("project %q metadata has project ID %q", project.ProjectID, meta.ProjectID),
				ProjectID: project.ProjectID,
				Path:      project.Path,
			})
		}
		if meta.WarrenID != manifest.WarrenID {
			report.Issues = append(report.Issues, DoctorIssue{
				Severity:  "error",
				Code:      "warren_id_mismatch",
				Message:   fmt.Sprintf("project %q metadata has Warren ID %q", project.ProjectID, meta.WarrenID),
				ProjectID: project.ProjectID,
				Path:      project.Path,
			})
		}
		if meta.VaultID != "" {
			if existingProject, ok := vaultIDs[meta.VaultID]; ok && existingProject != project.ProjectID {
				report.Issues = append(report.Issues, DoctorIssue{
					Severity:  "error",
					Code:      "duplicate_vault_id",
					Message:   fmt.Sprintf("project %q and project %q both use vault ID %q", existingProject, project.ProjectID, meta.VaultID),
					ProjectID: project.ProjectID,
					Path:      project.Path,
				})
			} else {
				vaultIDs[meta.VaultID] = project.ProjectID
			}
		}
		dbPath := filepath.Join(projectPath, ".marmot-data", "embeddings.db")
		if _, err := os.Stat(dbPath); err != nil {
			report.Issues = append(report.Issues, DoctorIssue{
				Severity:  "warning",
				Code:      "embeddings_missing",
				Message:   fmt.Sprintf("project %q has no embedding database; run indexing before relying on semantic search", project.ProjectID),
				ProjectID: project.ProjectID,
				Path:      project.Path,
			})
			continue
		}
		models, hasStatus, dbErr := inspectEmbeddingsDB(dbPath)
		if dbErr != nil {
			// Degrade, don't break doctor: an unreadable/corrupt DB is
			// worth a note but must not mask the structural checks above.
			fmt.Fprintf(warnWriter, "warning: project %q embeddings database could not be inspected: %v\n", project.ProjectID, dbErr)
			continue
		}
		if !hasStatus {
			report.Issues = append(report.Issues, DoctorIssue{
				Severity:  "warning",
				Code:      "schema_stale",
				Message:   fmt.Sprintf("project %q embeddings database was indexed by an older marmot (missing the status column) and cannot serve remote searches; re-import the project", project.ProjectID),
				ProjectID: project.ProjectID,
				Path:      project.Path,
			})
		}
		if len(models) > 0 {
			projectModels[project.ProjectID] = models
		}
	}
	report.Issues = append(report.Issues, modelSkewIssues(manifest.Projects, projectModels)...)
	for _, bridge := range manifest.Bridges {
		if !known[bridge.Source] {
			report.Issues = append(report.Issues, DoctorIssue{
				Severity: "error",
				Code:     "bridge_source_missing",
				Message:  fmt.Sprintf("bridge source project %q is not registered", bridge.Source),
			})
		}
		if !known[bridge.Target] {
			report.Issues = append(report.Issues, DoctorIssue{
				Severity: "error",
				Code:     "bridge_target_missing",
				Message:  fmt.Sprintf("bridge target project %q is not registered", bridge.Target),
			})
		}
	}
	return report, nil
}

// inspectEmbeddingsDB opens a project embeddings DB strictly read-only
// (doctor must never mutate the vaults it inspects) and reports its stored
// model set plus whether the soft-delete status column exists.
func inspectEmbeddingsDB(dbPath string) (models []string, hasStatus bool, err error) {
	store, err := embedding.NewStoreReadOnly(dbPath)
	if err != nil {
		return nil, false, err
	}
	defer func() { _ = store.Close() }()
	hasStatus, err = store.HasStatusColumn()
	if err != nil {
		return nil, false, err
	}
	models, err = store.Models()
	if err != nil {
		return nil, false, err
	}
	return models, hasStatus, nil
}

// modelSkewIssues emits one warning per project whose stored embedding
// models differ from the first indexed project's: SearchActive filters
// WHERE model = ?, so cross-project semantic search between skewed projects
// silently returns nothing. projects supplies deterministic (manifest)
// order; projectModels holds each indexed project's sorted model set.
func modelSkewIssues(projects []Project, projectModels map[string][]string) []DoctorIssue {
	var issues []DoctorIssue
	baseProject := ""
	var baseModels []string
	for _, project := range projects {
		models, ok := projectModels[project.ProjectID]
		if !ok {
			continue
		}
		if baseProject == "" {
			baseProject, baseModels = project.ProjectID, models
			continue
		}
		if equalStringSlices(baseModels, models) {
			continue
		}
		issues = append(issues, DoctorIssue{
			Severity:  "warning",
			Code:      "model_skew",
			Message:   fmt.Sprintf("project %q embeddings use model(s) %s but project %q uses %s; cross-project semantic search between these will return no results", baseProject, strings.Join(baseModels, ","), project.ProjectID, strings.Join(models, ",")),
			ProjectID: project.ProjectID,
			Path:      project.Path,
		})
	}
	return issues
}

func equalStringSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// gitignoreHasEntry reports whether <root>/.gitignore carries entry as an
// exact line (the read-only mirror of the CLI's ensureGitignoreEntry).
func gitignoreHasEntry(root, entry string) bool {
	data, err := os.ReadFile(filepath.Join(root, ".gitignore"))
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) == entry {
			return true
		}
	}
	return false
}

// DoctorWorkspace checks workspace-level warren consistency: vault-ID
// collisions across the local vault and every registered warren's active
// projects, plus identity health. Mount refuses new collisions
// unconditionally, so doctor errors exactly where mount refuses (editable
// self-mounts, true collisions) and is healthy exactly where identity is
// derived (identified projects, reported as self_identity info; a redundant
// R1-era self-mount record is a second info with the unmount cleanup).
// Legacy editable/materialized self-mount state written by older binaries
// surfaces here with its remediation verb.
func DoctorWorkspace(workspaceMarmotDir, workspaceRoot string) (DoctorReport, error) {
	// State is keyed off the marmot dir, not workspaceRoot: identical for
	// classic workspaces (<root>/.marmot/_warren.md IS <marmotDir>/_warren.md)
	// and correct for direct-state vaults (den identity vaults), whose state
	// lives in the vault itself and never resolves through a parent root.
	state, _, err := LoadWorkspaceStateFromMarmot(workspaceMarmotDir)
	if err != nil {
		return DoctorReport{}, err
	}
	report := DoctorReport{}
	local := sourceVaultID(workspaceMarmotDir)
	// Identity is derived exactly the way the engine derives it —
	// ActiveMounts' manifest-wide scan — so doctor and the engine can never
	// disagree about who is identified. Best-effort: the state file already
	// loaded above, so a scan failure only drops the info rows.
	if local != "" {
		if mounts, mountsErr := ActiveMounts(workspaceMarmotDir); mountsErr == nil {
			for _, mount := range mounts {
				if !mount.SelfAlias {
					continue
				}
				report.Issues = append(report.Issues, DoctorIssue{
					Severity:  "info",
					Code:      "self_identity",
					Message:   fmt.Sprintf("project %s/%s is identified with this workspace (vault ID %q); it serves from the live vault", mount.WarrenID, mount.ProjectID, local),
					ProjectID: mount.ProjectID,
				})
			}
		}
	}
	claims := vaultIDClaims(workspaceMarmotDir, state)
	vaultIDs := make([]string, 0, len(claims))
	for vaultID := range claims {
		vaultIDs = append(vaultIDs, vaultID)
	}
	sort.Strings(vaultIDs)
	for _, vaultID := range vaultIDs {
		owners := claims[vaultID]
		if len(owners) < 2 {
			continue
		}
		if local != "" && vaultID == local {
			// The non-local claimants are identified projects of the live
			// vault, not duplicate claims: they never route, so nothing
			// resolves arbitrarily. A recorded mount entry is redundant
			// (identity is automatic) — info with the unmount cleanup; legacy
			// editable state is the split-brain error; a legacy burrow cache
			// is a stale shadow warning.
			for _, owner := range owners {
				if owner.WarrenID == "" {
					continue // the local vault's own claim
				}
				report.Issues = append(report.Issues, DoctorIssue{
					Severity:  "info",
					Code:      "self_alias_mount",
					Message:   fmt.Sprintf("project %s/%s has a redundant self-mount recorded — identity is automatic; clean with 'marmot warren unmount --warren %s %s'", owner.WarrenID, owner.ProjectID, owner.WarrenID, owner.ProjectID),
					ProjectID: owner.ProjectID,
				})
				if containsName(state.Warrens[owner.WarrenID].EditableProjects, owner.ProjectID) {
					report.Issues = append(report.Issues, DoctorIssue{
						Severity:  "error",
						Code:      "self_alias_editable",
						Message:   fmt.Sprintf("project %s/%s aliases the local vault but is marked editable — @-writes would split-brain; run 'marmot warren edit %s --warren %s --off'", owner.WarrenID, owner.ProjectID, owner.ProjectID, owner.WarrenID),
						ProjectID: owner.ProjectID,
					})
				}
				if cached := materializedProjectPath(workspaceMarmotDir, owner.WarrenID, owner.ProjectID); dirExists(cached) {
					report.Issues = append(report.Issues, DoctorIssue{
						Severity:  "warning",
						Code:      "self_alias_materialized",
						Message:   fmt.Sprintf("project %s/%s aliases the local vault but has a burrow cache (a stale shadow of the live vault); drop it with 'marmot warren burrow --drop --warren %s %s'", owner.WarrenID, owner.ProjectID, owner.WarrenID, owner.ProjectID),
						ProjectID: owner.ProjectID,
						Path:      cached,
					})
				}
			}
			continue
		}
		names := make([]string, 0, len(owners))
		for _, owner := range owners {
			if owner.WarrenID == "" {
				names = append(names, owner.ProjectID)
				continue
			}
			names = append(names, owner.WarrenID+"/"+owner.ProjectID)
		}
		report.Issues = append(report.Issues, DoctorIssue{
			Severity: "error",
			Code:     "vault_id_collision_workspace",
			Message:  fmt.Sprintf("vault ID %q is claimed by %s; queries resolve to one of them arbitrarily — unmount or re-import with distinct vault IDs", vaultID, strings.Join(names, " and ")),
		})
	}
	report.Issues = append(report.Issues, unreachableWarrenIssues(state)...)
	report.Issues = append(report.Issues, localRouteMismatchIssue(workspaceMarmotDir, local)...)
	return report, nil
}

// unreachableWarrenIssues warns for every REGISTERED warren whose manifest
// cannot be read (checkout moved, deleted, or corrupted) — including warrens
// with zero active projects, which previously surfaced nowhere: the graph
// view's skip toasts only fire for mounted projects, so an idle registration
// pointing at a vanished checkout was completely silent in both the UI and
// doctor. Warning, not error: nothing is broken until the user tries to use
// the warren, and the escape hatches are named in the message.
func unreachableWarrenIssues(state *WorkspaceState) []DoctorIssue {
	ids := make([]string, 0, len(state.Warrens))
	for id := range state.Warrens {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	var issues []DoctorIssue
	for _, id := range ids {
		entry := state.Warrens[id]
		if _, _, err := LoadManifest(entry.Path); err != nil {
			issues = append(issues, DoctorIssue{
				Severity: "warning",
				Code:     "warren_unreachable",
				Message: fmt.Sprintf(
					"warren %q is unreachable at %s (%v) — if the checkout moved, re-run 'marmot warren register %s <new-path>'; to drop it, 'marmot warren unregister --warren %s'",
					id, entry.Path, err, id, id),
				Path: entry.Path,
			})
		}
	}
	return issues
}

// localRouteMismatchIssue warns when the global routing table maps the local
// vault ID to a path other than this workspace's .marmot — the one remaining
// manual way ('marmot route add') to shadow the live vault with another copy.
// Warning, not error: two checkouts of one repo legitimately share a
// vault_id. Best-effort in every direction: no local ID, an unreadable or
// disabled routing table, and unresolvable paths all stay silent.
func localRouteMismatchIssue(workspaceMarmotDir, local string) []DoctorIssue {
	if local == "" {
		return nil
	}
	rt, err := routes.Load()
	if err != nil || rt == nil {
		return nil
	}
	routed, ok := rt.Get(local)
	if !ok {
		return nil
	}
	absLocal, err := filepath.Abs(workspaceMarmotDir)
	if err != nil {
		return nil
	}
	if sameResolvedPath(routed, absLocal) {
		return nil
	}
	return []DoctorIssue{{
		Severity: "warning",
		Code:     "local_route_mismatch",
		Message:  fmt.Sprintf("global routing table maps this workspace's vault ID %q to %s, not this workspace's %s; @-references resolved through routes.yml will read that copy — fix with 'marmot route add %s %s' if this workspace should answer", local, routed, absLocal, local, absLocal),
		Path:     routed,
	}}
}

// sameResolvedPath compares two paths after best-effort symlink resolution
// (macOS tempdirs live under the /var -> /private/var symlink); it falls
// back to samePath's lexical comparison when either side cannot resolve.
func sameResolvedPath(a, b string) bool {
	ra, errA := filepath.EvalSymlinks(a)
	rb, errB := filepath.EvalSymlinks(b)
	if errA == nil && errB == nil {
		return filepath.Clean(ra) == filepath.Clean(rb)
	}
	return samePath(a, b)
}

// Format rewrites the manifest with canonical YAML while preserving markdown body text.
func Format(root string) (*Manifest, error) {
	return updateManifest(root, func(*Manifest) error { return nil })
}

// GenerateProjectID converts display text into a safe Warren project ID.
func GenerateProjectID(name string) string {
	name = strings.TrimSpace(strings.ToLower(name))
	var b strings.Builder
	lastDash := false
	for _, r := range name {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
			lastDash = false
			continue
		}
		if b.Len() > 0 && !lastDash {
			b.WriteByte('-')
			lastDash = true
		}
	}
	id := strings.Trim(b.String(), "-")
	if id == "" {
		return "project"
	}
	if len(id) > 128 {
		id = strings.Trim(id[:128], "-")
	}
	if id == "" {
		return "project"
	}
	return id
}

// LoadManifest reads _warren.md from a Warren repository root.
func LoadManifest(root string) (*Manifest, string, error) {
	path, err := manifestPath(root)
	if err != nil {
		return nil, "", err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, "", fmt.Errorf("read Warren manifest: %w", err)
	}
	var m Manifest
	body, err := parseMarkdownYAML(data, &m)
	if err != nil {
		return nil, "", fmt.Errorf("parse Warren manifest: %w", err)
	}
	// Read paths stay permissive on newer manifests (best-effort queries keep
	// working); write paths refuse via checkManifestWritable.
	if m.Version > CurrentManifestVersion {
		fmt.Fprintf(warnWriter, "warning: warren manifest %s is version %d (this marmot supports <= %d); fields may be ignored, do not edit with this binary\n", path, m.Version, CurrentManifestVersion)
	}
	if m.WarrenID == "" {
		m.WarrenID = GenerateProjectID(filepath.Base(root))
	}
	defaultProjectPaths(&m)
	if err := validateManifest(root, &m); err != nil {
		return nil, "", err
	}
	normalizeManifest(&m)
	if err := validateManifest(root, &m); err != nil {
		return nil, "", err
	}
	return &m, body, nil
}

// SaveManifest writes _warren.md to a Warren repository root.
func SaveManifest(root string, manifest *Manifest, body string) error {
	path, err := manifestPath(root)
	if err != nil {
		return err
	}
	if manifest == nil {
		manifest = &Manifest{}
	}
	if err := validateManifest(root, manifest); err != nil {
		return err
	}
	normalizeManifest(manifest)
	if err := validateManifest(root, manifest); err != nil {
		return err
	}
	return writeMarkdownYAML(path, manifest, body)
}

// LoadProjectMetadata reads .marmot/_warren.md for a Warren project.
func LoadProjectMetadata(marmotDir string) (*ProjectMetadata, string, error) {
	path, err := projectMetadataPath(marmotDir)
	if err != nil {
		return nil, "", err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, "", fmt.Errorf("read Warren project metadata: %w", err)
	}
	var meta ProjectMetadata
	body, err := parseMarkdownYAML(data, &meta)
	if err != nil {
		return nil, "", fmt.Errorf("parse Warren project metadata: %w", err)
	}
	normalizeProjectMetadata(&meta)
	if err := validateProjectMetadata(&meta); err != nil {
		return nil, "", err
	}
	return &meta, body, nil
}

// SaveProjectMetadata writes .marmot/_warren.md for a Warren project.
func SaveProjectMetadata(marmotDir string, meta *ProjectMetadata, body string) error {
	path, err := projectMetadataPath(marmotDir)
	if err != nil {
		return err
	}
	if meta == nil {
		meta = &ProjectMetadata{}
	}
	normalizeProjectMetadata(meta)
	if err := validateProjectMetadata(meta); err != nil {
		return err
	}
	return writeMarkdownYAML(path, meta, body)
}

// LoadWorkspaceState reads .marmot/_warren.md from a local workspace root.
func LoadWorkspaceState(workspaceRoot string) (*WorkspaceState, string, error) {
	path, err := workspaceStatePath(workspaceRoot)
	if err != nil {
		return nil, "", err
	}
	return loadWorkspaceStatePath(path)
}

// LoadWorkspaceStateFromMarmot reads _warren.md from an explicit .marmot dir.
func LoadWorkspaceStateFromMarmot(marmotDir string) (*WorkspaceState, string, error) {
	if err := validateNonEmptyPath("marmot dir", marmotDir); err != nil {
		return nil, "", err
	}
	return loadWorkspaceStatePath(filepath.Join(marmotDir, ManifestFileName))
}

func loadWorkspaceStatePath(path string) (*WorkspaceState, string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return defaultWorkspaceState(), "", nil
		}
		return nil, "", fmt.Errorf("read Warren workspace state: %w", err)
	}
	var state WorkspaceState
	body, err := parseMarkdownYAML(data, &state)
	if err != nil {
		return nil, "", fmt.Errorf("parse Warren workspace state: %w", err)
	}
	normalizeWorkspaceState(&state)
	if err := validateWorkspaceState(&state); err != nil {
		return nil, "", err
	}
	return &state, body, nil
}

// SaveWorkspaceState writes .marmot/_warren.md in a local workspace root.
func SaveWorkspaceState(workspaceRoot string, state *WorkspaceState, body string) error {
	path, err := workspaceStatePath(workspaceRoot)
	if err != nil {
		return err
	}
	return saveWorkspaceStatePath(path, state, body)
}

// SaveWorkspaceStateToMarmot writes _warren.md to an explicit .marmot dir.
func SaveWorkspaceStateToMarmot(marmotDir string, state *WorkspaceState, body string) error {
	if err := validateNonEmptyPath("marmot dir", marmotDir); err != nil {
		return err
	}
	return saveWorkspaceStatePath(filepath.Join(marmotDir, ManifestFileName), state, body)
}

// UpdateWorkspaceStateInMarmot mutates workspace state stored directly in an
// explicit marmot dir (<marmotDir>/_warren.md) under the same cross-process
// flock convention updateWorkspaceState uses for workspace roots. It is the
// shared load→mutate→save primitive for den identity vaults (den link's
// editable mounts, register/touch routing in the CLI). fn returns write=false
// to skip the save: idempotent no-op paths must not rewrite the file, because
// a rewrite is the change signal live daemon owners watch.
func UpdateWorkspaceStateInMarmot(marmotDir string, fn func(*WorkspaceState) (write bool, err error)) error {
	if err := validateNonEmptyPath("marmot dir", marmotDir); err != nil {
		return err
	}
	statePath := filepath.Join(marmotDir, ManifestFileName)
	return flock.WithLock(statePath+".lock", func() error {
		state, body, err := loadWorkspaceStatePath(statePath)
		if err != nil {
			return err
		}
		write, err := fn(state)
		if err != nil || !write {
			return err
		}
		return saveWorkspaceStatePath(statePath, state, body)
	})
}

func saveWorkspaceStatePath(path string, state *WorkspaceState, body string) error {
	if state == nil {
		state = defaultWorkspaceState()
	}
	normalizeWorkspaceState(state)
	if err := validateWorkspaceState(state); err != nil {
		return err
	}
	return writeMarkdownYAML(path, state, body)
}

// RegisterWorkspaceWarren records a Warren root in local workspace state.
func RegisterWorkspaceWarren(workspaceRoot, warrenID, warrenRoot string) (*WorkspaceState, error) {
	absRoot, err := filepath.Abs(warrenRoot)
	if err != nil {
		return nil, fmt.Errorf("resolve Warren path: %w", err)
	}
	manifest, _, err := LoadManifest(absRoot)
	if err != nil {
		return nil, err
	}
	if manifest.WarrenID != warrenID {
		return nil, fmt.Errorf("warren ID mismatch: manifest has %q, command used %q", manifest.WarrenID, warrenID)
	}
	return updateWorkspaceState(workspaceRoot, func(state *WorkspaceState) error {
		entry := state.Warrens[warrenID]
		entry.Path = absRoot
		state.Warrens[warrenID] = entry
		return nil
	})
}

// Mount marks projects active in the local workspace.
func Mount(workspaceRoot, warrenID string, projects []string, materialized bool) (*WorkspaceState, error) {
	wsMarmotDir := workspaceMarmotDir(workspaceRoot)
	return updateWorkspaceState(workspaceRoot, func(state *WorkspaceState) error {
		return applyMount(wsMarmotDir, state, warrenID, projects, materialized)
	})
}

// SetEditable toggles a single project's local writable state.
func SetEditable(workspaceRoot, warrenID, projectID string, editable bool) (*WorkspaceState, error) {
	wsMarmotDir := workspaceMarmotDir(workspaceRoot)
	return updateWorkspaceState(workspaceRoot, func(state *WorkspaceState) error {
		return applySetEditable(wsMarmotDir, state, warrenID, projectID, editable)
	})
}

// TouchWorkspaceState rewrites the workspace _warren.md unchanged (a no-op
// read-modify-write under the state flock), bumping its mtime and firing a
// rename event so every live observer of the file — the daemon owner's
// watcher in particular — reloads its warren state. This is the CLI→daemon
// change signal: no socket verb, just the file every observer already
// watches.
func TouchWorkspaceState(workspaceRoot string) error {
	_, err := updateWorkspaceState(workspaceRoot, func(*WorkspaceState) error { return nil })
	return err
}

// Unmount deactivates projects in the local workspace (and drops their
// editable flag). Burrow caches are deliberately untouched — unmount must be
// non-destructive so mount→unmount round-trips; use DropMaterialized to
// delete caches. Validation is against the workspace state, not the Warren
// manifest, so unmounting works even when the checkout is gone: this is the
// escape hatch for unreachable warrens.
func Unmount(workspaceRoot, warrenID string, projects []string) (*WorkspaceState, error) {
	wsMarmotDir := workspaceMarmotDir(workspaceRoot)
	return updateWorkspaceState(workspaceRoot, func(state *WorkspaceState) error {
		return applyUnmount(wsMarmotDir, state, warrenID, projects)
	})
}

// DropMaterialized deletes projects' burrow caches (the whole
// <workspaceMarmotDir>/.marmot-data/warrens/<warrenID>/projects/<p>/ dir)
// and clears the entry's Materialized flag once no cache remains. Caches are
// deleted BEFORE the state write: the workspace _warren.md rewrite is the
// change signal live daemon owners watch, so the reload it triggers must
// observe the final cache layout. Within the owner's ~1s debounce window a
// live routing table can still point at a just-deleted cache; those queries
// fail loudly (once-per-vault warnings) — the same bounded exposure as a
// re-burrow swap.
func DropMaterialized(workspaceMarmotDir, workspaceRoot, warrenID string, projects []string) error {
	// State access is keyed off workspaceMarmotDir (same file as workspaceRoot
	// resolution for classic workspaces; the only file for direct-state vaults).
	state, _, err := LoadWorkspaceStateFromMarmot(workspaceMarmotDir)
	if err != nil {
		return err
	}
	if _, ok := state.Warrens[warrenID]; !ok {
		return fmt.Errorf("warren %q is not registered in this workspace", warrenID)
	}
	for _, project := range projects {
		if err := ValidateProjectID(project); err != nil {
			return err
		}
		if !dirExists(materializedProjectPath(workspaceMarmotDir, warrenID, project)) {
			return fmt.Errorf("project %q has no burrow cache in warren %q", project, warrenID)
		}
	}
	for _, project := range projects {
		cacheDir := filepath.Dir(materializedProjectPath(workspaceMarmotDir, warrenID, project))
		if err := os.RemoveAll(cacheDir); err != nil {
			return fmt.Errorf("drop burrow cache for %q: %w", project, err)
		}
	}
	_, err = updateWorkspaceStateFromMarmot(workspaceMarmotDir, func(state *WorkspaceState) error {
		entry, ok := state.Warrens[warrenID]
		if !ok {
			return fmt.Errorf("warren %q is not registered in this workspace", warrenID)
		}
		if len(MaterializedProjects(workspaceMarmotDir, warrenID)) == 0 {
			entry.Materialized = false
		}
		state.Warrens[warrenID] = entry
		return nil
	})
	return err
}

// ClearStaleMaterialized clears a warren entry's Materialized flag when no
// burrow cache remains on disk. Mount sets the flag before the CLI's
// Materialize loop creates any cache, so a first-project materialize
// failure (rolled back by unmounting) can strand the flag with zero caches
// behind it — and with the stale flag, ActiveMounts' unreadable-manifest
// branch silently serves materializedStatuses instead of emitting the A6
// "mounts skipped" warning, while `burrow --drop` has nothing to drop. Safe
// to call any time: it re-checks the cache ground truth under the state
// flock and is a no-op while caches (or no entry) exist.
func ClearStaleMaterialized(workspaceMarmotDir, workspaceRoot, warrenID string) error {
	_, err := updateWorkspaceStateFromMarmot(workspaceMarmotDir, func(state *WorkspaceState) error {
		entry, ok := state.Warrens[warrenID]
		if !ok || !entry.Materialized {
			return nil
		}
		if len(MaterializedProjects(workspaceMarmotDir, warrenID)) == 0 {
			entry.Materialized = false
			state.Warrens[warrenID] = entry
		}
		return nil
	})
	return err
}

// MaterializedProjects lists the project IDs that currently have a burrow
// cache dir for the given warren in this workspace — the per-project ground
// truth behind the warren-level Materialized flag, and the expansion set for
// `burrow --drop --all`.
func MaterializedProjects(workspaceMarmotDir, warrenID string) []string {
	dir := filepath.Join(workspaceMarmotDir, ".marmot-data", "warrens", warrenID, "projects")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() && dirExists(filepath.Join(dir, e.Name(), MarmotDirName)) {
			out = append(out, e.Name())
		}
	}
	sort.Strings(out)
	return out
}

// Unregister removes a warren's entry from the workspace state. It refuses
// while projects are still mounted or burrow caches still exist, unless
// force; with force it also deletes the warren's whole cache tree. The
// no---force precondition checks, the cache-tree removal, and the entry
// delete all run inside the workspace state flock: checking on an unlocked
// snapshot and acting later is exactly the cross-process check-then-act
// race the A5 flock exists to prevent (a concurrent `warren mount`/`burrow`
// from another process would have its just-created caches deleted and its
// mounts silently dropped). As in DropMaterialized, caches are removed
// before the state write so live observers reload against the final layout.
func Unregister(workspaceMarmotDir, workspaceRoot, warrenID string, force bool) error {
	_, err := updateWorkspaceStateFromMarmot(workspaceMarmotDir, func(state *WorkspaceState) error {
		entry, ok := state.Warrens[warrenID]
		if !ok {
			return fmt.Errorf("warren %q is not registered in this workspace", warrenID)
		}
		if !force {
			if len(entry.ActiveProjects) > 0 {
				return fmt.Errorf("warren %q still has mounted project(s) %s; run 'marmot warren unmount --warren %s --all' first, or pass --force",
					warrenID, strings.Join(entry.ActiveProjects, ", "), warrenID)
			}
			if cached := MaterializedProjects(workspaceMarmotDir, warrenID); len(cached) > 0 {
				return fmt.Errorf("warren %q still has burrow cache(s) for %s; run 'marmot warren burrow --drop --warren %s --all' first, or pass --force",
					warrenID, strings.Join(cached, ", "), warrenID)
			}
		}
		if err := os.RemoveAll(filepath.Join(workspaceMarmotDir, ".marmot-data", "warrens", warrenID)); err != nil {
			return fmt.Errorf("remove warren cache tree: %w", err)
		}
		delete(state.Warrens, warrenID)
		return nil
	})
	return err
}

// vaultClaim records which mounted project (or the local vault) owns a vault
// ID in this workspace. A zero WarrenID means the local workspace vault.
type vaultClaim struct {
	WarrenID  string
	ProjectID string
}

// vaultIDClaims maps every vault ID claimed in this workspace — by the local
// vault itself or by any registered warren's active projects — to ALL of its
// claimants, in deterministic order (local vault first, then warrens sorted
// by ID). Resolution matches ActiveMounts (metadata vault_id, falling back
// to the project ID), because that is exactly what reaches the routing
// table. Warrens whose checkout is unreadable contribute their materialized
// statuses (if any) and are otherwise skipped: an unresolvable vault ID can
// never collide. It is the single claim builder shared by the mount-time
// refusal (claimedVaultIDs) and DoctorWorkspace's collision report.
func vaultIDClaims(workspaceMarmotDir string, state *WorkspaceState) map[string][]vaultClaim {
	claims := make(map[string][]vaultClaim)
	if local := sourceVaultID(workspaceMarmotDir); local != "" {
		claims[local] = append(claims[local], vaultClaim{ProjectID: "the local workspace vault"})
	}
	warrenIDs := make([]string, 0, len(state.Warrens))
	for warrenID := range state.Warrens {
		warrenIDs = append(warrenIDs, warrenID)
	}
	sort.Strings(warrenIDs)
	for _, warrenID := range warrenIDs {
		entry := state.Warrens[warrenID]
		manifest, _, err := LoadManifest(entry.Path)
		if err != nil {
			if entry.Materialized {
				for _, status := range materializedStatuses(workspaceMarmotDir, warrenID, entry) {
					if status.VaultID == "" {
						continue
					}
					claims[status.VaultID] = append(claims[status.VaultID], vaultClaim{WarrenID: warrenID, ProjectID: status.ProjectID})
				}
			}
			continue
		}
		projectMap := make(map[string]Project, len(manifest.Projects))
		for _, project := range manifest.Projects {
			projectMap[project.ProjectID] = project
		}
		for _, projectID := range entry.ActiveProjects {
			project, ok := projectMap[projectID]
			if !ok {
				continue
			}
			vaultID := mountVaultID(workspaceMarmotDir, warrenID, entry, project)
			claims[vaultID] = append(claims[vaultID], vaultClaim{WarrenID: warrenID, ProjectID: projectID})
		}
	}
	return claims
}

// claimedVaultIDs reduces vaultIDClaims to each vault ID's first (highest
// precedence) claimant — what the mount-time collision refusal checks
// against.
func claimedVaultIDs(workspaceMarmotDir string, state *WorkspaceState) map[string]vaultClaim {
	claimed := make(map[string]vaultClaim)
	for vaultID, owners := range vaultIDClaims(workspaceMarmotDir, state) {
		claimed[vaultID] = owners[0]
	}
	return claimed
}

// mountVaultID resolves the vault ID a project would claim in the routing
// table when mounted: its metadata vault_id if readable, else the project ID
// (the same fallback ActiveMounts uses).
func mountVaultID(workspaceMarmotDir, warrenID string, entry WorkspaceWarren, project Project) string {
	path := preferredProjectPath(workspaceMarmotDir, warrenID, entry, project)
	if meta, _, err := LoadProjectMetadata(path); err == nil && meta != nil && meta.VaultID != "" {
		return meta.VaultID
	}
	return project.ProjectID
}

// identifiedProject reports whether a warren project is identified with this
// workspace: its resolved vault_id equals the live vault's (the same
// predicate as ProjectStatus.SelfAlias, evaluated on demand). Best-effort in
// every direction — no local vault_id, an unreachable manifest, or an
// unregistered project all derive no identity — so callers that must keep
// working without the checkout (Unmount's escape hatch) can use it safely.
func identifiedProject(workspaceMarmotDir, warrenID string, entry WorkspaceWarren, projectID string) bool {
	local := sourceVaultID(workspaceMarmotDir)
	if local == "" {
		return false
	}
	manifest, _, err := LoadManifest(entry.Path)
	if err != nil {
		return false
	}
	for _, project := range manifest.Projects {
		if project.ProjectID == projectID {
			return mountVaultID(workspaceMarmotDir, warrenID, entry, project) == local
		}
	}
	return false
}

// refuseVaultIDCollision errors when vaultID is already claimed by another
// mounted warren project or by the local workspace vault. It is
// unconditional: every true conflict (two claimants that are not the local
// vault plus its self-aliases) refuses. Mount and SetEditable short-circuit
// self-aliases before calling this, so the local-claim case is unreachable
// from them — it stays refusing (not warning) as defense for future callers.
func refuseVaultIDCollision(claimed map[string]vaultClaim, vaultID, warrenID, projectID string) error {
	claim, taken := claimed[vaultID]
	if !taken || (claim.WarrenID == warrenID && claim.ProjectID == projectID) {
		return nil
	}
	owner := claim.WarrenID + "/" + claim.ProjectID
	if claim.WarrenID == "" {
		owner = claim.ProjectID // "the local workspace vault"
	}
	return fmt.Errorf("vault ID %q of project %s/%s collides with %s already mounted in this workspace — unmount it or re-import one with a distinct --vault-id", vaultID, warrenID, projectID, owner)
}

// SplitQualifiedVaultID splits a qualified "@vault-id/node-id" reference into
// its vault and node parts. It is the single parser shared by the HTTP API
// and MCP write paths.
func SplitQualifiedVaultID(id string) (vaultID, nodeID string, ok bool) {
	if !strings.HasPrefix(id, "@") {
		return "", "", false
	}
	rest := strings.TrimPrefix(id, "@")
	parts := strings.SplitN(rest, "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", false
	}
	return parts[0], parts[1], true
}

// EmbedText builds the embedding text for a warren editable-node write: the
// node's summary plus a bounded context snippet, matching the local write
// path's shape. It is the single formula shared by the HTTP API and MCP
// write paths.
func EmbedText(n *node.Node) string {
	embedText := n.Summary
	if n.Context != "" {
		ctxSnip := n.Context
		if len(ctxSnip) > 6000 {
			ctxSnip = ctxSnip[:6000]
		}
		embedText = n.Summary + "\n\n" + ctxSnip
	}
	return embedText
}

// WriteEditableNode persists n into an editable warren mount's checkout and,
// when vec is non-nil, upserts its embedding into the mount's own
// embeddings.db (an intentional read-write remote open — this IS the
// editable write path). It is the single write-back helper shared by the
// HTTP API and MCP so the two paths cannot diverge, and the enforcement
// point for future manifest write policy. The node save error is fatal; an
// embedding failure never rolls back the durable node write and is returned
// as a warning string instead, which callers must surface.
//
// When the mount lives inside a cache edit worktree (den edit link into a
// cache-backed warren, see editworktree.go) the node write is auto-committed
// on the worktree's edit branch, pathspec-limited to the node file (OQ7). A
// commit failure is a warning on the same channel, never an error — the node
// file is durably written either way. Legacy checkout mounts are never
// auto-committed (unchanged behavior: `warren propose` / `den contribute`
// package those edits).
func WriteEditableNode(mount ProjectStatus, n *node.Node, vec []float32, summaryHash, model string) (warning string, err error) {
	if err := WriteEditableNodeFile(mount, n); err != nil {
		return "", err
	}
	var warnings []string
	if w := autoCommitEditWrite(mount, n, "write"); w != "" {
		warnings = append(warnings, w)
	}
	if vec != nil {
		embStore, storeErr := embedding.NewStore(filepath.Join(mount.Path, ".marmot-data", "embeddings.db"))
		if storeErr != nil {
			warnings = append(warnings, "embedding not updated: "+storeErr.Error())
			return strings.Join(warnings, "; "), nil
		}
		// UpsertChecked, not Upsert: an editable mount writes into a store the
		// warren owns, and one mixed-model row would poison every future search
		// of it (F2). A refusal degrades to a warning naming both models — the
		// node file is already durably written.
		if upsertErr := embStore.UpsertChecked(n.ID, vec, summaryHash, model); upsertErr != nil {
			warnings = append(warnings, "embedding not updated: "+upsertErr.Error())
		}
		if closeErr := embStore.Close(); closeErr != nil {
			warnings = append(warnings, "embedding not updated: "+closeErr.Error())
		}
	}
	return strings.Join(warnings, "; "), nil
}

// WriteEditableNodeFile persists n into an editable warren mount's checkout
// WITHOUT touching the mount's embeddings.db. It carries the exact same
// write-policy enforcement as WriteEditableNode (editable-mount check plus
// the fail-closed manifest re-read below). It exists for flows whose durable
// artifact is the git-carried markdown alone — den contribute stages node
// files on an edit branch and the target's embeddings regenerate on consume
// (reindex/reembed after the PR merges), so upserting into the checked-out
// tree's live embeddings.db would corrupt the main branch's derived state
// with edit-branch-only rows.
func WriteEditableNodeFile(mount ProjectStatus, n *node.Node) error {
	if !mount.Editable {
		return fmt.Errorf("warren project %q is read-only in this workspace", mount.ProjectID)
	}
	// Author-side write-policy backstop: re-read the manifest at write time
	// so a stale mount state (editable flag granted before the author marked
	// the project readonly) cannot slip a write through. Unlike the read
	// paths this fails CLOSED — a write is an explicit action whose refusal
	// is visible, so an unreadable manifest refuses instead of degrading.
	// Statuses without a WarrenPath (built by older code or hand-rolled)
	// carry no policy source and skip the re-check.
	if mount.WarrenPath != "" {
		manifest, _, loadErr := LoadManifest(mount.WarrenPath)
		if loadErr != nil {
			return fmt.Errorf("warren manifest unreadable at %s; refusing write to project %q: %w", mount.WarrenPath, mount.ProjectID, loadErr)
		}
		for _, project := range manifest.Projects {
			if project.ProjectID == mount.ProjectID && project.ReadOnly {
				return fmt.Errorf("warren author marked project %q read-only; edits must go through the warren repository itself", mount.ProjectID)
			}
		}
	}
	store := node.NewStore(mount.Path)
	if err := store.SaveNode(n); err != nil {
		return fmt.Errorf("save warren node: %w", err)
	}
	return nil
}

// Status returns project statuses for one registered Warren.
func Status(workspaceRoot, warrenID string) ([]ProjectStatus, error) {
	state, _, err := LoadWorkspaceState(workspaceRoot)
	if err != nil {
		return nil, err
	}
	return statusFromState(workspaceMarmotDir(workspaceRoot), warrenID, state)
}

// ActiveMounts returns active warren project vaults plus identified-local
// projects (manifest projects whose vault_id matches the live workspace
// vault) for a local .marmot dir. Identified projects are served as the
// live vault: Path is the workspace's own .marmot, never routed, never
// editable/materialized (SelfAlias). Identity is derived per call — no
// mount, no verb, no workspace-state field — so an R1-era self entry in
// ActiveProjects produces the same identity-shaped status as a never-mounted
// identified project (one entry per warren/project, structurally deduped).
func ActiveMounts(marmotDir string) ([]ProjectStatus, error) {
	state, _, err := LoadWorkspaceStateFromMarmot(marmotDir)
	if err != nil {
		return nil, err
	}
	local := sourceVaultID(marmotDir)
	var mounts []ProjectStatus
	for warrenID, entry := range state.Warrens {
		manifest, _, err := LoadManifest(entry.Path)
		if err != nil {
			if entry.Materialized {
				// The burrow cache keeps the mounts alive without the source.
				// No manifest means no identity synthesis, but
				// materializedStatuses keeps its own SelfAlias computation: a
				// legacy self burrow cache surfacing here without the flag
				// would be routed by the reload loop, re-poisoning @local-id.
				mounts = append(mounts, materializedStatuses(marmotDir, warrenID, entry)...)
			} else {
				fmt.Fprintf(warnWriter, "warning: warren %q manifest unreadable at %s: %v (mounts skipped)\n", warrenID, entry.Path, err)
			}
			continue
		}
		activeSet := make(map[string]bool, len(entry.ActiveProjects))
		for _, projectID := range entry.ActiveProjects {
			activeSet[projectID] = true
		}
		for _, project := range manifest.Projects {
			isActive := activeSet[project.ProjectID]
			if !isActive && local == "" {
				continue // dormant and identity impossible: no metadata probe
			}
			marmotPath := preferredProjectPath(marmotDir, warrenID, entry, project)
			// Dormant projects are probed only for identity; their metadata
			// reads stay silent (probing 50 dormant projects must not emit 50
			// warnings). Active mounts keep the loud loader.
			var meta *ProjectMetadata
			if isActive {
				meta = loadProjectMetadataWarn(marmotPath, warrenID, project.ProjectID)
			} else {
				meta, _, _ = LoadProjectMetadata(marmotPath)
			}
			vaultID := project.ProjectID
			if meta != nil && meta.VaultID != "" {
				vaultID = meta.VaultID
			}
			selfAlias := local != "" && vaultID == local
			switch {
			case selfAlias:
				// Identified project: served as the live vault regardless of
				// any (redundant, R1-era) mount entry.
				mounts = append(mounts, ProjectStatus{
					WarrenID:   warrenID,
					WarrenPath: entry.Path,
					ProjectID:  project.ProjectID,
					Path:       marmotDir,
					VaultID:    local,
					Registered: true,
					Active:     true,
					Available:  true,
					SelfAlias:  true,
				})
			case isActive:
				mounts = append(mounts, ProjectStatus{
					WarrenID:   warrenID,
					WarrenPath: entry.Path,
					ProjectID:  project.ProjectID,
					Path:       marmotPath,
					VaultID:    vaultID,
					Registered: true,
					Active:     true,
					// Author-side readonly policy trumps the workspace's
					// editable flag (see Status).
					Editable:     containsName(entry.EditableProjects, project.ProjectID) && !project.ReadOnly,
					Materialized: entry.Materialized && marmotPath == materializedProjectPath(marmotDir, warrenID, project.ProjectID),
					Available:    dirExists(marmotPath),
				})
			}
		}
	}
	sort.Slice(mounts, func(i, j int) bool {
		if mounts[i].WarrenID == mounts[j].WarrenID {
			return mounts[i].ProjectID < mounts[j].ProjectID
		}
		return mounts[i].WarrenID < mounts[j].WarrenID
	})
	return mounts, nil
}

// Materialize copies a Warren project vault into the local workspace cache
// through the same hardened copier as import (secrets and DB sidecars
// excluded, symlinks and irregular files skipped, permissions narrowed to
// Perm bits). The copy goes to a temp sibling and is swapped in with a
// rename, so a failed burrow never leaves a half-written cache and a
// re-burrow never resurrects files deleted from the source.
//
// sourceCommit is the warren checkout's HEAD, resolved by the caller (this
// package stays exec-free, so it cannot ask git itself); pass "" for
// non-git warrens. It is pinned in the cache's provenance record so refresh
// --pull can skip up-to-date caches. A provenance write failure only warns:
// the cache itself is durable, and a missing/unreadable provenance is
// defined as "stale", so the failure mode is one extra re-copy.
func Materialize(workspaceMarmotDir, warrenID string, project Project, warrenRoot, sourceCommit string) (string, error) {
	source := filepath.Join(warrenRoot, filepath.FromSlash(project.Path))
	// Self-alias backstop: a burrow cache of the workspace's own vault would
	// be a stale shadow that the reload loop must never route. Mount already
	// refuses materialization on self-aliases; this guards the CLI's direct
	// Materialize calls (post-burrow loop, refresh --pull).
	if local := sourceVaultID(workspaceMarmotDir); local != "" {
		vaultID := project.ProjectID
		if meta, _, err := LoadProjectMetadata(source); err == nil && meta != nil && meta.VaultID != "" {
			vaultID = meta.VaultID
		}
		if vaultID == local {
			return "", fmt.Errorf("refusing to materialize project %q: vault ID %q is this workspace's own vault; a burrow cache would be a stale shadow of the live vault", project.ProjectID, vaultID)
		}
	}
	target := materializedProjectPath(workspaceMarmotDir, warrenID, project.ProjectID)
	// Flush the source DB's WAL so the sidecar-excluding copy is complete.
	checkpointEmbeddings(source)
	tmp := target + ".tmp"
	_ = os.RemoveAll(tmp)
	if err := copyFilteredTree(source, tmp, nil, skipBurrowFile, nil); err != nil {
		_ = os.RemoveAll(tmp)
		return "", err
	}
	if err := os.RemoveAll(target); err != nil {
		_ = os.RemoveAll(tmp)
		return "", fmt.Errorf("clear stale burrow cache: %w", err)
	}
	if err := os.Rename(tmp, target); err != nil {
		_ = os.RemoveAll(tmp)
		return "", fmt.Errorf("commit burrow cache: %w", err)
	}
	if err := SaveBurrowProvenance(workspaceMarmotDir, warrenID, project.ProjectID, newBurrowProvenance(project, sourceCommit)); err != nil {
		fmt.Fprintf(warnWriter, "warning: burrow cache for %s/%s committed but its provenance was not recorded: %v (the cache will be treated as stale by 'warren refresh --pull')\n", warrenID, project.ProjectID, err)
	}
	return target, nil
}

// loadProjectMetadataWarn loads project metadata, warning on stderr when the
// file exists but cannot be read or parsed (silent degradation would hide
// vault-ID resolution failures). A missing file is a normal state (project
// not imported through the warren flow) and stays silent.
func loadProjectMetadataWarn(marmotDir, warrenID, projectID string) *ProjectMetadata {
	meta, _, err := LoadProjectMetadata(marmotDir)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		fmt.Fprintf(warnWriter, "warning: warren %q project %q metadata unreadable at %s: %v\n", warrenID, projectID, marmotDir, err)
	}
	return meta
}

func materializedStatuses(workspaceMarmotDir, warrenID string, entry WorkspaceWarren) []ProjectStatus {
	local := sourceVaultID(workspaceMarmotDir)
	var statuses []ProjectStatus
	for _, projectID := range entry.ActiveProjects {
		cached := materializedProjectPath(workspaceMarmotDir, warrenID, projectID)
		meta := loadProjectMetadataWarn(cached, warrenID, projectID)
		vaultID := projectID
		if meta != nil && meta.VaultID != "" {
			vaultID = meta.VaultID
		}
		selfAlias := local != "" && vaultID == local
		statuses = append(statuses, ProjectStatus{
			WarrenID:     warrenID,
			WarrenPath:   entry.Path,
			ProjectID:    projectID,
			Path:         cached,
			VaultID:      vaultID,
			Registered:   meta != nil,
			Active:       true,
			Editable:     containsName(entry.EditableProjects, projectID) && !selfAlias,
			Materialized: dirExists(cached),
			Available:    dirExists(cached),
			SelfAlias:    selfAlias,
		})
	}
	sort.Slice(statuses, func(i, j int) bool { return statuses[i].ProjectID < statuses[j].ProjectID })
	return statuses
}

func workspaceMarmotDir(workspaceRoot string) string {
	return filepath.Join(workspaceRoot, MarmotDirName)
}

func preferredProjectPath(workspaceMarmotDir, warrenID string, entry WorkspaceWarren, project Project) string {
	checkout := filepath.Join(entry.Path, filepath.FromSlash(project.Path))
	// Editable always wins over a materialized cache: documented behavior is
	// that editable writes go to the project's own vault (the checkout). A
	// stale _warren.md carrying both flags (hand-edited, or written by an
	// old binary before the mount/edit refusal existed) must not silently
	// redirect writes into a cache that never syncs back.
	if containsName(entry.EditableProjects, project.ProjectID) {
		if entry.Materialized && dirExists(materializedProjectPath(workspaceMarmotDir, warrenID, project.ProjectID)) {
			fmt.Fprintf(warnWriter, "warning: warren %q project %q is both editable and materialized; using the checkout path %s (the materialized cache is ignored for editable projects)\n", warrenID, project.ProjectID, checkout)
		}
		return checkout
	}
	if entry.Materialized {
		cached := materializedProjectPath(workspaceMarmotDir, warrenID, project.ProjectID)
		if dirExists(cached) {
			return cached
		}
	}
	return checkout
}

func materializedProjectPath(workspaceMarmotDir, warrenID, projectID string) string {
	return filepath.Join(workspaceMarmotDir, ".marmot-data", "warrens", warrenID, "projects", projectID, MarmotDirName)
}

// ValidateProjectID checks that a Warren project ID is safe for YAML keys and paths.
func ValidateProjectID(id string) error {
	if id == "" {
		return fmt.Errorf("project ID must not be empty")
	}
	if len(id) > 128 {
		return fmt.Errorf("project ID %q exceeds maximum length of 128 characters", id)
	}
	if strings.ContainsRune(id, 0) {
		return fmt.Errorf("project ID %q contains null byte", id)
	}
	if strings.ContainsAny(id, `/\`) {
		return fmt.Errorf("project ID %q must not contain path separators", id)
	}
	if strings.HasPrefix(id, ".") || strings.HasPrefix(id, "_") || strings.Contains(id, "..") {
		return fmt.Errorf("project ID %q is not safe", id)
	}
	return nil
}

// ValidateWarrenID checks that a Warren ID is safe for state keys and cache paths.
func ValidateWarrenID(id string) error {
	if err := ValidateProjectID(id); err != nil {
		return fmt.Errorf("%s", strings.NewReplacer("project ID", "Warren ID").Replace(err.Error()))
	}
	return nil
}

// updateWorkspaceState runs a Load -> mutate -> Save cycle on the workspace
// _warren.md under an exclusive cross-process flock so concurrent mutations
// (mount/edit/register from separate marmot processes) cannot drop each
// other's writes.
func updateWorkspaceState(workspaceRoot string, fn func(*WorkspaceState) error) (*WorkspaceState, error) {
	statePath, err := workspaceStatePath(workspaceRoot)
	if err != nil {
		return nil, err
	}
	var state *WorkspaceState
	err = flock.WithLock(statePath+".lock", func() error {
		s, body, err := LoadWorkspaceState(workspaceRoot)
		if err != nil {
			return err
		}
		if err := fn(s); err != nil {
			return err
		}
		if err := SaveWorkspaceState(workspaceRoot, s, body); err != nil {
			return err
		}
		state = s
		return nil
	})
	if err != nil {
		return nil, err
	}
	return state, nil
}

// checkManifestWritable refuses to rewrite a manifest newer than this binary
// understands: struct-based parsing silently drops unknown YAML fields, so
// saving would strip them. Every mutating manifest path (updateManifest,
// import, init-on-existing) calls this after loading.
func checkManifestWritable(m *Manifest) error {
	if m.Version > CurrentManifestVersion {
		return fmt.Errorf("manifest version %d exceeds supported %d; upgrade marmot before editing this warren", m.Version, CurrentManifestVersion)
	}
	return nil
}

// updateManifest runs a LoadManifest -> mutate -> SaveManifest cycle on the
// Warren manifest under an exclusive cross-process flock (sibling
// _warren.md.lock file), preserving the markdown body. Every manifest
// mutation must go through this helper (or take the same lock) so concurrent
// warren CLI invocations cannot drop each other's updates.
func updateManifest(root string, fn func(*Manifest) error) (*Manifest, error) {
	path, err := manifestPath(root)
	if err != nil {
		return nil, err
	}
	var manifest *Manifest
	err = flock.WithLock(path+".lock", func() error {
		m, body, err := LoadManifest(root)
		if err != nil {
			return err
		}
		if err := checkManifestWritable(m); err != nil {
			return err
		}
		if err := fn(m); err != nil {
			return err
		}
		if err := SaveManifest(root, m, body); err != nil {
			return err
		}
		manifest = m
		return nil
	})
	if err != nil {
		return nil, err
	}
	return manifest, nil
}

func manifestPath(root string) (string, error) {
	if err := validateNonEmptyPath("Warren root", root); err != nil {
		return "", err
	}
	return filepath.Join(root, ManifestFileName), nil
}

func projectMetadataPath(marmotDir string) (string, error) {
	if err := validateNonEmptyPath("marmot dir", marmotDir); err != nil {
		return "", err
	}
	return filepath.Join(marmotDir, ManifestFileName), nil
}

func workspaceStatePath(root string) (string, error) {
	if err := validateNonEmptyPath("workspace root", root); err != nil {
		return "", err
	}
	return filepath.Join(root, MarmotDirName, ManifestFileName), nil
}

func validateNonEmptyPath(label, path string) error {
	if path == "" {
		return fmt.Errorf("%s must not be empty", label)
	}
	if strings.ContainsRune(path, 0) {
		return fmt.Errorf("%s contains null byte", label)
	}
	return nil
}

func parseMarkdownYAML(data []byte, out any) (string, error) {
	yamlBlock, body, err := frontmatter.Split(data)
	if err != nil {
		return "", err
	}
	if err := yaml.Unmarshal(yamlBlock, out); err != nil {
		return "", fmt.Errorf("unmarshal YAML: %w", err)
	}
	return body, nil
}

func writeMarkdownYAML(path string, in any, body string) error {
	yamlBytes, err := yaml.Marshal(in)
	if err != nil {
		return fmt.Errorf("marshal YAML: %w", err)
	}
	var buf strings.Builder
	buf.WriteString("---\n")
	buf.Write(yamlBytes)
	buf.WriteString("---\n")
	if body != "" {
		buf.WriteString(body)
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create Warren directory: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".warren-*.md.tmp")
	if err != nil {
		return fmt.Errorf("create Warren temp file: %w", err)
	}
	tmpPath := tmp.Name()
	success := false
	defer func() {
		if !success {
			_ = tmp.Close()
			_ = os.Remove(tmpPath)
		}
	}()
	if _, err := tmp.WriteString(buf.String()); err != nil {
		return fmt.Errorf("write Warren temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close Warren temp file: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("commit Warren file: %w", err)
	}
	success = true
	return nil
}

func defaultWorkspaceState() *WorkspaceState {
	return &WorkspaceState{Warrens: make(map[string]WorkspaceWarren)}
}

func normalizeManifest(m *Manifest) {
	if m.Version == 0 {
		m.Version = 1
	}
	// The readonly write policy is a version-2 field: bump so pre-v2
	// binaries refuse to rewrite (and silently strip) it.
	if m.Version < 2 {
		for _, project := range m.Projects {
			if project.ReadOnly {
				m.Version = 2
				break
			}
		}
	}
	// source_url/source_commit are version-3 fields: bump only when a
	// project actually carries them, so source-free manifests keep their
	// loaded version and stay editable by pre-v3 binaries.
	if m.Version < 3 {
		for _, project := range m.Projects {
			if project.SourceURL != "" || project.SourceCommit != "" {
				m.Version = 3
				break
			}
		}
	}
	m.Projects = uniqueProjects(m.Projects)
	for i := range m.Projects {
		m.Projects[i].Aliases = uniqueSorted(m.Projects[i].Aliases)
		m.Projects[i].Path = filepath.ToSlash(filepath.Clean(m.Projects[i].Path))
	}
	sort.Slice(m.Projects, func(i, j int) bool { return m.Projects[i].ProjectID < m.Projects[j].ProjectID })
	m.Bridges = uniqueBridges(m.Bridges)
}

func normalizeProjectMetadata(m *ProjectMetadata) {
	if m.VaultID == "" {
		m.VaultID = m.ProjectID
	}
	m.Aliases = uniqueSorted(m.Aliases)
}

func normalizeWorkspaceState(s *WorkspaceState) {
	if s.Warrens == nil {
		s.Warrens = make(map[string]WorkspaceWarren)
	}
	for id, entry := range s.Warrens {
		entry.ActiveProjects = uniqueSorted(entry.ActiveProjects)
		entry.EditableProjects = uniqueSorted(entry.EditableProjects)
		s.Warrens[id] = entry
	}
}

func validateManifest(root string, m *Manifest) error {
	if err := ValidateWarrenID(m.WarrenID); err != nil {
		return err
	}
	seen := make(map[string]bool, len(m.Projects))
	for _, project := range m.Projects {
		if err := ValidateProjectID(project.ProjectID); err != nil {
			return err
		}
		if seen[project.ProjectID] {
			return fmt.Errorf("duplicate project ID %q", project.ProjectID)
		}
		seen[project.ProjectID] = true
		if _, err := validateProjectPath(root, project.Path); err != nil {
			return err
		}
	}
	for _, bridge := range m.Bridges {
		if err := validateBridge(bridge); err != nil {
			return err
		}
	}
	return nil
}

func validateProjectMetadata(m *ProjectMetadata) error {
	if err := ValidateProjectID(m.ProjectID); err != nil {
		return err
	}
	if err := ValidateWarrenID(m.WarrenID); err != nil {
		return err
	}
	if err := ValidateProjectID(m.VaultID); err != nil {
		return err
	}
	return nil
}

func validateWorkspaceState(s *WorkspaceState) error {
	for warrenID, entry := range s.Warrens {
		if err := ValidateWarrenID(warrenID); err != nil {
			return err
		}
		if err := validateNonEmptyPath("Warren path", entry.Path); err != nil {
			return err
		}
		for _, project := range append(append([]string{}, entry.ActiveProjects...), entry.EditableProjects...) {
			if err := ValidateProjectID(project); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateProjectPath(base, projectPath string) (string, error) {
	if projectPath == "" {
		return "", fmt.Errorf("project path must not be empty")
	}
	if strings.ContainsRune(projectPath, 0) {
		return "", fmt.Errorf("project path %q contains null byte", projectPath)
	}
	clean := filepath.Clean(projectPath)
	if filepath.IsAbs(clean) {
		return clean, nil
	}
	if clean == "." || clean == ".." || strings.HasPrefix(filepath.ToSlash(clean), "../") {
		return "", fmt.Errorf("project path %q escapes Warren root", projectPath)
	}
	if base == "" {
		return clean, nil
	}
	absBase, err := filepath.Abs(base)
	if err != nil {
		return "", fmt.Errorf("resolve Warren root: %w", err)
	}
	absTarget, err := filepath.Abs(filepath.Join(absBase, clean))
	if err != nil {
		return "", fmt.Errorf("resolve project path: %w", err)
	}
	if absTarget != absBase && !strings.HasPrefix(absTarget, absBase+string(filepath.Separator)) {
		return "", fmt.Errorf("project path %q escapes Warren root", projectPath)
	}
	return clean, nil
}

// copyFilteredTree walks source and copies regular files to target. It is
// the single hardened copier shared by import and burrow:
//
//   - symlinks are never followed (skipped with a stderr note — a
//     symlink-to-dir would otherwise be walked as a plain file or escape the
//     tree);
//   - irregular files (FIFOs, sockets, devices) are skipped, so a FIFO can
//     never hang the copy in io.Copy;
//   - directories are created 0o755 and file permissions are copied via
//     Mode().Perm() only (no setuid/setgid/sticky propagation);
//   - skipDir/skipFile receive slash-separated paths relative to source;
//   - transformFile (optional, may be nil) lets a caller rewrite specific
//     files (import's _config.md sanitization); returning handled=true
//     suppresses the plain copy.
func copyFilteredTree(source, target string,
	skipDir func(relSlash string) bool,
	skipFile func(relSlash string) bool,
	transformFile func(relSlash, srcPath string, perm os.FileMode) (handled bool, err error),
) error {
	info, err := os.Stat(source)
	if err != nil {
		return fmt.Errorf("stat source: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("source %q is not a directory", source)
	}
	root := filepath.Clean(source)
	if err := os.MkdirAll(target, 0o755); err != nil {
		return fmt.Errorf("create copy target: %w", err)
	}
	return filepath.WalkDir(root, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == root {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		relSlash := filepath.ToSlash(rel)
		if d.Type()&os.ModeSymlink != 0 {
			fmt.Fprintf(warnWriter, "warning: skipping symlink %s\n", relSlash)
			return nil
		}
		if d.IsDir() {
			if skipDir != nil && skipDir(relSlash) {
				return filepath.SkipDir
			}
			return os.MkdirAll(filepath.Join(target, rel), 0o755)
		}
		if skipFile != nil && skipFile(relSlash) {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		dest := filepath.Join(target, rel)
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return err
		}
		if transformFile != nil {
			handled, err := transformFile(relSlash, path, info.Mode().Perm())
			if err != nil {
				return err
			}
			if handled {
				return nil
			}
		}
		return copyRegularFile(path, dest, info.Mode().Perm())
	})
}

// checkpointEmbeddings best-effort flushes the WAL of the source vault's
// embeddings DB so the sidecar-excluding copy that follows is complete: the
// copies skip -wal/-shm, and without a checkpoint any un-checkpointed writes
// would be silently lost from the copy. Only runs when a non-empty -wal
// sidecar exists (no WAL means nothing to flush and no reason to touch the
// file). Failure degrades to the documented point-in-time semantics (the
// last checkpoint), with a stderr warning.
func checkpointEmbeddings(sourceMarmotDir string) {
	dbPath := filepath.Join(sourceMarmotDir, ".marmot-data", "embeddings.db")
	if _, err := os.Stat(dbPath); err != nil {
		return // no DB, nothing to flush
	}
	walInfo, err := os.Stat(dbPath + "-wal")
	if err != nil || walInfo.Size() == 0 {
		return // no hot WAL, the main file is already complete
	}
	st, err := embedding.NewStore(dbPath)
	if err != nil {
		fmt.Fprintf(warnWriter, "warning: cannot open %s to checkpoint before copy: %v; copying last-checkpointed state\n", dbPath, err)
		return
	}
	defer func() { _ = st.Close() }()
	if err := st.Checkpoint(); err != nil {
		fmt.Fprintf(warnWriter, "warning: wal_checkpoint failed for %s: %v; copying last-checkpointed state\n", dbPath, err)
	}
}

var importAlwaysExcluded = map[string]bool{
	".marmot-data/.env":               true,
	".marmot-data/embeddings.db-wal":  true,
	".marmot-data/embeddings.db-shm":  true,
	".obsidian/workspace.json":        true,
	".obsidian/workspace-mobile.json": true,
}

func copyMarmotVault(source, target string, opts ImportOptions) error {
	return copyFilteredTree(source, target,
		func(relSlash string) bool { return shouldSkipImportDir(relSlash, opts) },
		func(relSlash string) bool { return shouldSkipImportFile(relSlash, opts) },
		func(relSlash, srcPath string, perm os.FileMode) (bool, error) {
			if relSlash != "_config.md" {
				return false, nil
			}
			data, err := sanitizedConfigBytes(srcPath)
			if err != nil {
				return false, fmt.Errorf("sanitize _config.md: %w", err)
			}
			return true, os.WriteFile(filepath.Join(target, "_config.md"), data, perm)
		})
}

// skipBurrowFile excludes secrets and DB sidecars from burrow copies. The
// checkout was already sanitized at import time, so no _config.md transform
// is applied — burrow stays byte-faithful for everything it copies.
func skipBurrowFile(relSlash string) bool {
	return importAlwaysExcluded[relSlash]
}

func shouldSkipImportDir(relSlash string, opts ImportOptions) bool {
	if opts.NoObsidian && (relSlash == ".obsidian" || strings.HasPrefix(relSlash, ".obsidian/")) {
		return true
	}
	if !opts.IncludeHeat && (relSlash == "_heat" || strings.HasPrefix(relSlash, "_heat/")) {
		return true
	}
	return false
}

func shouldSkipImportFile(relSlash string, opts ImportOptions) bool {
	if importAlwaysExcluded[relSlash] {
		return true
	}
	if opts.NoObsidian && strings.HasPrefix(relSlash, ".obsidian/") {
		return true
	}
	if !opts.IncludeHeat && strings.HasPrefix(relSlash, "_heat/") {
		return true
	}
	return false
}

func copyRegularFile(source, target string, mode os.FileMode) (err error) {
	src, err := os.Open(source)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := src.Close(); err == nil && closeErr != nil {
			err = closeErr
		}
	}()
	dst, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := dst.Close(); err == nil && closeErr != nil {
			err = closeErr
		}
	}()
	_, err = io.Copy(dst, src)
	return err
}

func sanitizedConfigBytes(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	out := map[string]any{}
	body, err := parseMarkdownYAML(data, &out)
	if err != nil {
		return sanitizePlainConfig(data), nil
	}
	out = sanitizeConfigMap(out)
	yamlBytes, err := yaml.Marshal(out)
	if err != nil {
		return nil, err
	}
	var buf strings.Builder
	buf.WriteString("---\n")
	buf.Write(yamlBytes)
	buf.WriteString("---\n")
	if body != "" {
		buf.WriteString(body)
	}
	return []byte(buf.String()), nil
}

func sanitizePlainConfig(data []byte) []byte {
	lines := strings.Split(string(data), "\n")
	out := lines[:0]
	for _, line := range lines {
		lower := strings.ToLower(line)
		value := strings.TrimSpace(line)
		if _, after, ok := strings.Cut(line, ":"); ok {
			value = strings.TrimSpace(after)
		}
		if isSecretConfigKey(lower) || looksLikeAPIKey(value) {
			continue
		}
		out = append(out, line)
	}
	return []byte(strings.Join(out, "\n"))
}

func sanitizeConfigMap(in map[string]any) map[string]any {
	out := make(map[string]any, len(in))
	for key, value := range in {
		if isSecretConfigKey(key) {
			continue
		}
		if sanitized, ok := sanitizeConfigValue(value); ok {
			out[key] = sanitized
		}
	}
	return out
}

func sanitizeConfigValue(value any) (any, bool) {
	switch typed := value.(type) {
	case string:
		if looksLikeAPIKey(strings.TrimSpace(typed)) {
			return nil, false
		}
		return typed, true
	case map[string]any:
		return sanitizeConfigMap(typed), true
	case map[any]any:
		out := make(map[string]any, len(typed))
		for rawKey, rawValue := range typed {
			key, ok := rawKey.(string)
			if !ok {
				continue
			}
			if isSecretConfigKey(key) {
				continue
			}
			if sanitized, ok := sanitizeConfigValue(rawValue); ok {
				out[key] = sanitized
			}
		}
		return out, true
	case []any:
		out := make([]any, 0, len(typed))
		for _, item := range typed {
			if sanitized, ok := sanitizeConfigValue(item); ok {
				out = append(out, sanitized)
			}
		}
		return out, true
	default:
		return value, true
	}
}

// LocalVaultID reads the live vault's vault_id from <marmotDir>/_config.md.
// It returns "" on a missing file, unparseable frontmatter, or an absent key —
// the single probe shared by the warren layer, cmd/marmot, and tests for the
// self-alias predicate (local != "" && vaultID == local).
func LocalVaultID(marmotDir string) string {
	return sourceVaultID(marmotDir)
}

func sourceVaultID(marmotDir string) string {
	data, err := os.ReadFile(filepath.Join(marmotDir, "_config.md"))
	if err != nil {
		return ""
	}
	out := map[string]any{}
	if _, err := parseMarkdownYAML(data, &out); err != nil {
		return ""
	}
	if value, ok := out["vault_id"].(string); ok {
		return strings.TrimSpace(value)
	}
	return ""
}

func isSecretConfigKey(key string) bool {
	lower := strings.ToLower(key)
	switch lower {
	case "openai_api_key", "anthropic_api_key", "voyage_api_key":
		return true
	}
	if strings.Contains(lower, "api_key") || strings.Contains(lower, "secret") || strings.Contains(lower, "password") {
		return true
	}
	return strings.Contains(lower, "token") && !strings.Contains(lower, "budget")
}

func looksLikeAPIKey(value string) bool {
	if len(value) < 20 {
		return false
	}
	for _, prefix := range []string{"sk-", "sk_", "pk-", "pk_", "Bearer ", "voyage-"} {
		if strings.HasPrefix(value, prefix) {
			return true
		}
	}
	return false
}

func samePath(a, b string) bool {
	absA, errA := filepath.Abs(a)
	absB, errB := filepath.Abs(b)
	if errA != nil || errB != nil {
		return filepath.Clean(a) == filepath.Clean(b)
	}
	return absA == absB
}

func pathContains(parent, child string) bool {
	parentAbs, err := filepath.Abs(parent)
	if err != nil {
		parentAbs = filepath.Clean(parent)
	}
	childAbs, err := filepath.Abs(child)
	if err != nil {
		childAbs = filepath.Clean(child)
	}
	parentAbs = filepath.Clean(parentAbs)
	childAbs = filepath.Clean(childAbs)
	if parentAbs == childAbs {
		return true
	}
	rel, err := filepath.Rel(parentAbs, childAbs)
	if err != nil {
		return false
	}
	return rel != "." && rel != ".." && !strings.HasPrefix(filepath.ToSlash(rel), "../")
}

func ensureImportParent(rootAbs, rootReal, parent string) (string, error) {
	if !pathContains(rootAbs, parent) {
		return "", fmt.Errorf("destination parent %q escapes Warren root", parent)
	}
	rel, err := filepath.Rel(rootAbs, parent)
	if err != nil {
		return "", fmt.Errorf("resolve destination parent: %w", err)
	}
	current := rootAbs
	if rel != "." {
		for _, part := range strings.Split(filepath.ToSlash(rel), "/") {
			if part == "" || part == "." {
				continue
			}
			current = filepath.Join(current, filepath.FromSlash(part))
			info, err := os.Lstat(current)
			if errors.Is(err, os.ErrNotExist) {
				break
			}
			if err != nil {
				return "", fmt.Errorf("stat destination parent: %w", err)
			}
			if info.Mode()&os.ModeSymlink != 0 {
				real, err := filepath.EvalSymlinks(current)
				if err != nil {
					return "", fmt.Errorf("resolve destination parent symlink: %w", err)
				}
				if !pathContains(rootReal, real) {
					return "", fmt.Errorf("destination parent %q escapes Warren root through symlink", current)
				}
				continue
			}
			if !info.IsDir() {
				return "", fmt.Errorf("destination parent %q is not a directory", current)
			}
		}
	}
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return "", fmt.Errorf("create destination parent: %w", err)
	}
	parentReal, err := filepath.EvalSymlinks(parent)
	if err != nil {
		return "", fmt.Errorf("resolve destination parent symlinks: %w", err)
	}
	if !pathContains(rootReal, parentReal) {
		return "", fmt.Errorf("destination parent %q escapes Warren root through symlink", parent)
	}
	return parentReal, nil
}

func dirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

func addName(names []string, name string) []string {
	if containsName(names, name) {
		return uniqueSorted(names)
	}
	return uniqueSorted(append(names, name))
}

func removeName(names []string, name string) []string {
	out := names[:0]
	for _, candidate := range names {
		if candidate != name {
			out = append(out, candidate)
		}
	}
	return uniqueSorted(out)
}

func containsName(names []string, name string) bool {
	for _, candidate := range names {
		if candidate == name {
			return true
		}
	}
	return false
}

func uniqueSorted(names []string) []string {
	if len(names) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(names))
	out := make([]string, 0, len(names))
	for _, name := range names {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

func removeNames(names []string, removals []string) []string {
	remove := make(map[string]struct{}, len(removals))
	for _, name := range removals {
		remove[name] = struct{}{}
	}
	out := names[:0]
	for _, name := range names {
		if _, ok := remove[name]; !ok {
			out = append(out, name)
		}
	}
	return uniqueSorted(out)
}

func uniqueProjects(projects []Project) []Project {
	seen := make(map[string]Project, len(projects))
	for _, project := range projects {
		project.ProjectID = strings.TrimSpace(project.ProjectID)
		if project.ProjectID == "" {
			continue
		}
		seen[project.ProjectID] = project
	}
	out := make([]Project, 0, len(seen))
	for _, project := range seen {
		out = append(out, project)
	}
	return out
}

func uniqueBridges(bridges []Bridge) []Bridge {
	seen := make(map[string]Bridge, len(bridges))
	for _, bridge := range bridges {
		normalizeBridge(&bridge)
		if bridge.Source == "" || bridge.Target == "" || len(bridge.Relations) == 0 {
			continue
		}
		key := bridge.Source + "\x00" + bridge.Target
		if existing, ok := seen[key]; ok {
			bridge.Relations = uniqueSorted(append(existing.Relations, bridge.Relations...))
		}
		seen[key] = bridge
	}
	out := make([]Bridge, 0, len(seen))
	for _, bridge := range seen {
		out = append(out, bridge)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Source == out[j].Source {
			return out[i].Target < out[j].Target
		}
		return out[i].Source < out[j].Source
	})
	return out
}

func normalizeBridge(bridge *Bridge) {
	bridge.Source = strings.TrimSpace(bridge.Source)
	bridge.Target = strings.TrimSpace(bridge.Target)
	bridge.Relations = uniqueSorted(bridge.Relations)
}

func defaultProjectPaths(m *Manifest) {
	for i := range m.Projects {
		if m.Projects[i].ProjectID != "" && m.Projects[i].Path == "" {
			m.Projects[i].Path = defaultProjectPath(m.Projects[i].ProjectID)
		}
	}
}

func validateBridge(bridge Bridge) error {
	if err := ValidateProjectID(bridge.Source); err != nil {
		return fmt.Errorf("bridge source: %w", err)
	}
	if err := ValidateProjectID(bridge.Target); err != nil {
		return fmt.Errorf("bridge target: %w", err)
	}
	if len(bridge.Relations) == 0 {
		return fmt.Errorf("bridge %q -> %q must include at least one relation", bridge.Source, bridge.Target)
	}
	for _, relation := range bridge.Relations {
		if err := validateRelation(relation); err != nil {
			return err
		}
	}
	return nil
}

func validateRelation(relation string) error {
	if relation == "" {
		return fmt.Errorf("bridge relation must not be empty")
	}
	switch node.EdgeRelation(relation) {
	case node.Contains, node.Imports, node.Extends, node.Implements,
		node.Calls, node.Reads, node.Writes, node.References,
		node.CrossProject, node.Associated:
		return nil
	default:
		return fmt.Errorf("bridge relation %q is not a known edge relation", relation)
	}
}

func projectSet(projects []Project) map[string]bool {
	known := make(map[string]bool, len(projects))
	for _, project := range projects {
		known[project.ProjectID] = true
	}
	return known
}

func defaultProjectPath(projectID string) string {
	return filepath.ToSlash(filepath.Join("projects", projectID, MarmotDirName))
}

func generateProjectIDFromPath(path string) string {
	clean := filepath.Clean(path)
	if filepath.Base(clean) == MarmotDirName {
		return GenerateProjectID(filepath.Base(filepath.Dir(clean)))
	}
	return GenerateProjectID(filepath.Base(clean))
}

func ensureProjectMetadata(root, warrenID string, project Project) error {
	marmotDir := filepath.Join(root, filepath.FromSlash(project.Path))
	meta, body, err := LoadProjectMetadata(marmotDir)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		meta = &ProjectMetadata{VaultID: project.ProjectID}
	}
	meta.ProjectID = project.ProjectID
	meta.WarrenID = warrenID
	meta.Aliases = uniqueSorted(append(meta.Aliases, project.Aliases...))
	return SaveProjectMetadata(marmotDir, meta, body)
}

func preflightProjectMetadata(root, warrenID string, project Project) error {
	marmotDir := filepath.Join(root, filepath.FromSlash(project.Path))
	meta, _, err := LoadProjectMetadata(marmotDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	meta.ProjectID = project.ProjectID
	meta.WarrenID = warrenID
	meta.Aliases = uniqueSorted(append(meta.Aliases, project.Aliases...))
	return validateProjectMetadata(meta)
}

// ---------------------------------------------------------------------------
// Direct-state (marmot-dir-keyed) workspace state API.
//
// Classic workspaces keep their warren state at <root>/.marmot/_warren.md and
// address it by workspace root; direct-state marmot dirs (den identity
// vaults: they carry _config.md but are not named .marmot) keep it at
// <marmotDir>/_warren.md — the file warren.ActiveMounts reads. The *FromMarmot
// entry points below run the exact same flock-held mutation bodies as their
// workspace-root twins, just keyed by the marmot dir, so the two shapes can
// never diverge in behavior. For a classic <root>/.marmot dir both address the
// same file with the same lock.
// ---------------------------------------------------------------------------

// updateWorkspaceStateFromMarmot mirrors updateWorkspaceState (always-save
// load→mutate→save under the state flock) for state kept directly at
// <marmotDir>/_warren.md.
func updateWorkspaceStateFromMarmot(marmotDir string, fn func(*WorkspaceState) error) (*WorkspaceState, error) {
	var out *WorkspaceState
	err := UpdateWorkspaceStateInMarmot(marmotDir, func(state *WorkspaceState) (bool, error) {
		if err := fn(state); err != nil {
			return false, err
		}
		out = state
		return true, nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// MountFromMarmot is Mount for direct-state marmot dirs.
func MountFromMarmot(marmotDir, warrenID string, projects []string, materialized bool) (*WorkspaceState, error) {
	return updateWorkspaceStateFromMarmot(marmotDir, func(state *WorkspaceState) error {
		return applyMount(marmotDir, state, warrenID, projects, materialized)
	})
}

// SetEditableFromMarmot is SetEditable for direct-state marmot dirs.
func SetEditableFromMarmot(marmotDir, warrenID, projectID string, editable bool) (*WorkspaceState, error) {
	return updateWorkspaceStateFromMarmot(marmotDir, func(state *WorkspaceState) error {
		return applySetEditable(marmotDir, state, warrenID, projectID, editable)
	})
}

// UnmountFromMarmot is Unmount for direct-state marmot dirs.
func UnmountFromMarmot(marmotDir, warrenID string, projects []string) (*WorkspaceState, error) {
	return updateWorkspaceStateFromMarmot(marmotDir, func(state *WorkspaceState) error {
		return applyUnmount(marmotDir, state, warrenID, projects)
	})
}

// StatusFromMarmot is Status for direct-state marmot dirs.
func StatusFromMarmot(marmotDir, warrenID string) ([]ProjectStatus, error) {
	state, _, err := LoadWorkspaceStateFromMarmot(marmotDir)
	if err != nil {
		return nil, err
	}
	return statusFromState(marmotDir, warrenID, state)
}

// applyMount is Mount's flock-held mutation body, shared by the
// workspace-root and direct-state entry points. wsMarmotDir is the vault dir
// that owns caches, config, and vault-ID claims.
func applyMount(wsMarmotDir string, state *WorkspaceState, warrenID string, projects []string, materialized bool) error {
	local := sourceVaultID(wsMarmotDir)
	entry, ok := state.Warrens[warrenID]
	if !ok {
		return fmt.Errorf("warren %q is not registered in this workspace", warrenID)
	}
	manifest, _, err := LoadManifest(entry.Path)
	if err != nil {
		return err
	}
	registered := make(map[string]Project, len(manifest.Projects))
	for _, project := range manifest.Projects {
		registered[project.ProjectID] = project
	}
	claimed := claimedVaultIDs(wsMarmotDir, state)
	for _, projectID := range projects {
		if err := ValidateProjectID(projectID); err != nil {
			return err
		}
		project, known := registered[projectID]
		if !known {
			return fmt.Errorf("project %q is not registered in Warren %q", projectID, warrenID)
		}
		// A materialized (burrowed) cache never syncs edits back to the
		// checkout, so materializing an editable project would silently
		// strand its future edits in the cache.
		if materialized && containsName(entry.EditableProjects, projectID) {
			return fmt.Errorf("project %q in warren %q is editable; a materialized cache never syncs edits back — disable editing first ('marmot warren edit %s --warren %s --off') or use 'marmot warren mount' instead of burrow", projectID, warrenID, projectID, warrenID)
		}
		// Vault IDs form one flat routing namespace per workspace: a
		// duplicate would be resolved last-mount-wins at runtime, silently
		// answering queries from the wrong project. Refuse at mount time.
		vaultID := mountVaultID(wsMarmotDir, warrenID, entry, project)
		if local != "" && vaultID == local {
			// Identified project: this project IS the workspace vault.
			// Identity is derived from vault_id, always on, and stateless —
			// mounting records nothing (bridges involving it activate
			// without a mount). It can never be editable or materialized
			// (a cache/copy would be a stale or split-brained shadow).
			if materialized {
				return fmt.Errorf("project %q in warren %q has this workspace's own vault ID %q; a self-alias serves from the live vault and cannot be materialized — use 'marmot warren mount' instead of burrow", projectID, warrenID, vaultID)
			}
			if containsName(entry.EditableProjects, projectID) {
				return fmt.Errorf("project %q in warren %q is marked editable but has this workspace's own vault ID %q; edit it directly in this workspace — run 'marmot warren edit %s --warren %s --off' first", projectID, warrenID, vaultID, projectID, warrenID)
			}
			fmt.Fprintf(warnWriter, "note: project %s/%s IS this workspace (vault ID %q); identity is automatic — bridges involving it activate without a mount\n", warrenID, projectID, vaultID)
			continue // no state write, no vault-ID claim: identity is derived, not mounted
		}
		if err := refuseVaultIDCollision(claimed, vaultID, warrenID, projectID); err != nil {
			return err
		}
		claimed[vaultID] = vaultClaim{WarrenID: warrenID, ProjectID: projectID}
		entry.ActiveProjects = addName(entry.ActiveProjects, projectID)
	}
	if materialized {
		entry.Materialized = true
	}
	state.Warrens[warrenID] = entry
	return nil
}

// applySetEditable is SetEditable's flock-held mutation body, shared by the
// workspace-root and direct-state entry points.
func applySetEditable(wsMarmotDir string, state *WorkspaceState, warrenID, projectID string, editable bool) error {
	entry, ok := state.Warrens[warrenID]
	if !ok {
		return fmt.Errorf("warren %q is not registered in this workspace", warrenID)
	}
	if err := ValidateProjectID(projectID); err != nil {
		return err
	}
	manifest, _, err := LoadManifest(entry.Path)
	if err != nil {
		return err
	}
	var project Project
	known := false
	for _, p := range manifest.Projects {
		if p.ProjectID == projectID {
			project, known = p, true
			break
		}
	}
	if !known {
		return fmt.Errorf("project %q is not registered in Warren %q", projectID, warrenID)
	}
	// Author-side write policy (manifest schema v2): the warren owner
	// marked the project read-only, so no workspace may enable edit.
	// Disabling (--off) stays allowed regardless.
	if editable && project.ReadOnly {
		return fmt.Errorf("warren author marked project %q read-only; edits must go through the warren repository itself", projectID)
	}
	// Self-alias (vault_id matches the live local vault): the mount is a
	// read-through view of the live vault, so editable would split-brain
	// writes into the warren checkout. Disabling (--off) stays allowed —
	// it is the legacy-state escape hatch.
	vaultID := mountVaultID(wsMarmotDir, warrenID, entry, project)
	local := sourceVaultID(wsMarmotDir)
	selfAlias := local != "" && vaultID == local
	if selfAlias && editable {
		return fmt.Errorf("project %q in warren %q has this workspace's own vault ID %q; it is served as an alias of the live vault — edit nodes directly in this workspace (no @ prefix) instead of enabling warren edit", projectID, warrenID, vaultID)
	}
	// Refuse editable on a burrowed project: edits would land in the
	// materialized cache and never sync back to the checkout, while
	// `warren propose` tells the user to commit a checkout that never
	// received them. Materialized is warren-wide, so the per-project
	// ground truth is the existence of the burrow cache dir. Disabling
	// (--off) stays allowed regardless.
	if editable && entry.Materialized {
		cached := materializedProjectPath(wsMarmotDir, warrenID, projectID)
		if dirExists(cached) {
			return fmt.Errorf("project %q in warren %q is materialized (burrowed); a materialized cache never syncs edits back — drop the burrow first ('marmot warren burrow --drop --warren %s %s') before enabling edit", projectID, warrenID, warrenID, projectID)
		}
	}
	// Edit implies mount: when the project is not yet active this is an
	// auto-mount, so it gets the same vault-ID collision refusal as Mount.
	// An identified project skips BOTH the collision check and the mount
	// record: there is nothing to mount (identity is derived from
	// vault_id), so --off just clears any legacy EditableProjects entry
	// without re-recording R1-era self state.
	if !containsName(entry.ActiveProjects, projectID) && !selfAlias {
		claimed := claimedVaultIDs(wsMarmotDir, state)
		if err := refuseVaultIDCollision(claimed, vaultID, warrenID, projectID); err != nil {
			return err
		}
	}
	if !selfAlias {
		entry.ActiveProjects = addName(entry.ActiveProjects, projectID)
	}
	if editable {
		entry.EditableProjects = addName(entry.EditableProjects, projectID)
	} else {
		entry.EditableProjects = removeName(entry.EditableProjects, projectID)
	}
	state.Warrens[warrenID] = entry
	return nil
}

// applyUnmount is Unmount's flock-held mutation body, shared by the
// workspace-root and direct-state entry points.
func applyUnmount(wsMarmotDir string, state *WorkspaceState, warrenID string, projects []string) error {
	entry, ok := state.Warrens[warrenID]
	if !ok {
		return fmt.Errorf("warren %q is not registered in this workspace", warrenID)
	}
	for _, project := range projects {
		if err := ValidateProjectID(project); err != nil {
			return err
		}
		if !containsName(entry.ActiveProjects, project) {
			if identifiedProject(wsMarmotDir, warrenID, entry, project) {
				return fmt.Errorf("project %q is not mounted from warren %q in this workspace (identity is derived from vault_id, not a mount — to sever it, re-import the warren copy with a distinct --vault-id)", project, warrenID)
			}
			return fmt.Errorf("project %q is not mounted from warren %q in this workspace", project, warrenID)
		}
	}
	entry.ActiveProjects = removeNames(entry.ActiveProjects, projects)
	entry.EditableProjects = removeNames(entry.EditableProjects, projects)
	state.Warrens[warrenID] = entry
	return nil
}

// statusFromState is Status's row builder, shared by the workspace-root and
// direct-state entry points.
func statusFromState(wsMarmotDir, warrenID string, state *WorkspaceState) ([]ProjectStatus, error) {
	entry, ok := state.Warrens[warrenID]
	if !ok {
		return nil, fmt.Errorf("warren %q is not registered in this workspace", warrenID)
	}
	manifest, _, err := LoadManifest(entry.Path)
	if err != nil {
		if entry.Materialized {
			return materializedStatuses(wsMarmotDir, warrenID, entry), nil
		}
		// The checkout is unreachable (moved, deleted, or unparseable):
		// degrade to rows built from workspace state instead of erroring
		// opaquely, so `warren status` can still show what is mounted and
		// the unmount escape hatch keeps working.
		fmt.Fprintf(warnWriter, "warning: warren %q manifest unreadable at %s: %v (status degraded to workspace state)\n", warrenID, entry.Path, err)
		statuses := make([]ProjectStatus, 0, len(entry.ActiveProjects))
		for _, projectID := range entry.ActiveProjects {
			statuses = append(statuses, ProjectStatus{
				WarrenID:   warrenID,
				WarrenPath: entry.Path,
				ProjectID:  projectID,
				Path:       filepath.Join(entry.Path, filepath.FromSlash(defaultProjectPath(projectID))),
				Registered: false,
				Active:     true,
				Editable:   containsName(entry.EditableProjects, projectID),
				Available:  false,
			})
		}
		return statuses, nil
	}
	local := sourceVaultID(wsMarmotDir)
	projects := make([]ProjectStatus, 0, len(manifest.Projects))
	for _, project := range manifest.Projects {
		sourceDir := filepath.Join(entry.Path, filepath.FromSlash(project.Path))
		marmotDir := preferredProjectPath(wsMarmotDir, warrenID, entry, project)
		meta := loadProjectMetadataWarn(marmotDir, warrenID, project.ProjectID)
		if meta == nil && marmotDir != sourceDir {
			meta = loadProjectMetadataWarn(sourceDir, warrenID, project.ProjectID)
		}
		vaultID := ""
		if meta != nil {
			vaultID = meta.VaultID
		}
		selfAlias := local != "" && vaultID == local
		_, statErr := os.Stat(marmotDir)
		available := statErr == nil
		if selfAlias {
			// Identified project: it is served as the live workspace vault, so
			// the checkout path would be a lie — report where reads actually go.
			marmotDir = wsMarmotDir
			available = true
		}
		projects = append(projects, ProjectStatus{
			WarrenID:   warrenID,
			WarrenPath: entry.Path,
			ProjectID:  project.ProjectID,
			Path:       marmotDir,
			VaultID:    vaultID,
			Registered: true,
			Active:     containsName(entry.ActiveProjects, project.ProjectID),
			// Author-side readonly policy trumps the workspace's editable
			// flag, so the UI save button and MCP/API rejections follow the
			// manifest without any client change; a self-alias is never
			// editable (writes go to the live vault).
			Editable:     containsName(entry.EditableProjects, project.ProjectID) && !project.ReadOnly && !selfAlias,
			Materialized: entry.Materialized && dirExists(materializedProjectPath(wsMarmotDir, warrenID, project.ProjectID)),
			Available:    available,
			SelfAlias:    selfAlias,
		})
	}
	sort.Slice(projects, func(i, j int) bool { return projects[i].ProjectID < projects[j].ProjectID })
	return projects, nil
}
