// Package namespace provides namespace management and cross-namespace bridge
// validation for ContextMarmot vaults.
package namespace

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/nurozen/context-marmot/internal/config"
	"github.com/nurozen/context-marmot/internal/routes"
	"gopkg.in/yaml.v3"
)

// NamespaceSettings holds per-namespace configuration.
type NamespaceSettings struct {
	AutoIndex                   bool     `yaml:"auto_index,omitempty"`
	SourceGlobs                 []string `yaml:"source_globs,omitempty"`
	IgnoreGlobs                 []string `yaml:"ignore_globs,omitempty"`
	EmbeddingModel              string   `yaml:"embedding_model,omitempty"`
	SummaryRegenerationInterval string   `yaml:"summary_regeneration_interval,omitempty"`
}

// Namespace represents a project namespace within a ContextMarmot vault.
// Each namespace corresponds to a top-level folder under .marmot/.
type Namespace struct {
	Name     string            `yaml:"name"`
	RootPath string            `yaml:"root_path,omitempty"`
	Created  string            `yaml:"created,omitempty"`
	Settings NamespaceSettings `yaml:"settings,omitempty"`
}

// Bridge defines allowed cross-namespace edge relations between two namespaces.
// The actual edges are declared in individual node files and auto-discovered.
type Bridge struct {
	Source           string   `yaml:"source"`
	Target           string   `yaml:"target"`
	Created          string   `yaml:"created,omitempty"`
	AllowedRelations []string `yaml:"allowed_relations"`

	// Cross-vault fields (non-empty only for cross-vault bridges)
	SourceVaultPath string `yaml:"source_vault_path,omitempty"`
	TargetVaultPath string `yaml:"target_vault_path,omitempty"`
	SourceVaultID   string `yaml:"source_vault_id,omitempty"`
	TargetVaultID   string `yaml:"target_vault_id,omitempty"`
}

// IsCrossVault returns true if the bridge spans two different vaults.
func (b *Bridge) IsCrossVault() bool {
	return b.SourceVaultPath != "" || b.TargetVaultPath != "" ||
		(b.SourceVaultID != "" && b.TargetVaultID != "")
}

// QualifiedID holds a parsed cross-namespace reference.
type QualifiedID struct {
	VaultID   string // "" = local vault; non-empty = cross-vault (@prefix)
	Namespace string // "" means local / same namespace
	NodeID    string
}

// CrossNamespaceEdge represents a discovered cross-namespace edge from scanning node files.
type CrossNamespaceEdge struct {
	SourceNamespace string
	SourceNodeID    string
	TargetNamespace string
	TargetNodeID    string
	Relation        string
}

// Manager provides namespace and bridge operations for a vault.
type Manager struct {
	VaultDir          string
	Namespaces        map[string]*Namespace
	Bridges           map[string]*Bridge // key: "source--target"
	CrossVaultBridges []*Bridge          // bridges where IsCrossVault() is true
}

// NewManager creates a Manager and loads all namespaces and bridges from disk.
func NewManager(vaultDir string) (*Manager, error) {
	m := &Manager{
		VaultDir:   vaultDir,
		Namespaces: make(map[string]*Namespace),
		Bridges:    make(map[string]*Bridge),
	}
	if err := m.loadNamespaces(); err != nil {
		return nil, fmt.Errorf("namespace manager: %w", err)
	}
	if err := m.loadBridges(); err != nil {
		return nil, fmt.Errorf("namespace manager: %w", err)
	}
	return m, nil
}

// loadNamespaces discovers all namespace directories (top-level dirs that aren't
// hidden or underscore-prefixed) and loads their _namespace.md config files.
func (m *Manager) loadNamespaces() error {
	entries, err := os.ReadDir(m.VaultDir)
	if err != nil {
		return fmt.Errorf("read vault dir: %w", err)
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		name := entry.Name()
		// Skip hidden dirs (.obsidian, .marmot-data) and underscore-prefixed dirs (_bridges, _heat).
		if strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_") {
			continue
		}
		ns, err := LoadNamespace(filepath.Join(m.VaultDir, name))
		if err != nil {
			// No _namespace.md — this is a regular node subdirectory, not a namespace.
			continue
		}
		m.Namespaces[name] = ns
	}
	return nil
}

