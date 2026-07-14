// Package den manages dens — per-project context workspaces under
// $MARMOT_HOME/dens/<den-id>/ with an optional identity vault and reverse
// route registration.
package den

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/nurozen/context-marmot/internal/flock"
	"github.com/nurozen/context-marmot/internal/home"
	"github.com/nurozen/context-marmot/internal/routes"
	"github.com/nurozen/context-marmot/internal/warren"
	"gopkg.in/yaml.v3"
)

const (
	// ManifestFileName is the den manifest filename.
	ManifestFileName = "_den.md"
	// VaultDirName is the optional identity vault subdirectory.
	VaultDirName = "vault"
	// DataDirName is the den-scoped data directory.
	DataDirName = ".marmot-data"
	// PointerFileName is the project-root pointer to a den/vault id.
	PointerFileName = ".marmot-vault"
)

// Lifetime values for Manifest.Lifetime.
const (
	LifetimeDurable = "durable"
	LifetimeTask    = "task"
)

// CurrentManifestVersion is the newest den manifest schema this binary
// fully understands. Write paths refuse newer versions (version ceiling).
const CurrentManifestVersion = 1

// Link is a den link entry (edit | link | live). Skeleton fields for P1b.
type Link struct {
	Target    string        `yaml:"target" json:"target"`
	Mode      string        `yaml:"mode" json:"mode"` // edit|link|live
	Warren    string        `yaml:"warren,omitempty" json:"warren,omitempty"`
	Project   string        `yaml:"project,omitempty" json:"project,omitempty"`
	PinnedRef string        `yaml:"pinned_ref,omitempty" json:"pinned_ref,omitempty"`
	Embedding LinkEmbedding `yaml:"embedding,omitempty" json:"embedding,omitempty"`
}

// LinkEmbedding holds per-link model/credential refs (P4).
type LinkEmbedding struct {
	Provider string `yaml:"provider,omitempty" json:"provider,omitempty"`
	Model    string `yaml:"model,omitempty" json:"model,omitempty"`
	KeyRef   string `yaml:"key_ref,omitempty" json:"key_ref,omitempty"`
}

// Manifest is the frontmatter schema for _den.md.
type Manifest struct {
	DenID    string   `yaml:"den_id" json:"den_id"`
	Version  int      `yaml:"version" json:"version"`
	Lifetime string   `yaml:"lifetime" json:"lifetime"` // durable|task
	Projects []string `yaml:"projects,omitempty" json:"projects,omitempty"`
	Links    []Link   `yaml:"links,omitempty" json:"links,omitempty"`
}

// CreateOptions controls den create behavior.
type CreateOptions struct {
	// Lifetime is durable|task (default durable when empty).
	Lifetime string
	// Projects are absolute project paths to register.
	Projects []string
	// NoVault skips creating the identity vault/.
	NoVault bool
	// NoPointer skips writing .marmot-vault into project paths.
	NoPointer bool
	// DryRun skips all persistence and returns planned ops.
	DryRun bool
}

// CreateResult is the outcome of Create (used by CLI JSON envelopes).
type CreateResult struct {
	DenID          string
	DenPath        string
	VaultID        string // empty when NoVault
	Projects       []string
	PointerWritten bool
	Ops            []string
	Warnings       []string
}

// StatusInfo is a snapshot of den status for CLI envelopes.
type StatusInfo struct {
	DenID    string
	Lifetime string
	VaultID  string
	Projects []string
	Links    []Link
	DenPath  string
}

// DestroyResult is the outcome of Destroy.
type DestroyResult struct {
	DenID    string
	Destroyed bool
	Kept     bool
	Ops      []string
}

// DensRoot returns $MARMOT_HOME/dens.
func DensRoot() string {
	return home.DensDir()
}

// Path returns the absolute path of a den directory.
func Path(denID string) string {
	return filepath.Join(DensRoot(), denID)
}

// ManifestPath returns the path to _den.md for denID.
func ManifestPath(denID string) string {
	return filepath.Join(Path(denID), ManifestFileName)
}

