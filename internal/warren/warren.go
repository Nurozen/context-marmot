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
	"gopkg.in/yaml.v3"
)

const (
	ManifestFileName = "_warren.md"
	MarmotDirName    = ".marmot"
)

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
	WarrenID     string `json:"warren_id"`
	ProjectID    string `json:"project_id"`
	Path         string `json:"path"`
	VaultID      string `json:"vault_id,omitempty"`
	Registered   bool   `json:"registered"`
	Active       bool   `json:"active"`
	Editable     bool   `json:"editable"`
	Materialized bool   `json:"materialized"`
	Available    bool   `json:"available"`
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

// RenameProject renames a project ID in the manifest, project metadata, and bridges.
func RenameProject(root, oldID, newID string) (*Manifest, error) {
	if err := ValidateProjectID(oldID); err != nil {
		return nil, err
	}
	if err := ValidateProjectID(newID); err != nil {
		return nil, err
	}
	if oldID == newID {
		return nil, fmt.Errorf("new project ID must differ from old project ID")
	}
	var renamed Project
	manifest, err := updateManifest(root, func(manifest *Manifest) error {
		found := false
		for i := range manifest.Projects {
			if manifest.Projects[i].ProjectID == newID {
				return fmt.Errorf("project %q already exists", newID)
			}
			if manifest.Projects[i].ProjectID == oldID {
				found = true
				manifest.Projects[i].ProjectID = newID
				renamed = manifest.Projects[i]
			}
		}
		if !found {
			return fmt.Errorf("project %q does not exist", oldID)
		}
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
	return manifest, nil
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
	if dirExists(filepath.Join(root, ".marmot-data", "warrens")) {
		report.Issues = append(report.Issues, DoctorIssue{
			Severity: "warning",
			Code:     "materialized_cache_in_warren",
			Message:  "materialized Warren cache path exists inside the Warren repository",
			Path:     ".marmot-data/warrens",
		})
	}
	for _, project := range manifest.Projects {
		projectPath := filepath.Join(root, filepath.FromSlash(project.Path))
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
		if _, err := os.Stat(filepath.Join(projectPath, ".marmot-data", "embeddings.db")); err != nil {
			report.Issues = append(report.Issues, DoctorIssue{
				Severity:  "warning",
				Code:      "embeddings_missing",
				Message:   fmt.Sprintf("project %q has no embedding database; run indexing before relying on semantic search", project.ProjectID),
				ProjectID: project.ProjectID,
				Path:      project.Path,
			})
		}
	}
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
	return updateWorkspaceState(workspaceRoot, func(state *WorkspaceState) error {
		entry, ok := state.Warrens[warrenID]
		if !ok {
			return fmt.Errorf("warren %q is not registered in this workspace", warrenID)
		}
		known, err := registeredProjectSet(entry.Path)
		if err != nil {
			return err
		}
		for _, project := range projects {
			if err := ValidateProjectID(project); err != nil {
				return err
			}
			if !known[project] {
				return fmt.Errorf("project %q is not registered in Warren %q", project, warrenID)
			}
			// A materialized (burrowed) cache never syncs edits back to the
			// checkout, so materializing an editable project would silently
			// strand its future edits in the cache.
			if materialized && containsName(entry.EditableProjects, project) {
				return fmt.Errorf("project %q in warren %q is editable; a materialized cache never syncs edits back — disable editing first ('marmot warren edit %s --warren %s --off') or mount without --materialize", project, warrenID, project, warrenID)
			}
			entry.ActiveProjects = addName(entry.ActiveProjects, project)
		}
		if materialized {
			entry.Materialized = true
		}
		state.Warrens[warrenID] = entry
		return nil
	})
}

// SetEditable toggles a single project's local writable state.
func SetEditable(workspaceRoot, warrenID, projectID string, editable bool) (*WorkspaceState, error) {
	return updateWorkspaceState(workspaceRoot, func(state *WorkspaceState) error {
		entry, ok := state.Warrens[warrenID]
		if !ok {
			return fmt.Errorf("warren %q is not registered in this workspace", warrenID)
		}
		if err := ValidateProjectID(projectID); err != nil {
			return err
		}
		known, err := registeredProjectSet(entry.Path)
		if err != nil {
			return err
		}
		if !known[projectID] {
			return fmt.Errorf("project %q is not registered in Warren %q", projectID, warrenID)
		}
		// Refuse editable on a burrowed project: edits would land in the
		// materialized cache and never sync back to the checkout, while
		// `warren propose` tells the user to commit a checkout that never
		// received them. Materialized is warren-wide, so the per-project
		// ground truth is the existence of the burrow cache dir. Disabling
		// (--off) stays allowed regardless.
		if editable && entry.Materialized {
			cached := materializedProjectPath(workspaceMarmotDir(workspaceRoot), warrenID, projectID)
			if dirExists(cached) {
				return fmt.Errorf("project %q in warren %q is materialized (burrowed); a materialized cache never syncs edits back — delete the burrow cache at %s or re-mount without --materialize before enabling edit", projectID, warrenID, cached)
			}
		}
		entry.ActiveProjects = addName(entry.ActiveProjects, projectID)
		if editable {
			entry.EditableProjects = addName(entry.EditableProjects, projectID)
		} else {
			entry.EditableProjects = removeName(entry.EditableProjects, projectID)
		}
		state.Warrens[warrenID] = entry
		return nil
	})
}

// Status returns project statuses for one registered Warren.
func Status(workspaceRoot, warrenID string) ([]ProjectStatus, error) {
	state, _, err := LoadWorkspaceState(workspaceRoot)
	if err != nil {
		return nil, err
	}
	entry, ok := state.Warrens[warrenID]
	if !ok {
		return nil, fmt.Errorf("warren %q is not registered in this workspace", warrenID)
	}
	manifest, _, err := LoadManifest(entry.Path)
	if err != nil {
		if entry.Materialized {
			return materializedStatuses(workspaceMarmotDir(workspaceRoot), warrenID, entry), nil
		}
		return nil, err
	}
	projects := make([]ProjectStatus, 0, len(manifest.Projects))
	for _, project := range manifest.Projects {
		sourceDir := filepath.Join(entry.Path, filepath.FromSlash(project.Path))
		marmotDir := preferredProjectPath(workspaceMarmotDir(workspaceRoot), warrenID, entry, project)
		meta := loadProjectMetadataWarn(marmotDir, warrenID, project.ProjectID)
		if meta == nil && marmotDir != sourceDir {
			meta = loadProjectMetadataWarn(sourceDir, warrenID, project.ProjectID)
		}
		vaultID := ""
		if meta != nil {
			vaultID = meta.VaultID
		}
		_, statErr := os.Stat(marmotDir)
		projects = append(projects, ProjectStatus{
			WarrenID:     warrenID,
			ProjectID:    project.ProjectID,
			Path:         marmotDir,
			VaultID:      vaultID,
			Registered:   true,
			Active:       containsName(entry.ActiveProjects, project.ProjectID),
			Editable:     containsName(entry.EditableProjects, project.ProjectID),
			Materialized: entry.Materialized && dirExists(materializedProjectPath(workspaceMarmotDir(workspaceRoot), warrenID, project.ProjectID)),
			Available:    statErr == nil,
		})
	}
	sort.Slice(projects, func(i, j int) bool { return projects[i].ProjectID < projects[j].ProjectID })
	return projects, nil
}

// ActiveMounts returns active Warren project vaults for a local .marmot dir.
func ActiveMounts(marmotDir string) ([]ProjectStatus, error) {
	state, _, err := LoadWorkspaceStateFromMarmot(marmotDir)
	if err != nil {
		return nil, err
	}
	var mounts []ProjectStatus
	for warrenID, entry := range state.Warrens {
		manifest, _, err := LoadManifest(entry.Path)
		if err != nil {
			if entry.Materialized {
				// The burrow cache keeps the mounts alive without the source.
				mounts = append(mounts, materializedStatuses(marmotDir, warrenID, entry)...)
			} else {
				fmt.Fprintf(warnWriter, "warning: warren %q manifest unreadable at %s: %v (mounts skipped)\n", warrenID, entry.Path, err)
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
			marmotPath := preferredProjectPath(marmotDir, warrenID, entry, project)
			meta := loadProjectMetadataWarn(marmotPath, warrenID, projectID)
			vaultID := projectID
			if meta != nil && meta.VaultID != "" {
				vaultID = meta.VaultID
			}
			mounts = append(mounts, ProjectStatus{
				WarrenID:     warrenID,
				ProjectID:    projectID,
				Path:         marmotPath,
				VaultID:      vaultID,
				Registered:   true,
				Active:       true,
				Editable:     containsName(entry.EditableProjects, projectID),
				Materialized: entry.Materialized && marmotPath == materializedProjectPath(marmotDir, warrenID, projectID),
				Available:    dirExists(marmotPath),
			})
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
func Materialize(workspaceMarmotDir, warrenID string, project Project, warrenRoot string) (string, error) {
	source := filepath.Join(warrenRoot, filepath.FromSlash(project.Path))
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
	var statuses []ProjectStatus
	for _, projectID := range entry.ActiveProjects {
		cached := materializedProjectPath(workspaceMarmotDir, warrenID, projectID)
		meta := loadProjectMetadataWarn(cached, warrenID, projectID)
		vaultID := projectID
		if meta != nil && meta.VaultID != "" {
			vaultID = meta.VaultID
		}
		statuses = append(statuses, ProjectStatus{
			WarrenID:     warrenID,
			ProjectID:    projectID,
			Path:         cached,
			VaultID:      vaultID,
			Registered:   meta != nil,
			Active:       true,
			Editable:     containsName(entry.EditableProjects, projectID),
			Materialized: dirExists(cached),
			Available:    dirExists(cached),
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

func registeredProjectSet(warrenRoot string) (map[string]bool, error) {
	manifest, _, err := LoadManifest(warrenRoot)
	if err != nil {
		return nil, err
	}
	known := make(map[string]bool, len(manifest.Projects))
	for _, project := range manifest.Projects {
		known[project.ProjectID] = true
	}
	return known, nil
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
