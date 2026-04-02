package namespace

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

func TestParseNamespace(t *testing.T) {
	data := []byte(`---
name: project-alpha
root_path: /Users/dev/projects/alpha
created: "2026-03-30T10:00:00Z"
settings:
  auto_index: true
  source_globs:
    - "src/**/*.ts"
  embedding_model: text-embedding-3-small
---

Namespace configuration for project-alpha.
`)

	ns, err := parseNamespace(data)
	if err != nil {
		t.Fatalf("parseNamespace: %v", err)
	}
	if ns.Name != "project-alpha" {
		t.Errorf("Name = %q, want %q", ns.Name, "project-alpha")
	}
	if ns.RootPath != "/Users/dev/projects/alpha" {
		t.Errorf("RootPath = %q, want %q", ns.RootPath, "/Users/dev/projects/alpha")
	}
	if !ns.Settings.AutoIndex {
		t.Error("Settings.AutoIndex = false, want true")
	}
	if len(ns.Settings.SourceGlobs) != 1 || ns.Settings.SourceGlobs[0] != "src/**/*.ts" {
		t.Errorf("SourceGlobs = %v, want [src/**/*.ts]", ns.Settings.SourceGlobs)
	}
	if ns.Settings.EmbeddingModel != "text-embedding-3-small" {
		t.Errorf("EmbeddingModel = %q, want %q", ns.Settings.EmbeddingModel, "text-embedding-3-small")
	}
}

func TestParseNamespaceMissingName(t *testing.T) {
	data := []byte(`---
root_path: /tmp/test
---
`)
	_, err := parseNamespace(data)
	if err == nil {
		t.Fatal("expected error for missing name")
	}
}

func TestParseNamespaceNoFrontmatter(t *testing.T) {
	data := []byte("Just some text, no frontmatter")
	_, err := parseNamespace(data)
	if err == nil {
		t.Fatal("expected error for missing frontmatter")
	}
}

func TestCreateAndLoadNamespace(t *testing.T) {
	dir := t.TempDir()
	ns, err := CreateNamespace(dir, "test-ns", "/tmp/test")
	if err != nil {
		t.Fatalf("CreateNamespace: %v", err)
	}
	if ns.Name != "test-ns" {
		t.Errorf("Name = %q, want %q", ns.Name, "test-ns")
	}

	loaded, err := LoadNamespace(filepath.Join(dir, "test-ns"))
	if err != nil {
		t.Fatalf("LoadNamespace: %v", err)
	}
	if loaded.Name != "test-ns" {
		t.Errorf("loaded Name = %q, want %q", loaded.Name, "test-ns")
	}
	if loaded.RootPath != "/tmp/test" {
		t.Errorf("loaded RootPath = %q, want %q", loaded.RootPath, "/tmp/test")
	}
}

func TestCreateNamespaceInvalidNames(t *testing.T) {
	dir := t.TempDir()

	cases := []string{"", ".hidden", "_private", "a/b", "a\\b"}
	for _, name := range cases {
		_, err := CreateNamespace(dir, name, "")
		if err == nil {
			t.Errorf("CreateNamespace(%q) should have returned error", name)
		}
	}
}

func TestParseBridge(t *testing.T) {
	data := []byte(`---
source: project-alpha
target: project-beta
created: "2026-03-30T10:00:00Z"
allowed_relations:
  - cross_project
  - calls
  - reads
---

Bridge between project-alpha and project-beta.
`)

	b, err := parseBridge(data)
	if err != nil {
		t.Fatalf("parseBridge: %v", err)
	}
	if b.Source != "project-alpha" {
		t.Errorf("Source = %q, want %q", b.Source, "project-alpha")
	}
	if b.Target != "project-beta" {
		t.Errorf("Target = %q, want %q", b.Target, "project-beta")
	}
	if len(b.AllowedRelations) != 3 {
		t.Fatalf("AllowedRelations length = %d, want 3", len(b.AllowedRelations))
	}
	if b.AllowedRelations[0] != "cross_project" {
		t.Errorf("AllowedRelations[0] = %q, want %q", b.AllowedRelations[0], "cross_project")
	}
}

func TestParseBridgeMissingFields(t *testing.T) {
	data := []byte(`---
source: alpha
---
`)
	_, err := parseBridge(data)
	if err == nil {
		t.Fatal("expected error for missing target")
	}
}

