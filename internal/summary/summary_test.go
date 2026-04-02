package summary

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nurozen/context-marmot/internal/llm"
	"github.com/nurozen/context-marmot/internal/node"
)

func TestGenerateSummary(t *testing.T) {
	mock := &llm.MockProvider{
		SummaryResult: "Generated summary with [[auth/login]] and [[auth/logout]].",
	}
	engine := NewEngine(mock)

	nodes := []*node.Node{
		{ID: "auth/login", Type: "function", Status: "active", Summary: "Handles user login."},
		{ID: "auth/logout", Type: "function", Status: "active", Summary: "Handles user logout."},
	}

	result, err := engine.GenerateSummary(context.Background(), "auth", nodes)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Namespace != "auth" {
		t.Errorf("Namespace = %q, want %q", result.Namespace, "auth")
	}
	if result.NodeCount != 2 {
		t.Errorf("NodeCount = %d, want 2", result.NodeCount)
	}
	if result.Content != mock.SummaryResult {
		t.Errorf("Content = %q, want %q", result.Content, mock.SummaryResult)
	}
	if mock.GetSummarizeCalls() != 1 {
		t.Errorf("SummarizeCalls = %d, want 1", mock.GetSummarizeCalls())
	}
	if result.GeneratedAt.IsZero() {
		t.Error("GeneratedAt should not be zero")
	}
}

func TestGenerateSummaryNoSummarizer(t *testing.T) {
	engine := NewEngine(nil)

	_, err := engine.GenerateSummary(context.Background(), "test", []*node.Node{
		{ID: "a", Type: "concept"},
	})
	if err == nil {
		t.Fatal("expected error for nil summarizer")
	}
	if !strings.Contains(err.Error(), "no summarizer configured") {
		t.Errorf("error = %q, want to contain 'no summarizer configured'", err.Error())
	}
}

func TestGenerateSummarySkipsSuperseded(t *testing.T) {
	mock := &llm.MockProvider{}
	engine := NewEngine(mock)

	nodes := []*node.Node{
		{ID: "auth/login", Type: "function", Status: "active", Summary: "Login handler."},
		{ID: "auth/legacy", Type: "function", Status: "superseded", Summary: "Old handler."},
		{ID: "auth/logout", Type: "function", Status: "", Summary: "Logout handler."},
	}

	result, err := engine.GenerateSummary(context.Background(), "auth", nodes)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Only 2 active nodes should be included.
	if result.NodeCount != 2 {
		t.Errorf("NodeCount = %d, want 2 (superseded should be skipped)", result.NodeCount)
	}
	// Verify the content doesn't reference the superseded node.
	if strings.Contains(result.Content, "auth/legacy") {
		t.Error("summary should not contain superseded node auth/legacy")
	}
}

func TestWriteAndReadSummary(t *testing.T) {
	dir := t.TempDir()

	original := &SummaryResult{
		Namespace:   "default",
		Content:     "This is a test summary with [[node/a]] references.",
		NodeCount:   5,
		GeneratedAt: time.Date(2026, 4, 1, 12, 0, 0, 0, time.UTC),
	}

	if err := WriteSummary(dir, "default", original); err != nil {
		t.Fatalf("WriteSummary: %v", err)
	}

	// Verify file exists at expected path.
	path := filepath.Join(dir, "_summary.md")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expected file at %s: %v", path, err)
	}

	// Read it back.
	got, err := ReadSummary(dir, "default")
	if err != nil {
		t.Fatalf("ReadSummary: %v", err)
	}
	if got.Namespace != original.Namespace {
		t.Errorf("Namespace = %q, want %q", got.Namespace, original.Namespace)
	}
	if got.NodeCount != original.NodeCount {
		t.Errorf("NodeCount = %d, want %d", got.NodeCount, original.NodeCount)
	}
	if got.Content != original.Content {
		t.Errorf("Content = %q, want %q", got.Content, original.Content)
	}
	if !got.GeneratedAt.Equal(original.GeneratedAt) {
		t.Errorf("GeneratedAt = %v, want %v", got.GeneratedAt, original.GeneratedAt)
	}
}

func TestWriteSummaryDefaultNamespace(t *testing.T) {
	dir := t.TempDir()

	result := &SummaryResult{
		Namespace:   "default",
		Content:     "Default namespace summary.",
		NodeCount:   3,
		GeneratedAt: time.Now().UTC(),
	}

	if err := WriteSummary(dir, "default", result); err != nil {
		t.Fatalf("WriteSummary: %v", err)
	}

	// Should be at vault root, not in a subdirectory.
	path := filepath.Join(dir, "_summary.md")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expected file at vault root %s: %v", path, err)
	}

	// Should NOT exist in a "default" subdirectory.
	badPath := filepath.Join(dir, "default", "_summary.md")
	if _, err := os.Stat(badPath); err == nil {
		t.Error("should not create _summary.md inside a 'default' subdirectory")
	}
}

