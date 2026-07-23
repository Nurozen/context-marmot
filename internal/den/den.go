// Package den manages dens — per-project context workspaces under
// $MARMOT_HOME/dens/<den-id>/ with an optional identity vault and reverse
// route registration.
package den

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/nurozen/context-marmot/internal/config"
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
	// EmbeddingProvider sets the identity vault's embedding_provider
	// ("mock" when empty). §18.4: a den vault must be creatable with a real
	// provider without hand-editing _config.md.
	EmbeddingProvider string
	// EmbeddingModel sets the identity vault's embedding_model (provider
	// default when empty).
	EmbeddingModel string
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
	DenID     string
	Destroyed bool
	Kept      bool
	Ops       []string
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

// LoadManifestAt reads a den manifest at an explicit filesystem path. Used
// by callers that discover _den.md relative to a served vault directory
// (e.g. the MCP instructions builder) instead of resolving by den id under
// $MARMOT_HOME/dens.
func LoadManifestAt(path string) (*Manifest, string, error) {
	return loadManifestAt(path)
}

func loadManifestAt(path string) (*Manifest, string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, "", err
	}
	return parseManifest(data)
}

// SaveManifest writes _den.md atomically under flock.
//
// NOTE: SaveManifest only serializes the WRITE. A Load→mutate→Save caller
// still races a concurrent mutator (lost update: both load the same links,
// each saves its own copy, one link vanishes). Mutations of an existing
// manifest must go through UpdateManifest, which holds the flock across the
// whole read-modify-write cycle.
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