// loadBridges reads all bridge manifests from _bridges/.
func (m *Manager) loadBridges() error {
	bridgeDir := filepath.Join(m.VaultDir, "_bridges")
	entries, err := os.ReadDir(bridgeDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // no bridges directory is fine
		}
		return fmt.Errorf("read bridges dir: %w", err)
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}
		path := filepath.Join(bridgeDir, entry.Name())
		b, err := LoadBridge(path)
		if err != nil {
			continue // skip malformed bridge files
		}
		key := BridgeKey(b.Source, b.Target)
		m.Bridges[key] = b
		if b.IsCrossVault() {
			m.CrossVaultBridges = append(m.CrossVaultBridges, b)
		}
	}
	return nil
}

// LoadNamespace reads and parses a _namespace.md file from the given namespace directory.
func LoadNamespace(nsDir string) (*Namespace, error) {
	path := filepath.Join(nsDir, "_namespace.md")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("load namespace: %w", err)
	}
	ns, err := parseNamespace(data)
	if err != nil {
		return nil, fmt.Errorf("parse namespace %s: %w", path, err)
	}
	return ns, nil
}

// parseNamespace extracts YAML frontmatter from _namespace.md content.
func parseNamespace(data []byte) (*Namespace, error) {
	content := string(data)
	if !strings.HasPrefix(content, "---") {
		return nil, fmt.Errorf("missing YAML frontmatter")
	}
	end := strings.Index(content[3:], "---")
	if end < 0 {
		return nil, fmt.Errorf("unterminated YAML frontmatter")
	}
	yamlBlock := content[3 : end+3]
	var ns Namespace
	if err := yaml.Unmarshal([]byte(yamlBlock), &ns); err != nil {
		return nil, fmt.Errorf("unmarshal namespace: %w", err)
	}
	if ns.Name == "" {
		return nil, fmt.Errorf("namespace name is required")
	}
	return &ns, nil
}

// CreateNamespace creates a new namespace directory and writes its _namespace.md file.
func CreateNamespace(vaultDir, name, rootPath string) (*Namespace, error) {
	if name == "" {
		return nil, fmt.Errorf("namespace name must not be empty")
	}
	// Validate name: no path separators, no dots at start, no special chars.
	if strings.ContainsAny(name, "/\\") || strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_") {
		return nil, fmt.Errorf("invalid namespace name %q: must not contain path separators or start with . or _", name)
	}

	nsDir := filepath.Join(vaultDir, name)
	if err := os.MkdirAll(nsDir, 0o755); err != nil {
		return nil, fmt.Errorf("create namespace dir: %w", err)
	}

	ns := &Namespace{
		Name:     name,
		RootPath: rootPath,
		Created:  time.Now().UTC().Format(time.RFC3339),
	}

	if err := SaveNamespace(nsDir, ns); err != nil {
		return nil, fmt.Errorf("save namespace: %w", err)
	}
	return ns, nil
}