func TestWriteSummaryNamedNamespace(t *testing.T) {
	dir := t.TempDir()

	result := &SummaryResult{
		Namespace:   "auth",
		Content:     "Auth namespace summary.",
		NodeCount:   2,
		GeneratedAt: time.Now().UTC(),
	}

	if err := WriteSummary(dir, "auth", result); err != nil {
		t.Fatalf("WriteSummary: %v", err)
	}

	// Should be in the namespace subdirectory.
	path := filepath.Join(dir, "auth", "_summary.md")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expected file at %s: %v", path, err)
	}
}

func TestReadSummaryNotFound(t *testing.T) {
	dir := t.TempDir()

	_, err := ReadSummary(dir, "nonexistent")
	if err == nil {
		t.Fatal("expected error for missing summary file")
	}
}

func TestSchedulerNotifyChange(t *testing.T) {
	mock := &llm.MockProvider{
		SummaryResult: "Test summary.",
	}
	engine := NewEngine(mock)
	dir := t.TempDir()

	loader := func() ([]*node.Node, error) {
		return []*node.Node{
			{ID: "a", Type: "concept", Status: "active", Summary: "Node A."},
			{ID: "b", Type: "concept", Status: "active", Summary: "Node B."},
			{ID: "c", Type: "concept", Status: "active", Summary: "Node C."},
		}, nil
	}

	config := SchedulerConfig{
		Interval:       0, // disable periodic
		DeltaThreshold: 0.2,
		MinNodes:       3,
	}

	sched := NewScheduler(engine, config, dir, "test", loader)

	// Set lastNodeCount to 5, then notify with 10 (100% delta, well above 20%).
	sched.mu.Lock()
	sched.lastNodeCount = 5
	sched.mu.Unlock()

	sched.NotifyChange(10)

	// Give the async goroutine time to run.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if mock.GetSummarizeCalls() > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	if mock.GetSummarizeCalls() == 0 {
		t.Error("expected summarize to be called after significant delta notification")
	}
}

func TestSchedulerNotifyChangeBelowThreshold(t *testing.T) {
	mock := &llm.MockProvider{
		SummaryResult: "Test summary.",
	}
	engine := NewEngine(mock)
	dir := t.TempDir()

	loader := func() ([]*node.Node, error) {
		return []*node.Node{
			{ID: "a", Type: "concept", Status: "active", Summary: "Node A."},
		}, nil
	}

	config := SchedulerConfig{
		Interval:       0,
		DeltaThreshold: 0.2,
		MinNodes:       1,
	}

	sched := NewScheduler(engine, config, dir, "test", loader)

	// Set lastNodeCount to 10, then notify with 11 (10% delta, below 20% threshold).
	sched.mu.Lock()
	sched.lastNodeCount = 10
	sched.mu.Unlock()

	sched.NotifyChange(11)

	// Wait briefly to confirm nothing was triggered.
	time.Sleep(100 * time.Millisecond)

	if mock.GetSummarizeCalls() != 0 {
		t.Errorf("SummarizeCalls = %d, want 0 (delta below threshold)", mock.GetSummarizeCalls())
	}
}

func TestSchedulerMinNodes(t *testing.T) {
	mock := &llm.MockProvider{
		SummaryResult: "Test summary.",
	}
	engine := NewEngine(mock)
	dir := t.TempDir()

	loader := func() ([]*node.Node, error) {
		return []*node.Node{
			{ID: "a", Type: "concept", Status: "active", Summary: "Node A."},
		}, nil
	}

	config := SchedulerConfig{
		Interval:       0,
		DeltaThreshold: 0.2,
		MinNodes:       5, // Require at least 5 nodes
	}

	sched := NewScheduler(engine, config, dir, "test", loader)

	// Notify with only 2 nodes — below MinNodes.
	sched.NotifyChange(2)

	// Wait briefly to confirm nothing was triggered.
	time.Sleep(100 * time.Millisecond)

	if mock.GetSummarizeCalls() != 0 {
		t.Errorf("SummarizeCalls = %d, want 0 (below MinNodes)", mock.GetSummarizeCalls())
	}
}

func TestSchedulerStartStop(t *testing.T) {
	mock := &llm.MockProvider{}
	engine := NewEngine(mock)
	dir := t.TempDir()

	loader := func() ([]*node.Node, error) {
		return nil, nil
	}

	config := SchedulerConfig{
		Interval:       1 * time.Hour, // won't fire during test
		DeltaThreshold: 0.2,
		MinNodes:       3,
	}

	sched := NewScheduler(engine, config, dir, "test", loader)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Should not panic.
	sched.Start(ctx)
	// Starting again should be a no-op.
	sched.Start(ctx)

	sched.Stop()
	// Stopping again should be a no-op.
	sched.Stop()
}

func TestDefaultSchedulerConfig(t *testing.T) {
	cfg := DefaultSchedulerConfig()

	if cfg.Interval != 30*time.Minute {
		t.Errorf("Interval = %v, want 30m", cfg.Interval)
	}
	if cfg.DeltaThreshold != 0.2 {
		t.Errorf("DeltaThreshold = %f, want 0.2", cfg.DeltaThreshold)
	}
	if cfg.MinNodes != 3 {
		t.Errorf("MinNodes = %d, want 3", cfg.MinNodes)
	}
}
