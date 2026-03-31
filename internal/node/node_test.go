package node

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// sampleMarkdown is a representative Obsidian-compatible node file.
const sampleMarkdown = `---
id: auth/login
type: function
namespace: project-alpha
status: active
source:
  path: src/auth/login.ts
  lines: [15, 42]
  hash: a3f8b2c1d4e5f6
edges:
  - target: auth/validate_token
    relation: calls
  - target: db/users/find
    relation: reads
  - target: auth/module
    relation: contains
  - target: project-beta/api/session
    relation: cross_project
---

Handles user login with OAuth2 flow. Validates credentials,
generates JWT token, and establishes session.

## Relationships

- **calls** [[auth/validate_token]]
- **reads** [[db/users/find]]
- **contains** [[auth/module]]
- **cross_project** [[project-beta/api/session]]

## Context

` + "```typescript\nexport async function login(credentials: Credentials): Promise<Session> {\n  const user = await findUser(credentials.email);\n  return createSession(user);\n}\n```"

// ---------------------------------------------------------------------------
// Parser tests
// ---------------------------------------------------------------------------

func TestParseNode(t *testing.T) {
	n, err := ParseNode([]byte(sampleMarkdown), "test.md")
	if err != nil {
		t.Fatalf("ParseNode: %v", err)
	}

	if n.ID != "auth/login" {
		t.Errorf("ID = %q, want %q", n.ID, "auth/login")
	}
	if n.Type != "function" {
		t.Errorf("Type = %q, want %q", n.Type, "function")
	}
	if n.Namespace != "project-alpha" {
		t.Errorf("Namespace = %q, want %q", n.Namespace, "project-alpha")
	}
	if n.Status != "active" {
		t.Errorf("Status = %q, want %q", n.Status, "active")
	}
	if n.Source.Path != "src/auth/login.ts" {
		t.Errorf("Source.Path = %q, want %q", n.Source.Path, "src/auth/login.ts")
	}
	if n.Source.Lines != [2]int{15, 42} {
		t.Errorf("Source.Lines = %v, want [15, 42]", n.Source.Lines)
	}
	if n.Source.Hash != "a3f8b2c1d4e5f6" {
		t.Errorf("Source.Hash = %q, want %q", n.Source.Hash, "a3f8b2c1d4e5f6")
	}

	if len(n.Edges) != 4 {
		t.Fatalf("len(Edges) = %d, want 4", len(n.Edges))
	}

	// Check edge parsing.
	wantEdges := []struct {
		target   string
		relation EdgeRelation
		class    EdgeClass
	}{
		{"auth/validate_token", Calls, Behavioral},
		{"db/users/find", Reads, Behavioral},
		{"auth/module", Contains, Structural},
		{"project-beta/api/session", CrossProject, Behavioral},
	}
	for i, want := range wantEdges {
		got := n.Edges[i]
		if got.Target != want.target {
			t.Errorf("Edge[%d].Target = %q, want %q", i, got.Target, want.target)
		}
		if got.Relation != want.relation {
			t.Errorf("Edge[%d].Relation = %q, want %q", i, got.Relation, want.relation)
		}
		if got.Class != want.class {
			t.Errorf("Edge[%d].Class = %q, want %q", i, got.Class, want.class)
		}
	}

	// Summary.
	if !strings.Contains(n.Summary, "OAuth2") {
		t.Errorf("Summary missing expected content, got: %q", n.Summary)
	}

	// Context.
	if !strings.Contains(n.Context, "async function login") {
		t.Errorf("Context missing expected content, got: %q", n.Context)
	}
}

func TestParseNode_EmptyFile(t *testing.T) {
	_, err := ParseNode([]byte(""), "empty.md")
	if err == nil {
		t.Fatal("expected error for empty file")
	}
}

func TestParseNode_NoFrontmatter(t *testing.T) {
	_, err := ParseNode([]byte("# Just a heading\nSome content"), "no-fm.md")
	if err == nil {
		t.Fatal("expected error for missing frontmatter")
	}
}

