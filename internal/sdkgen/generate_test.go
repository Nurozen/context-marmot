package sdkgen

import (
	"os"
	"strings"
	"testing"
)

func TestGenerate(t *testing.T) {
	output := Generate("http://localhost:3000")

	// Basic sanity checks
	checks := []string{
		"ContextMarmot TypeScript SDK  v0.1.3",
		"export interface QueryInput",
		"export interface WriteInput",
		"export interface VerifyInput",
		"export interface DeleteInput",
		"export interface TagInput",
		"export interface QueryResult",
		"export interface WriteResult",
		"export interface VerifyResult",
		"export interface DeleteResult",
		"export interface TagResult",
		"export interface MarmotNode",
		"export interface MarmotEdge",
		"export interface GraphData",
		"export interface HeatPair",
		"export interface BridgeInfo",
		"export class MarmotClient",
		"export class MarmotError",
		"async query(",
		"async write(",
		"async verify(",
		"async delete(",
		"async tag(",
		"async getGraph(",
		"async getNode(",
		"async search(",
		"async getNamespaces(",
		"async getHeat(",
		"async getBridges(",
		"/api/sdk/context_query",
		"/api/sdk/context_write",
		"/api/sdk/context_verify",
		"/api/sdk/context_delete",
		"/api/sdk/context_tag",
		"/api/graph/_all",
		"/api/node/",
		"/api/search?q=",
		"/api/namespaces",
		"/api/heat/",
		"/api/bridges",
		"http://localhost:3000",
	}

	for _, s := range checks {
		if !strings.Contains(output, s) {
			t.Errorf("output missing: %q", s)
		}
	}

	// Write to temp for manual inspection
	_ = os.WriteFile("/tmp/marmot_sdk.ts", []byte(output), 0644)
}

func TestGenerateDefaultBaseURL(t *testing.T) {
	output := Generate("")
	if !strings.Contains(output, "http://localhost:3000") {
		t.Error("empty baseURL should default to http://localhost:3000")
	}
}

func TestGenerateCustomBaseURL(t *testing.T) {
	output := Generate("https://marmot.example.com/")
	if !strings.Contains(output, "https://marmot.example.com") {
		t.Error("custom baseURL not found in output")
	}
	// Trailing slash should be stripped
	if strings.Contains(output, "https://marmot.example.com/'") {
		t.Error("trailing slash was not stripped")
	}
}

func TestGenerateValidTypeScript(t *testing.T) {
	output := Generate("http://localhost:3000")

	// Must contain TS constructs
	for _, want := range []string{"export interface", "export class", "async", "/**"} {
		if !strings.Contains(output, want) {
			t.Errorf("missing TS construct: %q", want)
		}
	}

	// Must NOT contain Go-isms
	for _, bad := range []string{":=", "func ", "package "} {
		if strings.Contains(output, bad) {
			t.Errorf("contains Go syntax: %q", bad)
		}
	}

	// Balanced braces
	open := strings.Count(output, "{")
	close := strings.Count(output, "}")
	if open != close {
		t.Errorf("unbalanced braces: %d open vs %d close", open, close)
	}
}

func TestGenerateHeader(t *testing.T) {
	output := Generate("http://localhost:3000")
	if !strings.HasPrefix(output, "//") && !strings.HasPrefix(output, "/*") {
		t.Error("output should start with a comment")
	}
	lower := strings.ToLower(output[:500])
	if !strings.Contains(lower, "generated") && !strings.Contains(lower, "auto-generated") {
		t.Error("header should mention generation")
	}
	if !strings.Contains(lower, "contextmarmot") && !strings.Contains(lower, "marmot") {
		t.Error("header should mention marmot")
	}
}

func TestGenerateEnumTypes(t *testing.T) {
	output := Generate("http://localhost:3000")
	// Check enum values exist (may be multiline unions)
	enums := []string{
		"'adjacency'",
		"'deep'",
		"'function'",
		"'module'",
		"'staleness'",
		"'integrity'",
		"'contains'",
		"'imports'",
	}
	for _, e := range enums {
		if !strings.Contains(output, e) {
			t.Errorf("missing enum union: %q", e)
		}
	}
}
