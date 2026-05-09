package codemode

import (
	"context"
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Sandbox tests — these don't need an engine.
// ---------------------------------------------------------------------------

func TestExecute_ReturnValue(t *testing.T) {
	ex := NewExecutor(nil)
	r := ex.Execute(context.Background(), `return {x: 1, y: "hi"}`)
	if r.Error != "" && !strings.Contains(r.Error, "client") {
		t.Fatalf("unexpected error: %s", r.Error)
	}
	// nil engine still allows non-client code; expect map back.
	m, ok := r.Value.(map[string]any)
	if !ok {
		t.Fatalf("expected map, got %T (%v)", r.Value, r.Value)
	}
	if m["x"] != int64(1) && m["x"] != float64(1) {
		t.Fatalf("expected x=1, got %v", m["x"])
	}
}

func TestExecute_Eval_Disabled(t *testing.T) {
	ex := NewExecutor(nil)
	r := ex.Execute(context.Background(), `return eval("1+1")`)
	if r.Error == "" {
		t.Fatalf("expected eval to fail, got value=%v", r.Value)
	}
}

func TestExecute_Function_Disabled(t *testing.T) {
	ex := NewExecutor(nil)
	r := ex.Execute(context.Background(), `return new Function("return 1")()`)
	if r.Error == "" {
		t.Fatalf("expected Function constructor to fail")
	}
}

func TestExecute_NoFilesystem(t *testing.T) {
	ex := NewExecutor(nil)
	r := ex.Execute(context.Background(), `return typeof require + ":" + typeof process + ":" + typeof fetch`)
	if r.Error != "" {
		t.Fatalf("unexpected error: %s", r.Error)
	}
	// All three should be undefined (string "undefined").
	if !strings.Contains(r.Value.(string), "undefined:undefined:undefined") {
		t.Fatalf("expected all undefined, got %v", r.Value)
	}
}

func TestExecute_ConsoleLog_Captured(t *testing.T) {
	ex := NewExecutor(nil)
	r := ex.Execute(context.Background(), `console.log("hello", 42); console.warn("careful"); return null`)
	if r.Error != "" {
		t.Fatalf("unexpected error: %s", r.Error)
	}
	if len(r.Logs) != 2 {
		t.Fatalf("expected 2 log lines, got %d: %v", len(r.Logs), r.Logs)
	}
	if !strings.Contains(r.Logs[0], "log: hello 42") {
		t.Fatalf("unexpected first log: %q", r.Logs[0])
	}
	if !strings.Contains(r.Logs[1], "warn: careful") {
		t.Fatalf("unexpected second log: %q", r.Logs[1])
	}
}

func TestExecute_Timeout(t *testing.T) {
	ex := NewExecutor(nil).WithTimeout(200 * time.Millisecond)
	r := ex.Execute(context.Background(), `while(true){}`)
	if r.Error == "" {
		t.Fatalf("expected timeout error")
	}
	if !strings.Contains(strings.ToLower(r.Error), "timeout") {
		t.Fatalf("expected timeout in error, got %q", r.Error)
	}
}

func TestExecute_CodeTooLong(t *testing.T) {
	ex := NewExecutor(nil)
	long := strings.Repeat("a", MaxCodeLength+1)
	r := ex.Execute(context.Background(), long)
	if !strings.Contains(r.Error, "exceeds maximum length") {
		t.Fatalf("expected max-length error, got %q", r.Error)
	}
}

func TestExecute_ParseError(t *testing.T) {
	ex := NewExecutor(nil)
	r := ex.Execute(context.Background(), `return for x;`)
	if r.Error == "" {
		t.Fatalf("expected parse error")
	}
}

func TestExecute_NullReturn(t *testing.T) {
	ex := NewExecutor(nil)
	r := ex.Execute(context.Background(), `return null`)
	if r.Error != "" {
		t.Fatalf("unexpected error: %s", r.Error)
	}
	if r.Value != nil {
		t.Fatalf("expected nil value for null return, got %v", r.Value)
	}
}

func TestExecute_LargeResult_Truncated(t *testing.T) {
	ex := NewExecutor(nil)
	// Build a result that JSON-serializes to >8KB.
	r := ex.Execute(context.Background(), `
        const arr = [];
        for (let i = 0; i < 1000; i++) {
          arr.push({i, name: "node-" + i, summary: "this is a long summary that pushes us past 8KB"});
        }
        return arr;
    `)
	if r.Error != "" {
		t.Fatalf("unexpected error: %s", r.Error)
	}
	if !r.Truncated {
		t.Fatalf("expected truncation, got Truncated=false (value type %T)", r.Value)
	}
	s, ok := r.Value.(string)
	if !ok || !strings.Contains(s, "TRUNCATED") {
		t.Fatalf("expected truncated string marker, got %T %v", r.Value, r.Value)
	}
}

// ---------------------------------------------------------------------------
// ExtractCode
// ---------------------------------------------------------------------------

func TestExtractCode_JSBlock(t *testing.T) {
	in := "Here you go:\n```js\nconst r = client.query({query:\"a\"});\nreturn r;\n```\nThat works."
	got := ExtractCode(in)
	if !strings.Contains(got, "client.query") {
		t.Fatalf("did not extract code, got %q", got)
	}
}

func TestExtractCode_JavaScriptBlock(t *testing.T) {
	in := "```javascript\nreturn client.getStats();\n```"
	got := ExtractCode(in)
	if !strings.Contains(got, "getStats") {
		t.Fatalf("did not extract javascript-tagged block: %q", got)
	}
}

func TestExtractCode_TypeScriptBlock(t *testing.T) {
	in := "```typescript\nconst r = client.query({query: \"a\"});\nreturn r;\n```"
	got := ExtractCode(in)
	if !strings.Contains(got, "client.query") {
		t.Fatalf("did not extract typescript block: %q", got)
	}
}

func TestExtractCode_NoBlock(t *testing.T) {
	in := "Here is some plain prose without code."
	if got := ExtractCode(in); got != "" {
		t.Fatalf("expected empty extraction, got %q", got)
	}
}

func TestExtractCode_UnlabeledBlock_NotJS(t *testing.T) {
	// A block with no language tag and no JS hints should NOT be extracted —
	// might be Python or markdown sample.
	in := "```\nHello world\n```"
	if got := ExtractCode(in); got != "" {
		t.Fatalf("expected empty extraction for non-JS unlabeled block, got %q", got)
	}
}

func TestExtractCode_UnlabeledBlock_LooksLikeJS(t *testing.T) {
	in := "```\nconst x = client.getStats();\nreturn x;\n```"
	got := ExtractCode(in)
	if !strings.Contains(got, "getStats") {
		t.Fatalf("did not extract unlabeled JS-looking block: %q", got)
	}
}