// VaultPath returns the identity vault path for denID (may not exist).
func VaultPath(denID string) string {
	return filepath.Join(Path(denID), VaultDirName)
}

// ValidateDenID reuses warren ID rules (no path separators, no leading ./_)
// and additionally rejects newlines / control chars that would break YAML
// frontmatter when interpolated into vault _config.md.
func ValidateDenID(id string) error {
	if strings.ContainsAny(id, "\n\r\t") || strings.ContainsRune(id, 0) {
		return fmt.Errorf("den ID must not contain control characters")
	}
	if err := warren.ValidateWarrenID(id); err != nil {
		return fmt.Errorf("%s", strings.NewReplacer("Warren ID", "den ID").Replace(err.Error()))
	}
	return nil
}

// NormalizeLifetime returns a canonical lifetime or an error.
func NormalizeLifetime(s string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", LifetimeDurable:
		return LifetimeDurable, nil
	case LifetimeTask:
		return LifetimeTask, nil
	default:
		return "", fmt.Errorf("lifetime must be %q or %q, got %q", LifetimeDurable, LifetimeTask, s)
	}
}

// LoadManifest reads _den.md for denID.
func LoadManifest(denID string) (*Manifest, string, error) {
	if err := ValidateDenID(denID); err != nil {
		return nil, "", err
	}
	return loadManifestAt(ManifestPath(denID))
}

func loadManifestAt(path string) (*Manifest, string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, "", err
	}
	return parseManifest(data)
}

// SaveManifest writes _den.md atomically under flock.
func SaveManifest(denID string, m *Manifest, body string) error {
	if err := ValidateDenID(denID); err != nil {
		return err
	}
	if m == nil {
		return fmt.Errorf("nil manifest")
	}
	path := ManifestPath(denID)
	return flock.WithLock(path+".lock", func() error {
		return writeManifestAtomic(path, m, body)
	})
}