// SaveNamespace writes a Namespace to _namespace.md in the given directory.
func SaveNamespace(nsDir string, ns *Namespace) error {
	yamlBytes, err := yaml.Marshal(ns)
	if err != nil {
		return fmt.Errorf("marshal namespace: %w", err)
	}

	var buf strings.Builder
	buf.WriteString("---\n")
	buf.Write(yamlBytes)
	buf.WriteString("---\n\n")
	buf.WriteString(fmt.Sprintf("Namespace configuration for %s.\n", ns.Name))

	path := filepath.Join(nsDir, "_namespace.md")
	tmpPath := path + ".tmp"
	if err := os.WriteFile(tmpPath, []byte(buf.String()), 0o644); err != nil {
		return fmt.Errorf("write tmp namespace: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("rename namespace: %w", err)
	}
	return nil
}

// ListNamespaces returns the names of all namespaces in the vault.
func (m *Manager) ListNamespaces() []string {
	names := make([]string, 0, len(m.Namespaces))
	for name := range m.Namespaces {
		names = append(names, name)
	}
	return names
}

// GetNamespace returns the Namespace with the given name, or nil if not found.
func (m *Manager) GetNamespace(name string) *Namespace {
	return m.Namespaces[name]
}

// NamespaceDir returns the on-disk directory path for a namespace.
func (m *Manager) NamespaceDir(name string) string {
	return filepath.Join(m.VaultDir, name)
}

// --- Qualified ID Resolution ---

// ParseQualifiedID splits an edge target into namespace and node ID components.
// If the target contains no namespace prefix (i.e., it's a local reference),
// the returned Namespace field is empty.
//
// Convention: a qualified ID uses the format "namespace/node/path".
// The first path component is checked against known namespaces to disambiguate
// from local node IDs that contain slashes (e.g., "auth/login").
func (m *Manager) ParseQualifiedID(target, currentNamespace string) QualifiedID {
	// Cross-vault reference: @vault-id/node-id
	if strings.HasPrefix(target, "@") {
		rest := target[1:]
		parts := strings.SplitN(rest, "/", 2)
		if len(parts) < 2 || parts[0] == "" {
			// Invalid cross-vault reference (e.g., "@", "@/node") — treat as local.
			return QualifiedID{Namespace: currentNamespace, NodeID: target}
		}
		return QualifiedID{VaultID: parts[0], NodeID: parts[1]}
	}

	// Intra-vault logic (unchanged).
	parts := strings.SplitN(target, "/", 2)
	if len(parts) < 2 {
		return QualifiedID{Namespace: currentNamespace, NodeID: target}
	}
	// Check if the first component is a known namespace different from current.
	candidate := parts[0]
	if _, ok := m.Namespaces[candidate]; ok && candidate != currentNamespace {
		return QualifiedID{Namespace: candidate, NodeID: parts[1]}
	}
	// Not a cross-namespace reference — treat entire target as a local node ID.
	return QualifiedID{Namespace: currentNamespace, NodeID: target}
}

// FormatQualifiedID produces a qualified "namespace/nodeID" string.
// If namespace matches currentNamespace, returns just the nodeID (local ref).
func FormatQualifiedID(namespace, nodeID, currentNamespace string) string {
	if namespace == currentNamespace || namespace == "" {
		return nodeID
	}
	return namespace + "/" + nodeID
}

// --- Bridge Operations ---

// LoadBridge reads and parses a bridge manifest file.
func LoadBridge(path string) (*Bridge, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("load bridge: %w", err)
	}
	return parseBridge(data)
}

// parseBridge extracts YAML frontmatter from a bridge manifest.
func parseBridge(data []byte) (*Bridge, error) {
	content := string(data)
	if !strings.HasPrefix(content, "---") {
		return nil, fmt.Errorf("missing YAML frontmatter")
	}
	end := strings.Index(content[3:], "---")
	if end < 0 {
		return nil, fmt.Errorf("unterminated YAML frontmatter")
	}
	yamlBlock := content[3 : end+3]
	var b Bridge
	if err := yaml.Unmarshal([]byte(yamlBlock), &b); err != nil {
		return nil, fmt.Errorf("unmarshal bridge: %w", err)
	}
	if b.Source == "" || b.Target == "" {
		return nil, fmt.Errorf("bridge source and target are required")
	}
	return &b, nil
}

