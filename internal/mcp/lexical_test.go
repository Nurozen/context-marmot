package mcp

import (
	"testing"

	"github.com/nurozen/context-marmot/internal/node"
)

func TestLexicalSearch_BasicMatch(t *testing.T) {
	nodes := []*node.Node{
		{ID: "a", Summary: "authenticate user login flow", Status: node.StatusActive},
		{ID: "b", Summary: "render markdown to HTML", Status: node.StatusActive},
		{ID: "c", Summary: "calculate tax for invoice", Status: node.StatusActive},
	}

	got := LexicalSearch("auth login", nodes, 5)
	if len(got) == 0 {
		t.Fatal("expected at least one match for 'auth login'")
	}
	if got[0].ID != "a" {
		t.Errorf("expected 'a' to rank first, got %q (full order: %v)", got[0].ID, ids(got))
	}
}

func TestLexicalSearch_TagMatch(t *testing.T) {
	nodes := []*node.Node{
		{ID: "a", Summary: "completely unrelated body text", Tags: []string{"oauth", "session"}, Status: node.StatusActive},
		{ID: "b", Summary: "rendering pipeline", Tags: []string{"ui"}, Status: node.StatusActive},
	}

	got := LexicalSearch("oauth", nodes, 5)
	if len(got) != 1 || got[0].ID != "a" {
		t.Errorf("expected only 'a' to match via tag, got %v", ids(got))
	}
}

func TestLexicalSearch_TopK(t *testing.T) {
	nodes := []*node.Node{
		{ID: "a", Summary: "user login", Status: node.StatusActive},
		{ID: "b", Summary: "user signup", Status: node.StatusActive},
		{ID: "c", Summary: "user logout", Status: node.StatusActive},
		{ID: "d", Summary: "user delete", Status: node.StatusActive},
	}

	got := LexicalSearch("user", nodes, 2)
	if len(got) != 2 {
		t.Errorf("expected exactly 2 results with topK=2, got %d (%v)", len(got), ids(got))
	}
}

func TestLexicalSearch_StopwordsIgnored(t *testing.T) {
	nodes := []*node.Node{
		{ID: "a", Summary: "user account management", Status: node.StatusActive},
		{ID: "b", Summary: "the of and to in", Status: node.StatusActive},
	}

	// Query is all stopwords plus "user". Only the 'user' token should drive scoring.
	got := LexicalSearch("the user", nodes, 5)
	if len(got) != 1 {
		t.Fatalf("expected exactly 1 match, got %d (%v)", len(got), ids(got))
	}
	if got[0].ID != "a" {
		t.Errorf("expected 'a', got %q", got[0].ID)
	}

	// Pure-stopword query should yield zero results.
	none := LexicalSearch("the of and", nodes, 5)
	if len(none) != 0 {
		t.Errorf("expected no results for pure-stopword query, got %v", ids(none))
	}
}

func TestLexicalSearch_EmptyQuery(t *testing.T) {
	nodes := []*node.Node{
		{ID: "a", Summary: "anything", Status: node.StatusActive},
	}

	if got := LexicalSearch("", nodes, 5); len(got) != 0 {
		t.Errorf("expected empty result for empty query, got %v", ids(got))
	}
	if got := LexicalSearch("   ", nodes, 5); len(got) != 0 {
		t.Errorf("expected empty result for whitespace query, got %v", ids(got))
	}
}

func TestLexicalSearch_ContextAndTagWeighting(t *testing.T) {
	// Summary match (+3) should beat tag match (+0.5) and context match (+1).
	nodes := []*node.Node{
		{ID: "summary-hit", Summary: "alpha beta gamma", Status: node.StatusActive},
		{ID: "context-hit", Context: "alpha beta gamma", Status: node.StatusActive},
		{ID: "tag-hit", Tags: []string{"alpha"}, Status: node.StatusActive},
	}

	got := LexicalSearch("alpha", nodes, 5)
	if len(got) != 3 {
		t.Fatalf("expected all 3 to match, got %d", len(got))
	}
	if got[0].ID != "summary-hit" {
		t.Errorf("expected summary-hit first, got %q", got[0].ID)
	}
	if got[2].ID != "tag-hit" {
		t.Errorf("expected tag-hit last, got %q", got[2].ID)
	}
}

func ids(ns []*node.Node) []string {
	out := make([]string, len(ns))
	for i, n := range ns {
		out[i] = n.ID
	}
	return out
}