func writeManifestAtomic(path string, m *Manifest, body string) error {
	if m.Version == 0 {
		m.Version = CurrentManifestVersion
	}
	if m.Version > CurrentManifestVersion {
		return fmt.Errorf("manifest version %d exceeds supported %d; upgrade marmot before editing this den", m.Version, CurrentManifestVersion)
	}
	yamlBytes, err := yaml.Marshal(m)
	if err != nil {
		return fmt.Errorf("marshal den manifest: %w", err)
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
		return fmt.Errorf("create den dir: %w", err)
	}
	tmp, err := os.CreateTemp(dir, "._den-*.md.tmp")
	if err != nil {
		return fmt.Errorf("create den manifest tmp: %w", err)
	}
	tmpPath := tmp.Name()
	success := false
	defer func() {
		if !success {
			_ = tmp.Close()
			_ = os.Remove(tmpPath)
		}
	}()
	if _, err := tmp.Write([]byte(buf.String())); err != nil {
		return fmt.Errorf("write den manifest tmp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close den manifest tmp: %w", err)
	}
	if err := os.Chmod(tmpPath, 0o644); err != nil {
		return fmt.Errorf("chmod den manifest tmp: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("commit den manifest: %w", err)
	}
	success = true
	return nil
}

// Create builds a new den under $MARMOT_HOME/dens/<id>.
func Create(denID string, opts CreateOptions) (*CreateResult, error) {
	if err := ValidateDenID(denID); err != nil {
		return nil, err
	}
	lifetime, err := NormalizeLifetime(opts.Lifetime)
	if err != nil {
		return nil, err
	}

	// Canonicalize project paths.
	projects := make([]string, 0, len(opts.Projects))
	seen := map[string]bool{}
	for _, p := range opts.Projects {
		if strings.TrimSpace(p) == "" {
			continue
		}
		key, err := routes.NormalizeProjectKey(p)
		if err != nil {
			return nil, fmt.Errorf("project %q: %w", p, err)
		}
		if seen[key] {
			continue
		}
		seen[key] = true
		projects = append(projects, key)
	}
	sort.Strings(projects)

	// Reject project paths already owned by another den/vault (route or pointer).
	// Last-write-wins silent reassignment caused route/pointer split-brain.
	for _, p := range projects {
		if err := assertProjectAvailable(p, denID); err != nil {
			return nil, err
		}
	}

	denPath := Path(denID)
	res := &CreateResult{
		DenID:    denID,
		DenPath:  denPath,
		Projects: projects,
	}
	if !opts.NoVault {
		res.VaultID = denID
	}

	// Planned ops (also returned for dry-run).
	res.Ops = append(res.Ops, "mkdir "+denPath)
	res.Ops = append(res.Ops, "write "+filepath.Join(denPath, ManifestFileName))
	res.Ops = append(res.Ops, "mkdir "+filepath.Join(denPath, DataDirName))
	if !opts.NoVault {
		res.Ops = append(res.Ops, "mkdir "+filepath.Join(denPath, VaultDirName))
		res.Ops = append(res.Ops, "write "+filepath.Join(denPath, VaultDirName, "_config.md"))
		res.Ops = append(res.Ops, "mkdir "+filepath.Join(denPath, VaultDirName, DataDirName))
	}
	for _, p := range projects {
		res.Ops = append(res.Ops, fmt.Sprintf("routes.SetProject %s -> %s", p, denID))
		if !opts.NoPointer {
			res.Ops = append(res.Ops, "write "+filepath.Join(p, PointerFileName))
		}
	}

	if opts.DryRun {
		return res, nil
	}

	// Atomically claim the den leaf directory so concurrent creates of the
	// same id cannot both succeed (os.Mkdir fails if the path exists).
	if err := os.MkdirAll(DensRoot(), 0o755); err != nil {
		return nil, fmt.Errorf("create dens root: %w", err)
	}
	if err := os.Mkdir(denPath, 0o755); err != nil {
		if os.IsExist(err) {
			return nil, fmt.Errorf("den %q already exists at %s", denID, denPath)
		}
		return nil, fmt.Errorf("create den dir: %w", err)
	}

	if err := os.MkdirAll(filepath.Join(denPath, DataDirName), 0o755); err != nil {
		_ = os.RemoveAll(denPath)
		return nil, fmt.Errorf("create den data dir: %w", err)
	}

	if !opts.NoVault {
		if err := writeIdentityVault(denPath, denID); err != nil {
			_ = os.RemoveAll(denPath)
			return nil, err
		}
	}

	// Store projects in slash-form.
	storedProjects := make([]string, len(projects))
	for i, p := range projects {
		storedProjects[i] = filepath.ToSlash(p)
	}
	m := &Manifest{
		DenID:    denID,
		Version:  CurrentManifestVersion,
		Lifetime: lifetime,
		Projects: storedProjects,
		Links:    nil,
	}
	body := fmt.Sprintf("# Den %s\n\nLifetime: %s\n", denID, lifetime)
	if err := writeManifestAtomic(ManifestPath(denID), m, body); err != nil {
		_ = os.RemoveAll(denPath)
		return nil, err
	}

	// Reverse routes + optional pointers.
	pointerWritten := false
	for _, p := range projects {
		if err := routes.Update(func(rt *routes.RoutingTable) error {
			rt.SetProject(p, denID)
			return nil
		}); err != nil {
			// Degrade clearly under MARMOT_ROUTES=off (risk R11).
			res.Warnings = append(res.Warnings, fmt.Sprintf("routes.SetProject %s: %v", p, err))
		}
		if !opts.NoPointer {
			if err := WritePointer(p, denID); err != nil {
				res.Warnings = append(res.Warnings, fmt.Sprintf("pointer %s: %v", p, err))
			} else {
				pointerWritten = true
			}
		}
	}
	// pointer_written is true if we intended to write and at least one succeeded,
	// or if projects were empty and we would have written (false when --no-pointer).
	if opts.NoPointer {
		res.PointerWritten = false
	} else if len(projects) == 0 {
		res.PointerWritten = false
	} else {
		res.PointerWritten = pointerWritten
	}

	return res, nil
}

func writeIdentityVault(denPath, denID string) error {
	vaultDir := filepath.Join(denPath, VaultDirName)
	if err := os.MkdirAll(filepath.Join(vaultDir, DataDirName), 0o755); err != nil {
		return fmt.Errorf("create vault data dir: %w", err)
	}
	configContent := fmt.Sprintf(`---
version: "1"
vault_id: %s
namespace: default
embedding_provider: mock
embedding_model: ""
token_budget: 4096
---
# Den identity vault

Lightweight identity vault for den %q.
`, denID, denID)
	if err := os.WriteFile(filepath.Join(vaultDir, "_config.md"), []byte(configContent), 0o644); err != nil {
		return fmt.Errorf("write vault _config.md: %w", err)
	}
	return nil
}

// Status loads den status. Returns os.ErrNotExist-wrapped if missing.
func Status(denID string) (*StatusInfo, error) {
	if err := ValidateDenID(denID); err != nil {
		return nil, err
	}
	m, _, err := LoadManifest(denID)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("den %q not found: %w", denID, err)
		}
		return nil, err
	}
	projects := make([]string, 0, len(m.Projects))
	for _, p := range m.Projects {
		projects = append(projects, filepath.FromSlash(p))
	}
	vaultID := ""
	if _, err := os.Stat(filepath.Join(VaultPath(denID), "_config.md")); err == nil {
		vaultID = denID
	}
	return &StatusInfo{
		DenID:    m.DenID,
		Lifetime: m.Lifetime,
		VaultID:  vaultID,
		Projects: projects,
		Links:    m.Links,
		DenPath:  Path(denID),
	}, nil
}