// CreateBridge creates a bridge manifest between two namespaces.
func CreateBridge(vaultDir, source, target string, allowedRelations []string) (*Bridge, error) {
	if source == "" || target == "" {
		return nil, fmt.Errorf("bridge source and target must not be empty")
	}
	if source == target {
		return nil, fmt.Errorf("bridge source and target must be different namespaces")
	}

	bridgeDir := filepath.Join(vaultDir, "_bridges")
	if err := os.MkdirAll(bridgeDir, 0o755); err != nil {
		return nil, fmt.Errorf("create bridges dir: %w", err)
	}

	b := &Bridge{
		Source:           source,
		Target:           target,
		Created:          time.Now().UTC().Format(time.RFC3339),
		AllowedRelations: allowedRelations,
	}

	if err := SaveBridge(vaultDir, b); err != nil {
		return nil, fmt.Errorf("save bridge: %w", err)
	}
	return b, nil
}

// SaveBridge writes a bridge manifest to _bridges/<source>--<target>.md.
func SaveBridge(vaultDir string, b *Bridge) error {
	bridgeDir := filepath.Join(vaultDir, "_bridges")
	if err := os.MkdirAll(bridgeDir, 0o755); err != nil {
		return fmt.Errorf("create bridges dir: %w", err)
	}

	yamlBytes, err := yaml.Marshal(b)
	if err != nil {
		return fmt.Errorf("marshal bridge: %w", err)
	}

	var buf strings.Builder
	buf.WriteString("---\n")
	buf.Write(yamlBytes)
	buf.WriteString("---\n\n")
	buf.WriteString(fmt.Sprintf("Bridge between [[%s/_namespace]] and [[%s/_namespace]].\n\n", b.Source, b.Target))
	buf.WriteString(fmt.Sprintf("Allowed cross-namespace relation types: %s.\n\n", formatRelationList(b.AllowedRelations)))
	buf.WriteString("Actual edges are declared in individual node files and auto-discovered by the engine.\n")

	filename := BridgeKey(b.Source, b.Target) + ".md"
	path := filepath.Join(bridgeDir, filename)
	tmpPath := path + ".tmp"
	if err := os.WriteFile(tmpPath, []byte(buf.String()), 0o644); err != nil {
		return fmt.Errorf("write tmp bridge: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("rename bridge: %w", err)
	}
	return nil
}

// BridgeKey returns the canonical key for a bridge between two namespaces.
func BridgeKey(source, target string) string {
	return source + "--" + target
}

// ValidateCrossNamespaceEdge checks whether a cross-namespace edge is allowed
// by the bridge configuration. Returns nil if allowed, error if not.
func (m *Manager) ValidateCrossNamespaceEdge(sourceNS, targetNS, relation string) error {
	if sourceNS == targetNS {
		return nil // same-namespace edges don't need bridge validation
	}

	// Check both directions: source--target and target--source.
	key1 := BridgeKey(sourceNS, targetNS)
	key2 := BridgeKey(targetNS, sourceNS)

	bridge, ok := m.Bridges[key1]
	if !ok {
		bridge, ok = m.Bridges[key2]
	}
	if !ok {
		return fmt.Errorf("no bridge between namespaces %q and %q", sourceNS, targetNS)
	}

	for _, allowed := range bridge.AllowedRelations {
		if allowed == relation {
			return nil
		}
	}
	return fmt.Errorf("relation %q not allowed between namespaces %q and %q (allowed: %v)",
		relation, sourceNS, targetNS, bridge.AllowedRelations)
}

// DiscoverCrossNamespaceEdges scans all node files in all namespaces and returns
// edges that reference nodes in other namespaces.
func (m *Manager) DiscoverCrossNamespaceEdges() ([]CrossNamespaceEdge, error) {
	var crossEdges []CrossNamespaceEdge

	for nsName := range m.Namespaces {
		nsDir := m.NamespaceDir(nsName)
		err := filepath.Walk(nsDir, func(path string, info os.FileInfo, walkErr error) error {
			if walkErr != nil {
				return nil
			}
			if info.IsDir() {
				if strings.HasPrefix(info.Name(), ".") && path != nsDir {
					return filepath.SkipDir
				}
				return nil
			}
			if strings.HasPrefix(info.Name(), "_") || !strings.HasSuffix(info.Name(), ".md") {
				return nil
			}

			data, err := os.ReadFile(path)
			if err != nil {
				return nil
			}

			// Quick parse to extract edges from YAML frontmatter.
			edges := extractEdgesFromFrontmatter(data)
			for _, e := range edges {
				qid := m.ParseQualifiedID(e.target, nsName)
				if qid.Namespace != nsName {
					// Compute source node ID from file path.
					rel, relErr := filepath.Rel(nsDir, path)
					if relErr != nil {
						continue
					}
					sourceNodeID := strings.TrimSuffix(filepath.ToSlash(rel), ".md")

					crossEdges = append(crossEdges, CrossNamespaceEdge{
						SourceNamespace: nsName,
						SourceNodeID:    sourceNodeID,
						TargetNamespace: qid.Namespace,
						TargetNodeID:    qid.NodeID,
						Relation:        e.relation,
					})
				}
			}
			return nil
		})
		if err != nil {
			return nil, fmt.Errorf("scan namespace %s: %w", nsName, err)
		}
	}

	return crossEdges, nil
}

// edgeRef is an internal type for extracting edge targets from frontmatter.
type edgeRef struct {
	target   string
	relation string
}

// extractEdgesFromFrontmatter does a lightweight YAML parse to extract edge targets.
func extractEdgesFromFrontmatter(data []byte) []edgeRef {
	content := string(data)
	if !strings.HasPrefix(content, "---") {
		return nil
	}
	end := strings.Index(content[3:], "---")
	if end < 0 {
		return nil
	}
	yamlBlock := content[3 : end+3]

	var fm struct {
		Edges []struct {
			Target   string `yaml:"target"`
			Relation string `yaml:"relation"`
		} `yaml:"edges"`
	}
	if err := yaml.Unmarshal([]byte(yamlBlock), &fm); err != nil {
		return nil
	}

	refs := make([]edgeRef, 0, len(fm.Edges))
	for _, e := range fm.Edges {
		if e.Target != "" {
			refs = append(refs, edgeRef{target: e.Target, relation: e.Relation})
		}
	}
	return refs
}

// formatRelationList formats a slice of relation strings for display.
func formatRelationList(relations []string) string {
	quoted := make([]string, len(relations))
	for i, r := range relations {
		quoted[i] = "`" + r + "`"
	}
	return strings.Join(quoted, ", ")
}

// --- Cross-Vault Operations ---

// ValidateCrossVaultEdge checks whether a cross-vault edge is allowed by the
// bridge configuration. Returns nil if allowed, error if not.
func (m *Manager) ValidateCrossVaultEdge(sourceVaultID, targetVaultID, relation string) error {
	// Check all cross-vault bridges for a match.
	for _, b := range m.CrossVaultBridges {
		srcMatch := (b.SourceVaultID == sourceVaultID && b.TargetVaultID == targetVaultID)
		tgtMatch := (b.SourceVaultID == targetVaultID && b.TargetVaultID == sourceVaultID)
		if srcMatch || tgtMatch {
			// Check allowed relations.
			for _, r := range b.AllowedRelations {
				if r == relation {
					return nil
				}
			}
			return fmt.Errorf("relation %q not allowed by cross-vault bridge %s <-> %s", relation, sourceVaultID, targetVaultID)
		}
	}
	return fmt.Errorf("no cross-vault bridge between %s and %s", sourceVaultID, targetVaultID)
}

// CreateCrossVaultBridge creates a cross-vault bridge and writes manifests to
// both the local and remote vaults.
func CreateCrossVaultBridge(localVaultDir, remoteVaultDir string, allowedRelations []string) (*Bridge, error) {
	// Load configs from both vaults to get vault_ids.
	localCfg, err := config.Load(localVaultDir)
	if err != nil {
		return nil, fmt.Errorf("load local vault config: %w", err)
	}
	remoteCfg, err := config.Load(remoteVaultDir)
	if err != nil {
		return nil, fmt.Errorf("load remote vault config: %w", err)
	}

	if localCfg.VaultID == "" {
		return nil, fmt.Errorf("local vault at %s has no vault_id set; run 'marmot configure' first", localVaultDir)
	}
	if remoteCfg.VaultID == "" {
		return nil, fmt.Errorf("remote vault at %s has no vault_id set; run 'marmot configure' in that project first", remoteVaultDir)
	}

	absLocal, err := filepath.Abs(localVaultDir)
	if err != nil {
		return nil, fmt.Errorf("resolve local path: %w", err)
	}
	absRemote, err := filepath.Abs(remoteVaultDir)
	if err != nil {
		return nil, fmt.Errorf("resolve remote path: %w", err)
	}

	bridge := &Bridge{
		Source:           localCfg.VaultID,
		Target:           remoteCfg.VaultID,
		SourceVaultPath:  absLocal,
		TargetVaultPath:  absRemote,
		SourceVaultID:    localCfg.VaultID,
		TargetVaultID:    remoteCfg.VaultID,
		Created:          time.Now().UTC().Format(time.RFC3339),
		AllowedRelations: allowedRelations,
	}

	// Two-phase write: write temp files to both vaults first, then rename both.
	localFile, localTmp, err := writeCrossVaultBridgeTemp(absLocal, bridge)
	if err != nil {
		return nil, fmt.Errorf("write bridge to local vault: %w", err)
	}
	remoteFile, remoteTmp, err := writeCrossVaultBridgeTemp(absRemote, bridge)
	if err != nil {
		_ = os.Remove(localTmp)
		return nil, fmt.Errorf("write bridge to remote vault: %w", err)
	}

	// Phase 2: rename both (atomic on most filesystems).
	if err := os.Rename(localTmp, localFile); err != nil {
		_ = os.Remove(localTmp)
		_ = os.Remove(remoteTmp)
		return nil, fmt.Errorf("commit bridge to local vault: %w", err)
	}
	if err := os.Rename(remoteTmp, remoteFile); err != nil {
		// Roll back local by removing the committed file.
		_ = os.Remove(localFile)
		_ = os.Remove(remoteTmp)
		return nil, fmt.Errorf("commit bridge to remote vault: %w", err)
	}

	// Auto-register both vaults in the global routing table (best-effort).
	if rt, rtErr := routes.Load(); rtErr == nil {
		rt.Set(localCfg.VaultID, absLocal)
		rt.Set(remoteCfg.VaultID, absRemote)
		_ = routes.Save(rt)
	}

	return bridge, nil
}

// writeCrossVaultBridgeTemp writes a cross-vault bridge manifest to a temp file
// in the vault's _bridges/ directory. Returns (finalPath, tmpPath, error).
// Caller is responsible for renaming tmpPath to finalPath.
func writeCrossVaultBridgeTemp(vaultDir string, b *Bridge) (string, string, error) {
	bridgeDir := filepath.Join(vaultDir, "_bridges")
	if err := os.MkdirAll(bridgeDir, 0o755); err != nil {
		return "", "", fmt.Errorf("create bridges dir: %w", err)
	}

	filename := fmt.Sprintf("@%s--@%s.md", b.SourceVaultID, b.TargetVaultID)

	yamlBytes, err := yaml.Marshal(b)
	if err != nil {
		return "", "", fmt.Errorf("marshal bridge: %w", err)
	}

	var buf strings.Builder
	buf.WriteString("---\n")
	buf.Write(yamlBytes)
	buf.WriteString("---\n")
	buf.WriteString(fmt.Sprintf("# Cross-Vault Bridge: %s <-> %s\n", b.SourceVaultID, b.TargetVaultID))

	path := filepath.Join(bridgeDir, filename)
	tmpPath := path + ".tmp"
	if err := os.WriteFile(tmpPath, []byte(buf.String()), 0o644); err != nil {
		return "", "", fmt.Errorf("write tmp bridge: %w", err)
	}
	return path, tmpPath, nil
}
