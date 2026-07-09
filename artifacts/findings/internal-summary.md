# internal/summary

## internal/summary

No SQLite usage in this package (no imports of ncruces/go-sqlite3, no embedding store references) — Workstream 1 (WAL/busy_timeout/driver upgrade) does not touch this folder. All findings are for Workstream 2 (single-owner daemon): this package IS the per-process background scheduler that must become owner-only, plus the on-disk _summary.md persistence that currently races across processes.

### scheduler.go:28-58
Workstream 2. The Scheduler type: one instance per `marmot serve` process (constructed by the engine/CLI wiring). Under the daemon design only the lock-holding owner should construct/Start this. It holds a `nodeLoader` closure — in multi-process mode each process's loader reads from its own stale in-memory graph, so duplicate + stale summaries get generated concurrently.

```go
28	// Scheduler manages async summary regeneration.
29	type Scheduler struct {
30		engine     *Engine
31		config     SchedulerConfig
32		dir        string
33		namespace  string
34		nodeLoader func() ([]*node.Node, error) // function to load current nodes
...
48	func NewScheduler(engine *Engine, config SchedulerConfig, dir string, namespace string, nodeLoader func() ([]*node.Node, error)) *Scheduler {
```

### scheduler.go:62-93
Workstream 2. Start/Stop lifecycle: `Start(ctx)` spawns a background goroutine, `Stop()` blocks on doneCh and drains NotifyChange goroutines via `wg.Wait()`. Daemon handoff must call `Stop()` on the old owner before a new owner starts its own scheduler; proxy-mode `serve` processes must never call `Start`. Note `Stop()` can block for up to the 2-minute LLM regeneration timeout (see :185), which matters for owner shutdown/handoff latency.

```go
62	func (s *Scheduler) Start(ctx context.Context) {
...
73		go s.run(ctx)
74	}
...
78	func (s *Scheduler) Stop() {
...
86		close(s.stopCh)
87		<-s.doneCh
88		s.wg.Wait() // drain any NotifyChange-spawned goroutines
```

### scheduler.go:98-122
Workstream 2. `NotifyChange` deduplicates in-flight regenerations only *within one process* (the `regenerating` flag is in-memory). With N serve processes, N schedulers each fire regeneration -> duplicate LLM calls and racing WriteSummary calls. The single-owner daemon eliminates this by having exactly one scheduler; write-path RPCs from proxies should funnel NotifyChange into the owner.

```go
98	func (s *Scheduler) NotifyChange(currentNodeCount int) {
99		s.mu.Lock()
100		if s.regenerating {
101			s.mu.Unlock()
102			return // another regeneration is already in-flight
103		}
```

### scheduler.go:151-176
Workstream 2. Periodic ticker loop (default 30m from DefaultSchedulerConfig at :20-26): every serve process ticks independently today, multiplying LLM spend. Also responds to ctx cancellation — relevant to daemon shutdown wiring.

```go
163		ticker := time.NewTicker(s.config.Interval)
164		defer ticker.Stop()
165	
166		for {
167			select {
168			case <-s.stopCh:
169				return
170			case <-ctx.Done():
171			return
172		case <-ticker.C:
173			s.regenerate()
```

### scheduler.go:178-205
Workstream 2. `regenerate()` = nodeLoader -> LLM GenerateSummary (2-minute timeout, background context — it outlives the Start ctx) -> WriteSummary to disk. Each process regenerates from its own possibly-stale graph, then writes `_summary.md`: last-writer-wins across processes, same failure class as the heatmap. `lastNodeCount`/`lastGenerated` state is per-process, so processes disagree about staleness.

```go
185		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
186		defer cancel()
187	
188		result, err := s.engine.GenerateSummary(ctx, s.namespace, nodes)
...
194		if err := WriteSummary(s.dir, s.namespace, result); err != nil {
```

### summary.go:29-48
Workstream 2. summary.Engine serializes generation with an in-process mutex only (`mu sync.Mutex // protects generation`) — no cross-process protection. One Engine per serve process today; must be owner-only in the daemon.

```go
29	type Engine struct {
30		summarizer llm.Summarizer // nil = no generation possible
31		mu         sync.Mutex     // protects generation
32	}
```

### summary.go:104-152
Workstream 2. `WriteSummary` persists `_summary.md` via temp-file + rename. Atomic per write (no torn files) but no cross-process coordination — concurrent owners silently overwrite each other (last-writer-wins). Safe once only the daemon owner writes; no change needed to the write mechanics themselves.

```go
104	func WriteSummary(dir string, namespace string, result *SummaryResult) error {
...
125		// Atomic write: temp file in same dir, then rename.
126		tmp, err := os.CreateTemp(parent, ".summary-*.md.tmp")
...
146		if err := os.Rename(tmpPath, target); err != nil {
```

### summary_test.go:181-321
Workstream 2 test impact. Tests construct schedulers directly (NewScheduler, NotifyChange, Start/Stop no-op semantics: TestSchedulerNotifyChange :181, TestSchedulerNotifyChangeBelowThreshold :225, TestSchedulerMinNodes :261, TestSchedulerStartStop :293). These are unit-level and stay valid, but any refactor that moves scheduler ownership into a daemon/owner component (or changes Start/Stop signatures for handoff) will need corresponding updates; new tests will be needed for "scheduler only runs in owner process".

```go
293	func TestSchedulerStartStop(t *testing.T) {
...
314		sched.Start(ctx)
315		// Starting again should be a no-op.
316		sched.Start(ctx)
317	
318		sched.Stop()
319		// Stopping again should be a no-op.
320		sched.Stop()
```