// assertProjectAvailable returns an error when projectPath is already bound
// to a different den-or-vault id via reverse route or .marmot-vault pointer.
// sameID is allowed (idempotent re-claim by the owning den).
func assertProjectAvailable(projectPath, sameID string) error {
	key, err := routes.NormalizeProjectKey(projectPath)
	if err != nil {
		return fmt.Errorf("project %q: %w", projectPath, err)
	}
	if rt, lerr := routes.Load(); lerr == nil && rt != nil {
		if id, ok := rt.GetProject(key); ok && id != "" && id != sameID {
			return fmt.Errorf("project %q is already registered to %q; relocate or destroy that den first (or route rm --project)", key, id)
		}
	}
	if ptr, perr := ReadPointer(key); perr == nil && ptr != "" && ptr != sameID {
		return fmt.Errorf("project %q has .marmot-vault pointer to %q; remove the pointer or destroy that den before claiming the path", key, ptr)
	}
	return nil
}

// RelocateProject rewrites a registered project path on the den (manifest +
// reverse route) in one logical move. Used by `marmot route set-project` (D6)
// so archive/path-move keeps _den.md.projects in sync with routes.yml.
// Returns the den-or-vault id that owned the old path.
func RelocateProject(fromPath, toPath string) (denOrVaultID string, err error) {
	fromKey, err := routes.NormalizeProjectKey(fromPath)
	if err != nil {
		return "", fmt.Errorf("from: %w", err)
	}
	toKey, err := routes.NormalizeProjectKey(toPath)
	if err != nil {
		return "", fmt.Errorf("to: %w", err)
	}
	if fromKey == toKey {
		// No-op move: still resolve the owner id for callers.
		rt, lerr := routes.Load()
		if lerr != nil {
			return "", lerr
		}
		id, ok := rt.GetProject(fromKey)
		if !ok {
			return "", fmt.Errorf("project %q not found", fromKey)
		}
		return id, nil
	}

	// Resolve owner first so we can allow moves that keep the same id.
	rtPre, lerr := routes.Load()
	if lerr != nil {
		return "", lerr
	}
	ownerID, ok := rtPre.GetProject(fromKey)
	if !ok {
		return "", fmt.Errorf("project %q not found", fromKey)
	}
	// Refuse clobbering a target path owned by a different den/vault.
	if err := assertProjectAvailable(toKey, ownerID); err != nil {
		return "", fmt.Errorf("relocate target: %w", err)
	}

	if err := routes.Update(func(rt *routes.RoutingTable) error {
		id, ok := rt.GetProject(fromKey)
		if !ok {
			return fmt.Errorf("project %q not found", fromKey)
		}
		// Re-check under the write lock: target must still be free or ours.
		if existing, taken := rt.GetProject(toKey); taken && existing != "" && existing != id {
			return fmt.Errorf("project %q is already registered to %q", toKey, existing)
		}
		denOrVaultID = id
		rt.RemoveProject(fromKey)
		rt.SetProject(toKey, denOrVaultID)
		return nil
	}); err != nil {
		return "", err
	}

	// If the id is a den we own, rewrite _den.md projects (from → to).
	// Non-den vault ids leave routes-only (no den manifest).
	if err := ValidateDenID(denOrVaultID); err == nil {
		if _, serr := os.Stat(ManifestPath(denOrVaultID)); serr == nil {
			if merr := rewriteManifestProject(denOrVaultID, fromKey, toKey); merr != nil {
				return denOrVaultID, fmt.Errorf("routes updated but den manifest: %w", merr)
			}
		}
	}
	return denOrVaultID, nil
}

