# internal/llm

## internal/llm

### internal/llm/provider.go:23-27
Peripheral relevance to Workstream 2 only. The `Summarizer` interface is what `summary.Scheduler` calls; in the single-owner daemon design, only the owner process should hold a Summarizer-backed scheduler to stop duplicate LLM calls. No changes needed inside this package — it is pure interfaces plus stateless HTTP clients (Anthropic/OpenAI, 120s timeout, no SQLite, no goroutines, no sockets, no signal/stdio handling, no lock files, no engine construction).

```go
23	// Summarizer generates namespace-level summaries from node data.
24	// Separate from Provider so implementations are optional.
25	type Summarizer interface {
26		Summarize(ctx context.Context, req SummarizeRequest) (string, error)
27	}
```

No other relevant findings: the package contains only HTTP LLM clients (`anthropic.go`, `openai.go`), interfaces (`provider.go`, `chat.go`), a test mock (`mock.go`), and httptest-based unit tests — none touch SQLite, ncruces/go-sqlite3, process lifecycle, unix sockets, schedulers, graph/heatmap persistence, or MCP/CLI wiring, so neither the WAL/driver-upgrade quick fix nor the daemon refactor changes code here.