func TestParseNode_BadYAML(t *testing.T) {
	data := "---\n[invalid yaml\n---\nBody text"
	_, err := ParseNode([]byte(data), "bad.md")
	if err == nil {
		t.Fatal("expected error for malformed YAML")
	}
}

func TestParseNode_MissingClosingDelimiter(t *testing.T) {
	data := "---\nid: test\nNo closing delimiter"
	_, err := ParseNode([]byte(data), "no-close.md")
	if err == nil {
		t.Fatal("expected error for missing closing ---")
	}
}

func TestParseNode_NoEdges(t *testing.T) {
	data := "---\nid: simple\ntype: concept\nnamespace: ns\nstatus: active\n---\nJust a summary."
	n, err := ParseNode([]byte(data), "simple.md")
	if err != nil {
		t.Fatalf("ParseNode: %v", err)
	}
	if len(n.Edges) != 0 {
		t.Errorf("expected 0 edges, got %d", len(n.Edges))
	}
	if n.Summary != "Just a summary." {
		t.Errorf("Summary = %q, want %q", n.Summary, "Just a summary.")
	}
}

func TestParseNode_NoContext(t *testing.T) {
	data := "---\nid: nocontext\ntype: concept\nnamespace: ns\nstatus: active\n---\nSummary only."
	n, err := ParseNode([]byte(data), "nocontext.md")
	if err != nil {
		t.Fatalf("ParseNode: %v", err)
	}
	if n.Context != "" {
		t.Errorf("Context = %q, want empty", n.Context)
	}
}

// ---------------------------------------------------------------------------
// Edge classification tests
// ---------------------------------------------------------------------------

func TestClassifyRelation(t *testing.T) {
	structural := []string{"contains", "imports", "extends", "implements"}
	for _, r := range structural {
		if got := ClassifyRelation(r); got != Structural {
			t.Errorf("ClassifyRelation(%q) = %q, want %q", r, got, Structural)
		}
	}

	behavioral := []string{"calls", "reads", "writes", "references", "cross_project", "associated"}
	for _, r := range behavioral {
		if got := ClassifyRelation(r); got != Behavioral {
			t.Errorf("ClassifyRelation(%q) = %q, want %q", r, got, Behavioral)
		}
	}

	// Unknown defaults to behavioral.
	if got := ClassifyRelation("unknown_type"); got != Behavioral {
		t.Errorf("ClassifyRelation(%q) = %q, want %q", "unknown_type", got, Behavioral)
	}
}

// ---------------------------------------------------------------------------
// Wikilink extraction tests
// ---------------------------------------------------------------------------

func TestExtractWikilinks(t *testing.T) {
	body := `Some text [[auth/login]] and [[db/users/find]].
More [[auth/login]] duplicated.
Also [[project-beta/api/session]].`

	links := ExtractWikilinks(body)
	want := []string{"auth/login", "db/users/find", "project-beta/api/session"}
	if len(links) != len(want) {
		t.Fatalf("len(links) = %d, want %d", len(links), len(want))
	}
	for i, w := range want {
		if links[i] != w {
			t.Errorf("links[%d] = %q, want %q", i, links[i], w)
		}
	}
}

func TestExtractWikilinks_Empty(t *testing.T) {
	links := ExtractWikilinks("No links here.")
	if len(links) != 0 {
		t.Errorf("expected 0 links, got %d", len(links))
	}
}

// ---------------------------------------------------------------------------
// Writer tests
// ---------------------------------------------------------------------------