// rewriteManifestProject replaces fromKey with toKey in the den's project list.
func rewriteManifestProject(denID, fromKey, toKey string) error {
	m, body, err := LoadManifest(denID)
	if err != nil {
		return err
	}
	fromSlash := filepath.ToSlash(fromKey)
	toSlash := filepath.ToSlash(toKey)
	found := false
	out := make([]string, 0, len(m.Projects))
	for _, p := range m.Projects {
		// m.Projects are OS-form after parse; compare via slash + normalize.
		key := p
		if nk, nerr := routes.NormalizeProjectKey(p); nerr == nil {
			key = nk
		}
		if filepath.ToSlash(key) == fromSlash || key == fromKey || p == fromKey {
			out = append(out, toSlash)
			found = true
			continue
		}
		out = append(out, filepath.ToSlash(key))
	}
	if !found {
		// Manifest lag / partial state: still record the new path so status
		// and destroy see it.
		out = append(out, toSlash)
	}
	// Dedupe.
	seen := map[string]bool{}
	deduped := make([]string, 0, len(out))
	for _, p := range out {
		if seen[p] {
			continue
		}
		seen[p] = true
		deduped = append(deduped, p)
	}
	m.Projects = deduped
	m.Version = CurrentManifestVersion
	return SaveManifest(denID, m, body)
}

// Destroy removes a den directory and cleans reverse routes / pointers.
// force is reserved for unpushed-edit refusal (P4); in P1b it only skips
// the "den not found" soft path when true is unused beyond existence.
//
// Cleanup removes every reverse-route entry that currently points at this
// den (not only paths listed in the manifest), so a set-project move that
// somehow left routes ahead of the manifest still cannot leave orphans.
func Destroy(denID string, force bool) (*DestroyResult, error) {
	_ = force
	if err := ValidateDenID(denID); err != nil {
		return nil, err
	}
	denPath := Path(denID)
	res := &DestroyResult{DenID: denID, Ops: []string{}}

	m, _, err := LoadManifest(denID)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("den %q not found", denID)
		}
		return nil, err
	}

	// Union of manifest projects + any live routes pointing at this den.
	keys := map[string]bool{}
	for _, pSlash := range m.Projects {
		p := filepath.FromSlash(pSlash)
		key, nerr := routes.NormalizeProjectKey(p)
		if nerr != nil {
			key = p
		}
		keys[key] = true
	}
	if rt, lerr := routes.Load(); lerr == nil && rt != nil {
		for path, id := range rt.ListProjects() {
			if id == denID {
				keys[path] = true
			}
		}
	}

	for key := range keys {
		_ = routes.Update(func(rt *routes.RoutingTable) error {
			if cur, ok := rt.GetProject(key); ok && cur == denID {
				rt.RemoveProject(key)
				res.Ops = append(res.Ops, fmt.Sprintf("routes.RemoveProject %s", key))
			}
			return nil
		})
		ptr := filepath.Join(key, PointerFileName)
		if data, rerr := os.ReadFile(ptr); rerr == nil {
			if strings.TrimSpace(string(data)) == denID {
				res.Ops = append(res.Ops, "remove "+ptr)
				_ = os.Remove(ptr)
			}
		}
	}

	res.Ops = append(res.Ops, "rm -rf "+denPath)
	if err := os.RemoveAll(denPath); err != nil {
		return nil, fmt.Errorf("destroy den: %w", err)
	}
	res.Destroyed = true
	return res, nil
}

