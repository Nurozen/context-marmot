package codemode

import (
	"strings"
	"testing"

	"github.com/nurozen/context-marmot/internal/curator"
)

// ---------------------------------------------------------------------------
// BuildPhase1Prompt
// ---------------------------------------------------------------------------

func TestBuildPhase1Prompt_IncludesReferenceAndStats(t *testing.T) {
	stats := curator.GraphStats{
		NodeCount:  12,
		EdgeCount:  7,
		Namespaces: []string{"core", "auth"},
	}
	got := BuildPhase1Prompt(stats, nil, false)

	if !strings.Contains(got, SDKReference) {
		t.Error("phase-1 prompt should embed the SDK reference")
	}
	if !strings.Contains(got, "Nodes: 12") {
		t.Errorf("expected node count in prompt, got:\n%s", got)
	}
	if !strings.Contains(got, "Edges: 7") {
		t.Errorf("expected edge count in prompt")
	}
	if !strings.Contains(got, "Namespaces: core, auth") {
		t.Errorf("expected namespaces line, got:\n%s", got)
	}
	// Not read-only, so the read-only warning must be absent.
	if strings.Contains(got, "READ-ONLY") {
		t.Error("did not expect READ-ONLY line for a writable vault")
	}
	// No selected nodes section.
	if strings.Contains(got, "Selected nodes") {
		t.Error("did not expect selected-nodes section with nil selection")
	}
}

func TestBuildPhase1Prompt_ReadOnly(t *testing.T) {
	stats := curator.GraphStats{NodeCount: 1, EdgeCount: 0}
	got := BuildPhase1Prompt(stats, nil, true)
	if !strings.Contains(got, "READ-ONLY") {
		t.Errorf("expected READ-ONLY warning when readOnly=true, got:\n%s", got)
	}
}

func TestBuildPhase1Prompt_NoNamespacesLine(t *testing.T) {
	stats := curator.GraphStats{NodeCount: 3, EdgeCount: 2, Namespaces: nil}
	got := BuildPhase1Prompt(stats, nil, false)
	if strings.Contains(got, "- Namespaces:") {
		t.Errorf("did not expect namespaces line when none present, got:\n%s", got)
	}
}

func TestBuildPhase1Prompt_SelectedNodes(t *testing.T) {
	stats := curator.GraphStats{NodeCount: 2, EdgeCount: 1}
	long := strings.Repeat("x", 250)
	selected := []curator.APINodeSummary{
		{
			ID:      "core/mcp-engine",
			Type:    "service",
			Edges:   3,
			Summary: long,
			Tags:    []string{"mcp", "entrypoint"},
		},
		{
			ID:      "auth/login",
			Type:    "function",
			Edges:   0,
			Summary: "", // empty summary — no summary line
			Tags:    nil,
		},
	}
	got := BuildPhase1Prompt(stats, selected, false)

	if !strings.Contains(got, "Selected nodes") {
		t.Fatalf("expected selected-nodes section, got:\n%s", got)
	}
	if !strings.Contains(got, "core/mcp-engine (type: service, edges: 3)") {
		t.Errorf("expected formatted selected node header, got:\n%s", got)
	}
	// Long summary should be truncated with an ellipsis.
	if !strings.Contains(got, "...") {
		t.Error("expected long summary to be truncated with ...")
	}
	if strings.Contains(got, long) {
		t.Error("expected long summary to be truncated, not included verbatim")
	}
	if !strings.Contains(got, "[tags: mcp, entrypoint]") {
		t.Errorf("expected tags rendered for first node, got:\n%s", got)
	}
	// Second node has no tags and empty summary.
	if !strings.Contains(got, "auth/login (type: function, edges: 0)") {
		t.Errorf("expected second selected node header, got:\n%s", got)
	}
}

// ---------------------------------------------------------------------------
// BuildPhase2Prompt
// ---------------------------------------------------------------------------

func TestBuildPhase2Prompt_Success(t *testing.T) {
	result := &Result{
		Value: map[string]any{"id": "core/mcp-engine", "type": "service"},
		Logs:  []string{"log: hello", "warn: careful"},
	}
	got := BuildPhase2Prompt("What is core/mcp-engine?", `return client.getNode("core/mcp-engine");`, result)

	if !strings.Contains(got, "What is core/mcp-engine?") {
		t.Error("expected original question in prompt")
	}
	if !strings.Contains(got, `return client.getNode("core/mcp-engine");`) {
		t.Error("expected the executed code in prompt")
	}
	if !strings.Contains(got, "## Result") {
		t.Error("expected Result section on success")
	}
	if strings.Contains(got, "Execution failed") {
		t.Error("did not expect failure section on success")
	}
	if !strings.Contains(got, "core/mcp-engine") {
		t.Error("expected serialized result body")
	}
	if !strings.Contains(got, "## Logs") {
		t.Error("expected logs section when logs present")
	}
	if !strings.Contains(got, "- log: hello") {
		t.Errorf("expected log line rendered, got:\n%s", got)
	}
	if strings.Contains(got, "TRUNCATED") {
		t.Error("did not expect truncation marker for un-truncated result")
	}
}

func TestBuildPhase2Prompt_Truncated(t *testing.T) {
	result := &Result{
		Value:     "big-blob<TRUNCATED at 8KB>",
		Truncated: true,
	}
	got := BuildPhase2Prompt("q", "code", result)
	if !strings.Contains(got, "<TRUNCATED at 8KB>") {
		t.Errorf("expected truncation marker, got:\n%s", got)
	}
}

func TestBuildPhase2Prompt_NoLogs(t *testing.T) {
	result := &Result{Value: []any{}}
	got := BuildPhase2Prompt("q", "code", result)
	if strings.Contains(got, "## Logs") {
		t.Error("did not expect logs section when no logs present")
	}
}

func TestBuildPhase2Prompt_Error(t *testing.T) {
	result := &Result{Error: "TypeError: client.foo is not a function"}
	got := BuildPhase2Prompt("do a thing", "return client.foo();", result)

	if !strings.Contains(got, "## Execution failed") {
		t.Errorf("expected failure section, got:\n%s", got)
	}
	if !strings.Contains(got, "TypeError: client.foo is not a function") {
		t.Error("expected the error text surfaced")
	}
	if strings.Contains(got, "## Result") {
		t.Error("did not expect Result section on error")
	}
}

// ---------------------------------------------------------------------------
// formatResult
// ---------------------------------------------------------------------------

func TestFormatResult_Nil(t *testing.T) {
	if got := formatResult(nil); got != "null" {
		t.Errorf("expected 'null' for nil, got %q", got)
	}
}

func TestFormatResult_String(t *testing.T) {
	if got := formatResult("<TRUNCATED>"); got != "<TRUNCATED>" {
		t.Errorf("expected string passthrough, got %q", got)
	}
}

func TestFormatResult_Struct(t *testing.T) {
	got := formatResult(map[string]any{"a": 1})
	if !strings.Contains(got, `"a": 1`) {
		t.Errorf("expected indented JSON, got %q", got)
	}
}

func TestFormatResult_Unmarshalable(t *testing.T) {
	// Channels are not JSON-serializable — formatResult falls back to %v.
	ch := make(chan int)
	got := formatResult(ch)
	if got == "" || strings.Contains(got, `"`) {
		t.Errorf("expected fmt fallback for unmarshalable value, got %q", got)
	}
}
