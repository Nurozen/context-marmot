package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nurozen/context-marmot/internal/embedding"
)

// ---------------------------------------------------------------------------
// context_write argument validation (manual-test issue 7)
// ---------------------------------------------------------------------------

// A write that misnames the body field ("content" instead of "context") must
// be rejected with an error pointing at the right field, instead of silently
// creating an empty-body node.
func TestContextWrite_RejectsUnknownArguments(t *testing.T) {
	eng := testEngine(t)
	ctx := context.Background()

	res, err := eng.HandleContextWrite(ctx, makeCallToolRequest("context_write", map[string]any{
		"id":      "qa/wrong-field",
		"type":    "concept",
		"content": "this body is in the wrong field",
	}))
	if err != nil {
		t.Fatalf("HandleContextWrite: %v", err)
	}
	if !res.IsError {
		t.Fatal("expected write with unknown 'content' argument to be rejected")
	}
	msg := resultText(t, res)
	if !strings.Contains(msg, `"content"`) || !strings.Contains(msg, `"context"`) {
		t.Errorf("expected error naming the unknown arg and suggesting context, got %q", msg)
	}

	// No node must have been created.
	if _, ok := eng.GetGraph().GetNode("qa/wrong-field"); ok {
		t.Error("node was created despite rejected arguments")
	}

	// Multiple unknown args are all reported; unaliased ones without a hint.
	res, err = eng.HandleContextWrite(ctx, makeCallToolRequest("context_write", map[string]any{
		"id":          "qa/wrong-field-2",
		"type":        "concept",
		"description": "typo for summary",
		"bogus":       true,
	}))
	if err != nil {
		t.Fatalf("HandleContextWrite: %v", err)
	}
	if !res.IsError {
		t.Fatal("expected write with unknown args to be rejected")
	}
	msg = resultText(t, res)
	for _, want := range []string{`"description" (did you mean "summary"?)`, `"bogus"`, "valid arguments are"} {
		if !strings.Contains(msg, want) {
			t.Errorf("expected error to contain %q, got %q", want, msg)
		}
	}

	// Underscore-prefixed metadata keys are tolerated.
	res, err = eng.HandleContextWrite(ctx, makeCallToolRequest("context_write", map[string]any{
		"id":      "qa/meta-ok",
		"type":    "concept",
		"summary": "node with client metadata",
		"_meta":   map[string]any{"client": "test"},
	}))
	if err != nil {
		t.Fatalf("HandleContextWrite: %v", err)
	}
	if res.IsError {
		t.Fatalf("expected underscore-prefixed keys to be tolerated, got %s", resultText(t, res))
	}
}

// A write with neither summary nor context would produce an empty, unsearchable
// node — it must be rejected with an error naming the expected fields.
func TestContextWrite_RejectsEmptyBody(t *testing.T) {
	eng := testEngine(t)
	ctx := context.Background()

	for name, args := range map[string]map[string]any{
		"missing both":    {"id": "qa/empty", "type": "concept"},
		"whitespace only": {"id": "qa/empty-ws", "type": "concept", "summary": "  ", "context": "\n\t"},
	} {
		res, err := eng.HandleContextWrite(ctx, makeCallToolRequest("context_write", args))
		if err != nil {
			t.Fatalf("%s: HandleContextWrite: %v", name, err)
		}
		if !res.IsError {
			t.Fatalf("%s: expected empty-body write to be rejected", name)
		}
		msg := resultText(t, res)
		if !strings.Contains(msg, "summary") || !strings.Contains(msg, "context") {
			t.Errorf("%s: expected error naming summary/context, got %q", name, msg)
		}
		if _, ok := eng.GetGraph().GetNode(args["id"].(string)); ok {
			t.Errorf("%s: empty node was created", name)
		}
	}

	// Context-only and summary-only writes both remain valid.
	res, err := eng.HandleContextWrite(ctx, makeCallToolRequest("context_write", map[string]any{
		"id":      "qa/context-only",
		"type":    "concept",
		"context": "full body without a summary",
	}))
	if err != nil || res.IsError {
		t.Fatalf("context-only write should succeed, got err=%v res=%v", err, res)
	}
	res, err = eng.HandleContextWrite(ctx, makeCallToolRequest("context_write", map[string]any{
		"id":      "qa/summary-only",
		"type":    "concept",
		"summary": "summary without a body",
	}))
	if err != nil || res.IsError {
		t.Fatalf("summary-only write should succeed, got err=%v res=%v", err, res)
	}
}

