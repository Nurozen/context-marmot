// Package warren manages git-backed Warren manifests and local workspace
// mount/edit state.
package warren

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

const (
	ManifestFileName = "_warren.md"
	MarmotDirName    = ".marmot"
)

// Manifest describes a Warren repository.
type Manifest struct {
	WarrenID string    `yaml:"warren_id"`
	Version  int       `yaml:"version"`
	Projects []Project `yaml:"projects,omitempty"`
	Bridges  []Bridge  `yaml:"bridges,omitempty"`
}

// Project describes one project vault inside a Warren repository.
type Project struct {
	ProjectID string   `yaml:"project_id"`
	Path      string   `yaml:"path"`
	Aliases   []string `yaml:"aliases,omitempty"`
}

// Bridge describes curated cross-project relations in a Warren.
type Bridge struct {
	Source    string   `yaml:"source"`
	Target    string   `yaml:"target"`
	Relations []string `yaml:"relations"`
}

// ProjectMetadata lives in projects/<id>/.marmot/_warren.md.
type ProjectMetadata struct {
	ProjectID string   `yaml:"project_id"`
	WarrenID  string   `yaml:"warren_id"`
	VaultID   string   `yaml:"vault_id"`
	Aliases   []string `yaml:"aliases,omitempty"`
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
		return nil, fmt.Errorf("Warren ID mismatch: manifest has %q, command used %q", manifest.WarrenID, warrenID)
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
			return fmt.Errorf("Warren %q is not registered in this workspace", warrenID)
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
			return fmt.Errorf("Warren %q is not registered in this workspace", warrenID)
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
		return nil, fmt.Errorf("Warren %q is not registered in this workspace", warrenID)
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
		meta, _, _ := LoadProjectMetadata(marmotDir)
		if meta == nil && marmotDir != sourceDir {
			meta, _, _ = LoadProjectMetadata(sourceDir)
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
				mounts = append(mounts, materializedStatuses(marmotDir, warrenID, entry)...)
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
			meta, _, _ := LoadProjectMetadata(marmotPath)
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

// Materialize copies a Warren project vault into the local workspace cache.
func Materialize(workspaceMarmotDir, warrenID string, project Project, warrenRoot string) (string, error) {
	source := filepath.Join(warrenRoot, filepath.FromSlash(project.Path))
	target := materializedProjectPath(workspaceMarmotDir, warrenID, project.ProjectID)
	if err := copyDir(source, target); err != nil {
		return "", err
	}
	return target, nil
}

func materializedStatuses(workspaceMarmotDir, warrenID string, entry WorkspaceWarren) []ProjectStatus {
	var statuses []ProjectStatus
	for _, projectID := range entry.ActiveProjects {
		cached := materializedProjectPath(workspaceMarmotDir, warrenID, projectID)
		meta, _, _ := LoadProjectMetadata(cached)
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
	if entry.Materialized {
		cached := materializedProjectPath(workspaceMarmotDir, warrenID, project.ProjectID)
		if dirExists(cached) {
			return cached
		}
	}
	return filepath.Join(entry.Path, filepath.FromSlash(project.Path))
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

func updateWorkspaceState(workspaceRoot string, fn func(*WorkspaceState) error) (*WorkspaceState, error) {
	state, body, err := LoadWorkspaceState(workspaceRoot)
	if err != nil {
		return nil, err
	}
	if err := fn(state); err != nil {
		return nil, err
	}
	if err := SaveWorkspaceState(workspaceRoot, state, body); err != nil {
		return nil, err
	}
	return state, nil
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
	content := string(data)
	if !strings.HasPrefix(content, "---") {
		return "", fmt.Errorf("missing YAML frontmatter")
	}
	end := strings.Index(content[3:], "---")
	if end < 0 {
		return "", fmt.Errorf("unterminated YAML frontmatter")
	}
	yamlBlock := content[3 : end+3]
	body := content[end+6:]
	if strings.HasPrefix(body, "\n") {
		body = body[1:]
	}
	if err := yaml.Unmarshal([]byte(yamlBlock), out); err != nil {
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
	}
	sort.Slice(m.Projects, func(i, j int) bool { return m.Projects[i].ProjectID < m.Projects[j].ProjectID })
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
		if err := ValidateProjectID(bridge.Source); err != nil {
			return err
		}
		if err := ValidateProjectID(bridge.Target); err != nil {
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

func copyDir(source, target string) error {
	info, err := os.Stat(source)
	if err != nil {
		return fmt.Errorf("stat source: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("source %q is not a directory", source)
	}
	return filepath.WalkDir(source, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		dest := filepath.Join(target, rel)
		if d.IsDir() {
			return os.MkdirAll(dest, 0o755)
		}
		src, err := os.Open(path)
		if err != nil {
			return err
		}
		defer src.Close()
		info, err := d.Info()
		if err != nil {
			return err
		}
		dst, err := os.OpenFile(dest, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, info.Mode())
		if err != nil {
			return err
		}
		defer dst.Close()
		_, err = io.Copy(dst, src)
		return err
	})
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

func uniqueProjects(projects []Project) []Project {
	seen := make(map[string]Project, len(projects))
	for _, project := range projects {
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