// List returns den IDs under $MARMOT_HOME/dens that have a _den.md.
func List() ([]string, error) {
	root := DensRoot()
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var ids []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		id := e.Name()
		if err := ValidateDenID(id); err != nil {
			continue
		}
		if _, err := os.Stat(ManifestPath(id)); err == nil {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	return ids, nil
}

// WritePointer writes a one-line .marmot-vault pointer (den-or-vault id).
func WritePointer(projectPath, denOrVaultID string) error {
	key, err := routes.NormalizeProjectKey(projectPath)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(key, 0o755); err != nil {
		return fmt.Errorf("create project dir for pointer: %w", err)
	}
	path := filepath.Join(key, PointerFileName)
	tmp, err := os.CreateTemp(key, ".marmot-vault-*.tmp")
	if err != nil {
		return fmt.Errorf("create pointer tmp: %w", err)
	}
	tmpPath := tmp.Name()
	success := false
	defer func() {
		if !success {
			_ = tmp.Close()
			_ = os.Remove(tmpPath)
		}
	}()
	if _, err := tmp.WriteString(denOrVaultID + "\n"); err != nil {
		return fmt.Errorf("write pointer: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("commit pointer: %w", err)
	}
	success = true
	return nil
}

// ReadPointer reads a .marmot-vault pointer; returns ("", nil) if missing.
func ReadPointer(projectPath string) (string, error) {
	key, err := routes.NormalizeProjectKey(projectPath)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(filepath.Join(key, PointerFileName))
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}

// RemovePointer deletes .marmot-vault if present.
func RemovePointer(projectPath string) error {
	key, err := routes.NormalizeProjectKey(projectPath)
	if err != nil {
		return err
	}
	path := filepath.Join(key, PointerFileName)
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// AdoptOptions controls minimal den adopt (P1b skeleton).
type AdoptOptions struct {
	// From is the project path containing an in-repo .marmot/ vault.
	From string
	// DenID overrides the derived den id (default: vault_id or basename).
	DenID string
	// DryRun skips persistence.
	DryRun bool
	// Apply is reserved for MCP config rewrites (OQ13); ignored in skeleton.
	Apply bool
}

// AdoptResult is a minimal adopt outcome.
type AdoptResult struct {
	DenID   string
	DenPath string
	From    string
	Ops     []string
	// Note explains skeleton limitations.
	Note string
}

// Adopt is a minimal P1b skeleton: creates a den pointing at the project
// without moving the in-repo vault yet (full migrate is later).
func Adopt(opts AdoptOptions) (*AdoptResult, error) {
	from := opts.From
	if from == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return nil, err
		}
		from = cwd
	}
	key, err := routes.NormalizeProjectKey(from)
	if err != nil {
		return nil, err
	}
	denID := opts.DenID
	if denID == "" {
		denID = filepath.Base(key)
	}
	if err := ValidateDenID(denID); err != nil {
		return nil, err
	}
	res := &AdoptResult{
		DenID:   denID,
		DenPath: Path(denID),
		From:    key,
		Note:    "skeleton adopt: creates den + reverse route; does not move in-repo .marmot yet",
		Ops: []string{
			"mkdir " + Path(denID),
			"write " + ManifestPath(denID),
			fmt.Sprintf("routes.SetProject %s -> %s", key, denID),
		},
	}
	if opts.DryRun {
		return res, nil
	}
	cr, err := Create(denID, CreateOptions{
		Lifetime: LifetimeDurable,
		Projects:  []string{key},
		NoVault:   false,
		NoPointer: false,
	})
	if err != nil {
		return nil, err
	}
	res.DenPath = cr.DenPath
	res.Ops = cr.Ops
	return res, nil
}