func TestCreateAndLoadBridge(t *testing.T) {
	dir := t.TempDir()
	b, err := CreateBridge(dir, "ns-a", "ns-b", []string{"calls", "reads"})
	if err != nil {
		t.Fatalf("CreateBridge: %v", err)
	}
	if b.Source != "ns-a" {
		t.Errorf("Source = %q, want %q", b.Source, "ns-a")
	}

	loaded, err := LoadBridge(filepath.Join(dir, "_bridges", "ns-a--ns-b.md"))
	if err != nil {
		t.Fatalf("LoadBridge: %v", err)
	}
	if loaded.Source != "ns-a" || loaded.Target != "ns-b" {
		t.Errorf("loaded bridge = %s->%s, want ns-a->ns-b", loaded.Source, loaded.Target)
	}
	if len(loaded.AllowedRelations) != 2 {
		t.Fatalf("loaded AllowedRelations length = %d, want 2", len(loaded.AllowedRelations))
	}
}

func TestCreateBridgeSameNamespace(t *testing.T) {
	dir := t.TempDir()
	_, err := CreateBridge(dir, "ns-a", "ns-a", []string{"calls"})
	if err == nil {
		t.Fatal("expected error for same-namespace bridge")
	}
}

func TestValidateCrossNamespaceEdge(t *testing.T) {
	dir := t.TempDir()

	// Create namespaces with _namespace.md.
	writeMinimalNamespace(t, dir, "alpha")
	writeMinimalNamespace(t, dir, "beta")

	// Create bridge.
	CreateBridge(dir, "alpha", "beta", []string{"calls", "cross_project"})

	m, err := NewManager(dir)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	// Allowed relation.
	if err := m.ValidateCrossNamespaceEdge("alpha", "beta", "calls"); err != nil {
		t.Errorf("ValidateCrossNamespaceEdge(calls) = %v, want nil", err)
	}

	// Allowed in reverse direction.
	if err := m.ValidateCrossNamespaceEdge("beta", "alpha", "cross_project"); err != nil {
		t.Errorf("ValidateCrossNamespaceEdge(cross_project, reverse) = %v, want nil", err)
	}

	// Disallowed relation.
	if err := m.ValidateCrossNamespaceEdge("alpha", "beta", "writes"); err == nil {
		t.Error("ValidateCrossNamespaceEdge(writes) should have returned error")
	}

	// No bridge exists.
	writeMinimalNamespace(t, dir, "gamma")
	m, _ = NewManager(dir)
	if err := m.ValidateCrossNamespaceEdge("alpha", "gamma", "calls"); err == nil {
		t.Error("ValidateCrossNamespaceEdge(no bridge) should have returned error")
	}
}

func TestValidateSameNamespaceEdge(t *testing.T) {
	dir := t.TempDir()
	writeMinimalNamespace(t, dir, "alpha")
	m, _ := NewManager(dir)
	// Same-namespace edges always pass.
	if err := m.ValidateCrossNamespaceEdge("alpha", "alpha", "anything"); err != nil {
		t.Errorf("same-namespace edge should pass: %v", err)
	}
}

func TestParseQualifiedID(t *testing.T) {
	dir := t.TempDir()
	writeMinimalNamespace(t, dir, "alpha")
	writeMinimalNamespace(t, dir, "beta")
	m, _ := NewManager(dir)

	// Cross-namespace reference.
	qid := m.ParseQualifiedID("beta/api/session", "alpha")
	if qid.Namespace != "beta" || qid.NodeID != "api/session" {
		t.Errorf("ParseQualifiedID(beta/api/session) = %+v, want {beta, api/session}", qid)
	}

	// Local reference (auth/login stays local when first component is not a known namespace).
	qid = m.ParseQualifiedID("auth/login", "alpha")
	if qid.Namespace != "alpha" || qid.NodeID != "auth/login" {
		t.Errorf("ParseQualifiedID(auth/login) = %+v, want {alpha, auth/login}", qid)
	}

	// Reference to own namespace treated as local.
	qid = m.ParseQualifiedID("alpha/auth/login", "alpha")
	if qid.Namespace != "alpha" || qid.NodeID != "alpha/auth/login" {
		t.Errorf("ParseQualifiedID(alpha/auth/login, from alpha) = %+v, want {alpha, alpha/auth/login}", qid)
	}

	// Simple ID without slashes.
	qid = m.ParseQualifiedID("readme", "alpha")
	if qid.Namespace != "alpha" || qid.NodeID != "readme" {
		t.Errorf("ParseQualifiedID(readme) = %+v, want {alpha, readme}", qid)
	}
}