func TestRenderNode(t *testing.T) {
	n := &Node{
		ID:        "auth/login",
		Type:      "function",
		Namespace: "project-alpha",
		Status:    "active",
		Source: Source{
			Path:  "src/auth/login.ts",
			Lines: [2]int{15, 42},
			Hash:  "abc123",
		},
		Edges: []Edge{
			{Target: "auth/validate_token", Relation: Calls, Class: Behavioral},
			{Target: "db/users/find", Relation: Reads, Class: Behavioral},
		},
		Summary: "Handles user login with OAuth2 flow.",
		Context: "```typescript\nfunction login() {}\n```",
	}

	data, err := RenderNode(n)
	if err != nil {
		t.Fatalf("RenderNode: %v", err)
	}

	s := string(data)

	// Check frontmatter delimiters.
	if !strings.HasPrefix(s, "---\n") {
		t.Error("output must start with ---")
	}
	if !strings.Contains(s, "\n---\n") {
		t.Error("output must contain closing ---")
	}

	// Check key content.
	checks := []string{
		"id: auth/login",
		"type: function",
		"namespace: project-alpha",
		"status: active",
		"path: src/auth/login.ts",
		"hash: abc123",
		"target: auth/validate_token",
		"relation: calls",
		"## Relationships",
		"[[auth/validate_token]]",
		"[[db/users/find]]",
		"## Context",
		"function login()",
		"Handles user login with OAuth2 flow.",
	}
	for _, check := range checks {
		if !strings.Contains(s, check) {
			t.Errorf("output missing %q", check)
		}
	}
}

func TestRenderNode_Nil(t *testing.T) {
	_, err := RenderNode(nil)
	if err == nil {
		t.Fatal("expected error for nil node")
	}
}

func TestRenderNode_MinimalNode(t *testing.T) {
	n := &Node{
		ID:        "simple",
		Type:      "concept",
		Namespace: "ns",
		Status:    "active",
	}
	data, err := RenderNode(n)
	if err != nil {
		t.Fatalf("RenderNode: %v", err)
	}
	s := string(data)
	if !strings.Contains(s, "id: simple") {
		t.Error("missing id")
	}
	// No relationships or context sections for a minimal node.
	if strings.Contains(s, "## Relationships") {
		t.Error("unexpected Relationships section for node with no edges")
	}
	if strings.Contains(s, "## Context") {
		t.Error("unexpected Context section for node with no context")
	}
}

// ---------------------------------------------------------------------------
// Roundtrip test
// ---------------------------------------------------------------------------

func TestRoundtrip(t *testing.T) {
	original := &Node{
		ID:        "auth/login",
		Type:      "function",
		Namespace: "project-alpha",
		Status:    "active",
		Source: Source{
			Path:  "src/auth/login.ts",
			Lines: [2]int{15, 42},
			Hash:  "a3f8b2c1d4e5f6",
		},
		Edges: []Edge{
			{Target: "auth/validate_token", Relation: Calls, Class: Behavioral},
			{Target: "db/users/find", Relation: Reads, Class: Behavioral},
			{Target: "auth/module", Relation: Contains, Class: Structural},
		},
		Summary: "Handles user login with OAuth2 flow.",
		Context: "```typescript\nfunction login() {}\n```",
	}

	// Write.
	data, err := RenderNode(original)
	if err != nil {
		t.Fatalf("RenderNode: %v", err)
	}

	// Parse back.
	parsed, err := ParseNode(data, "roundtrip.md")
	if err != nil {
		t.Fatalf("ParseNode after render: %v", err)
	}

	// Verify key fields.
	if parsed.ID != original.ID {
		t.Errorf("ID: got %q, want %q", parsed.ID, original.ID)
	}
	if parsed.Type != original.Type {
		t.Errorf("Type: got %q, want %q", parsed.Type, original.Type)
	}
	if parsed.Namespace != original.Namespace {
		t.Errorf("Namespace: got %q, want %q", parsed.Namespace, original.Namespace)
	}
	if parsed.Status != original.Status {
		t.Errorf("Status: got %q, want %q", parsed.Status, original.Status)
	}
	if parsed.Source.Path != original.Source.Path {
		t.Errorf("Source.Path: got %q, want %q", parsed.Source.Path, original.Source.Path)
	}
	if parsed.Source.Lines != original.Source.Lines {
		t.Errorf("Source.Lines: got %v, want %v", parsed.Source.Lines, original.Source.Lines)
	}
	if parsed.Source.Hash != original.Source.Hash {
		t.Errorf("Source.Hash: got %q, want %q", parsed.Source.Hash, original.Source.Hash)
	}

	if len(parsed.Edges) != len(original.Edges) {
		t.Fatalf("Edges: got %d, want %d", len(parsed.Edges), len(original.Edges))
	}
	for i := range original.Edges {
		if parsed.Edges[i].Target != original.Edges[i].Target {
			t.Errorf("Edge[%d].Target: got %q, want %q", i, parsed.Edges[i].Target, original.Edges[i].Target)
		}
		if parsed.Edges[i].Relation != original.Edges[i].Relation {
			t.Errorf("Edge[%d].Relation: got %q, want %q", i, parsed.Edges[i].Relation, original.Edges[i].Relation)
		}
		if parsed.Edges[i].Class != original.Edges[i].Class {
			t.Errorf("Edge[%d].Class: got %q, want %q", i, parsed.Edges[i].Class, original.Edges[i].Class)
		}
	}

	if parsed.Summary != original.Summary {
		t.Errorf("Summary: got %q, want %q", parsed.Summary, original.Summary)
	}
	if parsed.Context != original.Context {
		t.Errorf("Context: got %q, want %q", parsed.Context, original.Context)
	}
}