// UpdateManifest performs a read-modify-write of the den manifest with the
// WHOLE cycle under the manifest flock (mirroring
// warren.UpdateWorkspaceStateInMarmot), so concurrent mutators — e.g. two
// `den link` processes appending different links — cannot drop each other's
// writes the way Load→mutate→Save callers could. fn mutates the loaded
// manifest in place and returns write=false to skip persisting (no-op
// mutations leave the file untouched); any fn error aborts without writing.
// The manifest body is preserved verbatim.
func UpdateManifest(denID string, fn func(m *Manifest) (write bool, err error)) error {
	if err := ValidateDenID(denID); err != nil {
		return err
	}
	path := ManifestPath(denID)
	return flock.WithLock(path+".lock", func() error {
		m, body, err := loadManifestAt(path)
		if err != nil {
			return err
		}
		write, err := fn(m)
		if err != nil || !write {
			return err
		}
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
	embProvider, err := normalizeEmbeddingProvider(opts.EmbeddingProvider)
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
		if err := writeIdentityVault(denPath, denID, embProvider, opts.EmbeddingModel); err != nil {
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

// normalizeEmbeddingProvider validates a den-create embedding provider
// override. The allowlist mirrors embedding.NewEmbedder: writing an unknown
// provider into _config.md would only break the vault at serve time.
func normalizeEmbeddingProvider(p string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(p)) {
	case "", "mock":
		return "mock", nil
	case "openai":
		return "openai", nil
	default:
		return "", fmt.Errorf("unknown embedding provider %q (supported: openai, mock)", p)
	}
}

func writeIdentityVault(denPath, denID, embProvider, embModel string) error {
	vaultDir := filepath.Join(denPath, VaultDirName)
	if err := os.MkdirAll(filepath.Join(vaultDir, DataDirName), 0o755); err != nil {
		return fmt.Errorf("create vault data dir: %w", err)
	}
	if embProvider == "" {
		embProvider = "mock"
	}
	configContent := fmt.Sprintf(`---
version: "1"
vault_id: %s
namespace: default
embedding_provider: %s
embedding_model: %q
token_budget: 4096
---
# Den identity vault

Lightweight identity vault for den %q.
`, denID, embProvider, embModel, denID)
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

// rewriteManifestProject replaces fromKey with toKey in the den's project
// list, with the whole read-modify-write under the manifest flock
// (UpdateManifest) so a concurrent link mutation is never lost.
func rewriteManifestProject(denID, fromKey, toKey string) error {
	return UpdateManifest(denID, func(m *Manifest) (bool, error) {
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
		return true, nil
	})
}

// Destroy removes a den directory and cleans reverse routes / pointers.
// force is accepted for signature stability but unused here: the
// unpushed-edit refusal (and edit-worktree removal) for cache-backed edit
// links lives in the CLI layer, which owns all git execution — internal
// packages stay exec-free.
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

// AdoptOptions controls den adopt (migrate an in-repo .marmot vault into a den).
type AdoptOptions struct {
	// From is the project path containing an in-repo .marmot/ vault.
	From string
	// DenID overrides the derived den id (default: source vault_id, else basename).
	DenID string
	// DryRun skips persistence.
	DryRun bool
	// NoPointer skips writing the .marmot-vault pointer into the project root
	// (OQ3: adopt writes the pointer by default).
	NoPointer bool
}

// AdoptResult is the adopt outcome.
type AdoptResult struct {
	DenID   string
	DenPath string
	From    string
	// VaultMoved is true when the in-repo .marmot moved into the den.
	VaultMoved bool
	// PointerWritten is true when .marmot-vault was written into the project.
	PointerWritten bool
	Ops            []string
	Warnings       []string
}

// AdoptRefusal is a structured adopt failure. The CLI maps Code straight onto
// the schema:1 error envelope's error.code.
type AdoptRefusal struct {
	Code    string // "not_a_vault" | "den_vault_exists" | "move_failed"
	Message string
	Hint    string
}

func (e *AdoptRefusal) Error() string { return e.Message }

// SourceVaultPath returns the in-repo vault an adopt of projectKey would move.
func SourceVaultPath(projectKey string) string {
	return filepath.Join(projectKey, ".marmot")
}

// Adopt migrates a project's in-repo .marmot/ vault into
// $MARMOT_HOME/dens/<id>/vault/ (the layouts are byte-identical per OQ1):
// creates the den (no fresh identity vault — the moved one IS the identity
// vault), moves the vault (same-filesystem rename when possible, else
// copy+verify+remove; .marmot-data/embeddings.db travels with it), registers
// the reverse route, and writes the .marmot-vault pointer unless NoPointer.
// MCP config rewrites live in the CLI layer (exec-free package rule).
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
	srcVault := SourceVaultPath(key)
	if _, err := os.Stat(filepath.Join(srcVault, "_config.md")); err != nil {
		return nil, &AdoptRefusal{
			Code:    "not_a_vault",
			Message: fmt.Sprintf("%s has no .marmot/_config.md — nothing to adopt", key),
			Hint:    "run 'marmot init' in the project first, or create a fresh den: marmot den create <id> --project " + key,
		}
	}
	denID := opts.DenID
	if denID == "" {
		// Prefer the source vault's declared vault_id, else the project basename.
		if cfg, cerr := config.Load(srcVault); cerr == nil && cfg.VaultID != "" && ValidateDenID(cfg.VaultID) == nil {
			denID = cfg.VaultID
		} else {
			denID = filepath.Base(key)
		}
	}
	if err := ValidateDenID(denID); err != nil {
		return nil, err
	}
	dstVault := VaultPath(denID)
	if _, err := os.Stat(dstVault); err == nil {
		return nil, &AdoptRefusal{
			Code:    "den_vault_exists",
			Message: fmt.Sprintf("den %q already has a vault at %s; refusing to overwrite it", denID, dstVault),
			Hint:    "pick another id with --id, or destroy the existing den first: marmot den destroy " + denID,
		}
	}

	res := &AdoptResult{
		DenID:   denID,
		DenPath: Path(denID),
		From:    key,
		Ops: []string{
			"mkdir " + Path(denID),
			"write " + ManifestPath(denID),
			fmt.Sprintf("move %s -> %s", srcVault, dstVault),
			fmt.Sprintf("routes.SetProject %s -> %s", key, denID),
		},
		Warnings: []string{},
	}
	if !opts.NoPointer {
		res.Ops = append(res.Ops, "write "+filepath.Join(key, PointerFileName))
	}
	if opts.DryRun {
		return res, nil
	}

	// Create the den shell first (atomically claims the den dir; registers the
	// reverse route). NoVault: the moved vault IS the identity vault. The
	// pointer is written only AFTER a successful move so a failed adopt never
	// leaves a pointer at an empty den.
	cr, err := Create(denID, CreateOptions{
		Lifetime:  LifetimeDurable,
		Projects:  []string{key},
		NoVault:   true,
		NoPointer: true,
	})
	if err != nil {
		return nil, err
	}
	res.DenPath = cr.DenPath
	res.Warnings = append(res.Warnings, cr.Warnings...)

	srcLeftover, err := moveVaultDir(srcVault, dstVault)
	if err != nil {
		// Roll back the den shell (routes included) so a retry starts clean.
		// moveVaultDir leaves the source vault intact on every error path.
		if _, derr := Destroy(denID, false); derr != nil {
			res.Warnings = append(res.Warnings, fmt.Sprintf("rollback of den %q failed: %v", denID, derr))
		}
		return nil, &AdoptRefusal{
			Code:    "move_failed",
			Message: fmt.Sprintf("moving %s to %s failed: %v", srcVault, dstVault, err),
			Hint:    fmt.Sprintf("the source vault at %s is intact and the den was rolled back; fix the cause (permissions/disk space) and re-run marmot den adopt", srcVault),
		}
	}
	res.VaultMoved = true
	if srcLeftover != nil {
		// The verified copy is already promoted into the den — never roll
		// back here (that would destroy the only complete copy).
		res.Warnings = append(res.Warnings, fmt.Sprintf("vault adopted, but the source under %s could not be fully removed: %v — remove the leftover manually", srcVault, srcLeftover))
	}

	if !opts.NoPointer {
		if perr := WritePointer(key, denID); perr != nil {
			res.Warnings = append(res.Warnings, fmt.Sprintf("pointer %s: %v", key, perr))
		} else {
			res.PointerWritten = true
		}
	}
	return res, nil
}

// moveVaultDir moves src to dst: a same-filesystem rename when possible,
// otherwise copy+verify+remove. On every err path the source is left intact
// (partial copies are cleaned up); the source is only deleted after the copy
// is fully verified AND promoted to dst. A source-removal failure at that
// point is reported via srcLeftover, NOT err — the move itself succeeded and
// the caller must never roll back the (only complete) den copy for it.
func moveVaultDir(src, dst string) (srcLeftover, err error) {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return nil, err
	}
	if err := os.Rename(src, dst); err == nil {
		return nil, nil
	}
	// Cross-device (or otherwise un-renameable): copy into a temp sibling of
	// dst, verify byte-for-byte, promote, then remove the source.
	tmp := dst + ".adopt-tmp"
	_ = os.RemoveAll(tmp)
	if err := copyTree(src, tmp); err != nil {
		_ = os.RemoveAll(tmp)
		return nil, fmt.Errorf("copy: %w", err)
	}
	if err := verifyTree(src, tmp); err != nil {
		_ = os.RemoveAll(tmp)
		return nil, fmt.Errorf("copy verification: %w", err)
	}
	if err := os.Rename(tmp, dst); err != nil {
		_ = os.RemoveAll(tmp)
		return nil, err
	}
	if err := os.RemoveAll(src); err != nil {
		return err, nil
	}
	return nil, nil
}

// copyTree copies the directory tree at src to dst, preserving file modes.
func copyTree(src, dst string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, rerr := filepath.Rel(src, path)
		if rerr != nil {
			return rerr
		}
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, info.Mode().Perm()|0o700)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("%s: unsupported non-regular file", path)
		}
		data, derr := os.ReadFile(path)
		if derr != nil {
			return derr
		}
		return os.WriteFile(target, data, info.Mode().Perm())
	})
}

// verifyTree checks that every regular file under src exists under dst with
// identical bytes (the byte-identical guarantee behind copy+verify+remove).
func verifyTree(src, dst string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() || !info.Mode().IsRegular() {
			return nil
		}
		rel, rerr := filepath.Rel(src, path)
		if rerr != nil {
			return rerr
		}
		want, werr := os.ReadFile(path)
		if werr != nil {
			return werr
		}
		got, gerr := os.ReadFile(filepath.Join(dst, rel))
		if gerr != nil {
			return gerr
		}
		if !bytes.Equal(want, got) {
			return fmt.Errorf("%s differs after copy", rel)
		}
		return nil
	})
}
