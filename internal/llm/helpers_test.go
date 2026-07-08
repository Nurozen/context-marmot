package llm

import (
	"strings"
	"testing"

	"github.com/nurozen/context-marmot/internal/node"
)

func TestParseClassifyJSON(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want ClassifyResult
	}{
		{
			name: "plain json",
			in:   `{"action":"ADD","target_node_id":"","confidence":0.9,"reasoning":"new"}`,
			want: ClassifyResult{Action: ActionADD, Confidence: 0.9, Reasoning: "new"},
		},
		{
			name: "fenced json with prose",
			in:   "Sure!\n```json\n{\"action\":\"noop\",\"confidence\":0.5,\"reasoning\":\"same\"}\n```\nthanks",
			want: ClassifyResult{Action: ActionNOOP, Confidence: 0.5, Reasoning: "same"},
		},
		{
			name: "lowercase action normalized",
			in:   `{"action":"supersede","target_node_id":"n1","confidence":0.7}`,
			want: ClassifyResult{Action: ActionSUPERSEDE, TargetNodeID: "n1", Confidence: 0.7},
		},
		{
			name: "unknown action -> fallback",
			in:   `{"action":"MERGE","confidence":0.9}`,
			want: fallbackResult(),
		},
		{
			name: "malformed json -> fallback",
			in:   `{action: ADD}`,
			want: fallbackResult(),
		},
		{
			name: "empty -> fallback",
			in:   ``,
			want: fallbackResult(),
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := parseClassifyJSON(tc.in)
			if got != tc.want {
				t.Errorf("got %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestFallbackResult(t *testing.T) {
	got := fallbackResult()
	if got.Action != ActionADD || got.Confidence != 0.1 {
		t.Errorf("fallbackResult = %+v", got)
	}
	if !strings.Contains(got.Reasoning, "fallback") {
		t.Errorf("reasoning = %q", got.Reasoning)
	}
}

func TestBuildUserMessage_NoCandidates(t *testing.T) {
	msg := buildUserMessage(ClassifyRequest{
		Incoming: &node.Node{ID: "a", Type: "concept", Summary: "s", Context: "ctx"},
	})
	if !strings.Contains(msg, "No similar existing nodes found.") {
		t.Errorf("expected no-candidates text, got:\n%s", msg)
	}
	if !strings.Contains(msg, "ID: a") {
		t.Errorf("expected incoming id, got:\n%s", msg)
	}
}

func TestBuildUserMessage_NilIncoming(t *testing.T) {
	msg := buildUserMessage(ClassifyRequest{})
	if !strings.Contains(msg, "Incoming node:") {
		t.Errorf("expected header even with nil incoming, got:\n%s", msg)
	}
}

func TestBuildUserMessage_TruncatesContextAndCandidates(t *testing.T) {
	longCtx := strings.Repeat("x", anthropicContextLimit+50)
	var cands []CandidateNode
	for i := 0; i < anthropicMaxCandidates+2; i++ {
		cands = append(cands, CandidateNode{Node: &node.Node{ID: "c", Summary: "sum"}, Score: 0.5})
	}
	msg := buildUserMessage(ClassifyRequest{
		Incoming:   &node.Node{ID: "a", Context: longCtx},
		Candidates: cands,
	})
	// Context truncated to limit: extract the "  Context: <value>" line.
	ctxLine := ""
	for _, line := range strings.Split(msg, "\n") {
		if strings.HasPrefix(line, "  Context: ") {
			ctxLine = strings.TrimPrefix(line, "  Context: ")
		}
	}
	if len(ctxLine) != anthropicContextLimit {
		t.Errorf("context not truncated to %d chars, got %d", anthropicContextLimit, len(ctxLine))
	}
	// Only anthropicMaxCandidates candidates rendered.
	if got := strings.Count(msg, "Similarity score:"); got != anthropicMaxCandidates {
		t.Errorf("rendered %d candidates, want %d", got, anthropicMaxCandidates)
	}
}

func TestBuildSummarizeUserMessage(t *testing.T) {
	msg := buildSummarizeUserMessage(SummarizeRequest{
		Namespace: "auth",
		Nodes: []NodeSummaryInput{
			{ID: "auth/login", Type: "concept", Summary: "login", Edges: []string{"auth/token"}},
			{ID: "auth/noedges", Type: "concept", Summary: "x"},
		},
	})
	if !strings.Contains(msg, "Namespace: auth") {
		t.Errorf("missing namespace: %s", msg)
	}
	if !strings.Contains(msg, "Edges: auth/token") {
		t.Errorf("missing edges: %s", msg)
	}
	if !strings.Contains(msg, "ID: auth/noedges") {
		t.Errorf("missing second node: %s", msg)
	}
	if strings.Count(msg, "Edges:") != 1 {
		t.Errorf("node with no edges should not render Edges line: %s", msg)
	}
}

func TestBuildSummarizeUserMessage_Truncation(t *testing.T) {
	var nodes []NodeSummaryInput
	for i := 0; i < summarizeMaxNodes+10; i++ {
		nodes = append(nodes, NodeSummaryInput{ID: "n", Type: "t", Summary: "s"})
	}
	// One node with an over-long summary and too many edges.
	longSummary := strings.Repeat("y", summarizeSummaryLimit+100)
	manyEdges := make([]string, summarizeMaxEdges+3)
	for i := range manyEdges {
		manyEdges[i] = "e"
	}
	nodes[0] = NodeSummaryInput{ID: "big", Type: "t", Summary: longSummary, Edges: manyEdges}

	msg := buildSummarizeUserMessage(SummarizeRequest{Namespace: "ns", Nodes: nodes})

	// Only summarizeMaxNodes node lines rendered.
	if got := strings.Count(msg, "- ID:"); got != summarizeMaxNodes {
		t.Errorf("rendered %d node lines, want %d", got, summarizeMaxNodes)
	}
	// Locate the big node's line and dissect its Summary and Edges fields.
	bigLine := ""
	for _, line := range strings.Split(msg, "\n") {
		if strings.HasPrefix(line, "- ID: big") {
			bigLine = line
		}
	}
	if bigLine == "" {
		t.Fatalf("big node line not found in:\n%s", msg)
	}
	_, afterSummary, ok := strings.Cut(bigLine, "Summary: ")
	if !ok {
		t.Fatalf("no Summary field: %q", bigLine)
	}
	summaryVal, tail, _ := strings.Cut(afterSummary, " | Edges: ")
	if len(summaryVal) != summarizeSummaryLimit {
		t.Errorf("summary not truncated: got %d chars, want %d", len(summaryVal), summarizeSummaryLimit)
	}
	if got := len(strings.Split(tail, ", ")); got != summarizeMaxEdges {
		t.Errorf("edges not truncated: got %d entries, want %d", got, summarizeMaxEdges)
	}
}