// ---------------------------------------------------------------------------
// Store tests
// ---------------------------------------------------------------------------

func TestStore_SaveAndLoad(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)

	node := &Node{
		ID:        "auth/login",
		Type:      "function",
		Namespace: "project-alpha",
		Status:    "active",
		Source: Source{
			Path:  "src/auth/login.ts",
			Lines: [2]int{15, 42},
			Hash:  "abc123",
		},
		Edges: []Edge{
			{Target: "auth/validate_token", Relation: Calls, Class: Behavioral},
		},
		Summary: "Login handler.",
		Context: "```ts\nfunction login() {}\n```",
	}

	// Save.
	if err := store.SaveNode(node); err != nil {
		t.Fatalf("SaveNode: %v", err)
	}

	// Verify file exists.
	path := store.NodePath("auth/login")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("file does not exist after SaveNode: %v", err)
	}

	// Load.
	loaded, err := store.LoadNode(path)
	if err != nil {
		t.Fatalf("LoadNode: %v", err)
	}

	if loaded.ID != node.ID {
		t.Errorf("ID = %q, want %q", loaded.ID, node.ID)
	}
	if loaded.Summary != node.Summary {
		t.Errorf("Summary = %q, want %q", loaded.Summary, node.Summary)
	}
}

func TestStore_DeleteNode(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)

	node := &Node{
		ID:        "to-delete",
		Type:      "concept",
		Namespace: "ns",
		Status:    "active",
		Summary:   "Will be deleted.",
	}

	if err := store.SaveNode(node); err != nil {
		t.Fatalf("SaveNode: %v", err)
	}

	if err := store.DeleteNode("to-delete"); err != nil {
		t.Fatalf("DeleteNode: %v", err)
	}

	path := store.NodePath("to-delete")
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("file still exists after DeleteNode")
	}
}

func TestStore_ListNodes(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)

	nodes := []*Node{
		{ID: "auth/login", Type: "function", Namespace: "ns", Status: "active", Summary: "Login."},
		{ID: "auth/logout", Type: "function", Namespace: "ns", Status: "active", Summary: "Logout."},
		{ID: "db/users", Type: "module", Namespace: "ns", Status: "active", Summary: "Users DB."},
	}

	for _, n := range nodes {
		if err := store.SaveNode(n); err != nil {
			t.Fatalf("SaveNode(%s): %v", n.ID, err)
		}
	}

	metas, err := store.ListNodes()
	if err != nil {
		t.Fatalf("ListNodes: %v", err)
	}

	if len(metas) != 3 {
		t.Fatalf("ListNodes returned %d, want 3", len(metas))
	}

	// Verify IDs are present (order is filesystem-walk dependent).
	ids := make(map[string]bool)
	for _, m := range metas {
		ids[m.ID] = true
		if m.Status != "active" {
			t.Errorf("meta %q: Status = %q, want %q", m.ID, m.Status, "active")
		}
	}
	for _, n := range nodes {
		if !ids[n.ID] {
			t.Errorf("ListNodes missing node %q", n.ID)
		}
	}
}