// ---------------------------------------------------------------------------
// Read-your-writes ordering over the stdio transport (manual-test issue 6)
// ---------------------------------------------------------------------------

// slowWriteEmbedder delays Embed calls for texts containing the marker so a
// pipelined context_write is guaranteed to still be in flight when a
// concurrently-dispatched context_query would search the store.
type slowWriteEmbedder struct {
	inner  embedding.Embedder
	marker string
	delay  time.Duration
}

func (s *slowWriteEmbedder) Embed(text string) ([]float32, error) {
	if strings.Contains(text, s.marker) {
		time.Sleep(s.delay)
	}
	return s.inner.Embed(text)
}

func (s *slowWriteEmbedder) EmbedBatch(texts []string) ([][]float32, error) {
	return s.inner.EmbedBatch(texts)
}

func (s *slowWriteEmbedder) Model() string { return s.inner.Model() }

func (s *slowWriteEmbedder) Dimension() int { return s.inner.Dimension() }

// syncBuffer is a goroutine-safe bytes.Buffer: the stdio server writes tool
// responses from worker goroutines.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// A context_query pipelined immediately after context_write in the same
// session (same pipe flush, no waiting for the write ack) must still see the
// just-written node: tool calls are processed sequentially per session and
// the write persists + embeds before it acks.
func TestListenStdio_ReadYourWrites_SameFlush(t *testing.T) {
	dir := t.TempDir()
	embedder := &slowWriteEmbedder{
		inner:  embedding.NewMockEmbedder("test-model"),
		marker: "SLOWMARKER",
		delay:  200 * time.Millisecond,
	}
	eng, err := NewEngine(dir, embedder)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	t.Cleanup(func() { _ = eng.Close() })
	srv := NewServer(eng)

	writeArgs, _ := json.Marshal(map[string]any{
		"id":      "qa/ryw-note",
		"type":    "concept",
		"summary": "SLOWMARKER read-your-writes discount note",
	})
	queryArgs, _ := json.Marshal(map[string]any{"query": "discount note"})

	// All four messages in a single reader = a single pipe flush.
	input := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"ryw-test","version":"0.0.1"}}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		fmt.Sprintf(`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"context_write","arguments":%s}}`, writeArgs),
		fmt.Sprintf(`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"context_query","arguments":%s}}`, queryArgs),
	}, "\n") + "\n"

	var out syncBuffer
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.ListenStdio(ctx, strings.NewReader(input), &out); err != nil {
		t.Fatalf("ListenStdio: %v", err)
	}

	// Collect responses by id.
	responses := map[int64]json.RawMessage{}
	for _, line := range strings.Split(out.String(), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var msg struct {
			ID     int64           `json:"id"`
			Result json.RawMessage `json:"result"`
		}
		if err := json.Unmarshal([]byte(line), &msg); err != nil {
			continue
		}
		if msg.Result != nil {
			responses[msg.ID] = msg.Result
		}
	}

	writeRes, ok := responses[2]
	if !ok {
		t.Fatalf("no response for context_write (id 2); output:\n%s", out.String())
	}
	if !strings.Contains(string(writeRes), "created") {
		t.Errorf("expected write to report created, got %s", writeRes)
	}

	queryRes, ok := responses[3]
	if !ok {
		t.Fatalf("no response for context_query (id 3); output:\n%s", out.String())
	}
	if !strings.Contains(string(queryRes), "qa/ryw-note") {
		t.Errorf("read-your-writes violated: query pipelined after write missed the node; got %s", queryRes)
	}
}