func TestFormatQualifiedID(t *testing.T) {
	// Cross-namespace.
	if got := FormatQualifiedID("beta", "api/session", "alpha"); got != "beta/api/session" {
		t.Errorf("FormatQualifiedID = %q, want %q", got, "beta/api/session")
	}
	// Same namespace.
	if got := FormatQualifiedID("alpha", "auth/login", "alpha"); got != "auth/login" {
		t.Errorf("FormatQualifiedID = %q, want %q", got, "auth/login")
	}
}

func TestListNamespaces(t *testing.T) {
	dir := t.TempDir()
	writeMinimalNamespace(t, dir, "alpha")
	writeMinimalNamespace(t, dir, "beta")
	os.MkdirAll(filepath.Join(dir, ".hidden"), 0o755)
	os.MkdirAll(filepath.Join(dir, "_bridges"), 0o755)
	// A bare directory without _namespace.md should NOT appear as a namespace.
	os.MkdirAll(filepath.Join(dir, "plain-dir"), 0o755)
	os.WriteFile(filepath.Join(dir, "not-a-dir.md"), []byte("file"), 0o644)

	m, err := NewManager(dir)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	names := m.ListNamespaces()
	sort.Strings(names)
	if len(names) != 2 {
		t.Fatalf("ListNamespaces length = %d, want 2, got %v", len(names), names)
	}
	if names[0] != "alpha" || names[1] != "beta" {
		t.Errorf("ListNamespaces = %v, want [alpha, beta]", names)
	}
}

func TestNewManagerEmptyDir(t *testing.T) {
	dir := t.TempDir()
	m, err := NewManager(dir)
	if err != nil {
		t.Fatalf("NewManager on empty dir: %v", err)
	}
	if len(m.Namespaces) != 0 {
		t.Errorf("expected 0 namespaces, got %d", len(m.Namespaces))
	}
}

func TestDiscoverCrossNamespaceEdges(t *testing.T) {
	dir := t.TempDir()

	// Create two namespaces with _namespace.md.
	writeMinimalNamespace(t, dir, "alpha")
	writeMinimalNamespace(t, dir, "beta")
	alphaDir := filepath.Join(dir, "alpha")
	betaDir := filepath.Join(dir, "beta")
	os.MkdirAll(filepath.Join(alphaDir, "auth"), 0o755)
	os.MkdirAll(filepath.Join(betaDir, "api"), 0o755)

	// Write a node in alpha that references beta.
	nodeContent := `---
id: auth/login
type: function
namespace: alpha
status: active
edges:
  - target: beta/api/session
    relation: cross_project
  - target: auth/validate
    relation: calls
---

Login function.
`
	os.WriteFile(filepath.Join(alphaDir, "auth", "login.md"), []byte(nodeContent), 0o644)

	// Write a node in beta (no cross-namespace edges).
	betaContent := `---
id: api/session
type: function
namespace: beta
status: active
edges: []
---

Session endpoint.
`
	os.WriteFile(filepath.Join(betaDir, "api", "session.md"), []byte(betaContent), 0o644)

	m, err := NewManager(dir)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	crossEdges, err := m.DiscoverCrossNamespaceEdges()
	if err != nil {
		t.Fatalf("DiscoverCrossNamespaceEdges: %v", err)
	}

	if len(crossEdges) != 1 {
		t.Fatalf("expected 1 cross-namespace edge, got %d", len(crossEdges))
	}

	e := crossEdges[0]
	if e.SourceNamespace != "alpha" || e.SourceNodeID != "auth/login" {
		t.Errorf("source = %s/%s, want alpha/auth/login", e.SourceNamespace, e.SourceNodeID)
	}
	if e.TargetNamespace != "beta" || e.TargetNodeID != "api/session" {
		t.Errorf("target = %s/%s, want beta/api/session", e.TargetNamespace, e.TargetNodeID)
	}
	if e.Relation != "cross_project" {
		t.Errorf("relation = %q, want %q", e.Relation, "cross_project")
	}
}

func TestBridgeKey(t *testing.T) {
	if got := BridgeKey("alpha", "beta"); got != "alpha--beta" {
		t.Errorf("BridgeKey = %q, want %q", got, "alpha--beta")
	}
}

// writeMinimalNamespace writes a minimal _namespace.md for testing.
func writeMinimalNamespace(t *testing.T, dir, name string) {
	t.Helper()
	nsDir := filepath.Join(dir, name)
	os.MkdirAll(nsDir, 0o755)
	content := "---\nname: " + name + "\n---\n\nNamespace " + name + ".\n"
	if err := os.WriteFile(filepath.Join(nsDir, "_namespace.md"), []byte(content), 0o644); err != nil {
		t.Fatalf("writeMinimalNamespace(%s): %v", name, err)
	}
}

// Suppress unused import warnings.
var _ = strings.Contains