func TestStore_ListNodes_SkipUnderscore(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)

	// Save a regular node.
	regular := &Node{ID: "regular", Type: "concept", Namespace: "ns", Status: "active", Summary: "Reg."}
	if err := store.SaveNode(regular); err != nil {
		t.Fatal(err)
	}

	// Write an _summary.md that should be skipped.
	summaryPath := filepath.Join(dir, "_summary.md")
	summaryData := "---\nid: _summary\ntype: summary\nnamespace: ns\nstatus: active\n---\nSummary."
	if err := os.WriteFile(summaryPath, []byte(summaryData), 0o644); err != nil {
		t.Fatal(err)
	}

	metas, err := store.ListNodes()
	if err != nil {
		t.Fatal(err)
	}
	if len(metas) != 1 {
		t.Fatalf("ListNodes returned %d, want 1 (should skip _summary.md)", len(metas))
	}
	if metas[0].ID != "regular" {
		t.Errorf("expected regular, got %q", metas[0].ID)
	}
}

// ---------------------------------------------------------------------------
// ID derivation tests
// ---------------------------------------------------------------------------

func TestNodePath(t *testing.T) {
	store := NewStore("/vault/project-alpha")
	got := store.NodePath("auth/login")
	want := filepath.Join("/vault/project-alpha", "auth/login.md")
	if got != want {
		t.Errorf("NodePath = %q, want %q", got, want)
	}
}

func TestIDFromPath(t *testing.T) {
	store := NewStore("/vault/project-alpha")

	tests := []struct {
		path string
		want string
	}{
		{"/vault/project-alpha/auth/login.md", "auth/login"},
		{"/vault/project-alpha/simple.md", "simple"},
		{"/vault/project-alpha/a/b/c.md", "a/b/c"},
	}

	for _, tt := range tests {
		got, err := store.IDFromPath(tt.path)
		if err != nil {
			t.Errorf("IDFromPath(%q): %v", tt.path, err)
			continue
		}
		if got != tt.want {
			t.Errorf("IDFromPath(%q) = %q, want %q", tt.path, got, tt.want)
		}
	}
}

func TestIDFromPath_OutsideBase(t *testing.T) {
	store := NewStore("/vault/project-alpha")
	_, err := store.IDFromPath("/other/place/file.md")
	if err == nil {
		t.Fatal("expected error for path outside base")
	}
}

// ---------------------------------------------------------------------------
// Atomic write verification
// ---------------------------------------------------------------------------

func TestAtomicWrite_NoPartialFile(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)

	node := &Node{
		ID:        "atomic-test",
		Type:      "concept",
		Namespace: "ns",
		Status:    "active",
		Summary:   "Testing atomic writes.",
	}

	if err := store.SaveNode(node); err != nil {
		t.Fatalf("SaveNode: %v", err)
	}

	// Read the file and verify content is complete.
	data, err := os.ReadFile(store.NodePath("atomic-test"))
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}

	if !strings.Contains(string(data), "id: atomic-test") {
		t.Error("file content incomplete after atomic write")
	}

	// Ensure no temp files remain.
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Errorf("temp file %q still exists", e.Name())
		}
	}
}

// ---------------------------------------------------------------------------
// ParseNodeMeta tests
// ---------------------------------------------------------------------------

func TestParseNodeMeta(t *testing.T) {
	data := []byte("---\nid: auth/login\ntype: function\nnamespace: project-alpha\nstatus: active\n---\nSummary text.")
	meta, err := ParseNodeMeta(data, "test.md")
	if err != nil {
		t.Fatalf("ParseNodeMeta: %v", err)
	}
	if meta.ID != "auth/login" {
		t.Errorf("ID = %q, want %q", meta.ID, "auth/login")
	}
	if meta.Type != "function" {
		t.Errorf("Type = %q, want %q", meta.Type, "function")
	}
	if meta.Namespace != "project-alpha" {
		t.Errorf("Namespace = %q, want %q", meta.Namespace, "project-alpha")
	}
	if meta.Status != "active" {
		t.Errorf("Status = %q, want %q", meta.Status, "active")
	}
}
