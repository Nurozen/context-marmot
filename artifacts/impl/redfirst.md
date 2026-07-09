# Red-first baseline proof — WS1 e2e contention tests

The two new e2e tests (`TestConcurrentServes`, `TestIndexDuringServe` in
`e2e/e2e_test.go`, build tag `e2e`) were run against a binary built from the
pre-WS1 baseline commit `6ec0f61` ("Fix staticcheck QF1008 in mcp coverage
test"), i.e. `go-sqlite3 v0.17.1`, no WAL, no `busy_timeout`. Method:

```sh
git worktree add <scratchpad>/baseline 6ec0f61
cp -R web/dist <scratchpad>/baseline/web/dist   # gitignored build artifact
(cd <scratchpad>/baseline && go build -o <scratchpad>/marmot-baseline ./cmd/marmot)
MARMOT_E2E_BIN=<scratchpad>/marmot-baseline \
  go test -tags e2e -count=1 -run 'TestConcurrentServes|TestIndexDuringServe' -v ./e2e/
```

(`MARMOT_E2E_BIN` is the harness hook added in `e2e_test.go` `TestMain` that
skips the working-tree build and points every spawned process at a prebuilt
binary.)

Both tests went RED on the baseline in two consecutive runs, and both are
GREEN against the WS1 working tree (WAL + 5s busy_timeout + driver v0.33.2).

## Baseline run 1 (raw)

```
=== RUN   TestConcurrentServes
    e2e_test.go:532: sustained 10s: 6027 queries (A), 256 writes (B)
    e2e_test.go:537: serve B write failed: tool context_write returned error: [{Type:text Text:upsert embedding: exec upsert: sqlite3: database is locked}]
--- FAIL: TestConcurrentServes (11.63s)
=== RUN   TestIndexDuringServe
    e2e_test.go:629: index-during-serve: 377 serve writes completed; index output: index: open embedding store: init schema: create embeddings table: sqlite3: database is locked
    e2e_test.go:632: index during serve failed: exit status 1
        index: open embedding store: init schema: create embeddings table: sqlite3: database is locked
    e2e_test.go:637: index output contains "database is locked" (swallowed error):
        index: open embedding store: init schema: create embeddings table: sqlite3: database is locked
--- FAIL: TestIndexDuringServe (1.74s)
FAIL
FAIL	github.com/nurozen/context-marmot/e2e	13.649s
```

## Baseline run 2 (raw)

```
=== RUN   TestConcurrentServes
    e2e_test.go:532: sustained 10s: 5857 queries (A), 266 writes (B)
    e2e_test.go:537: serve B write failed: tool context_write returned error: [{Type:text Text:upsert embedding: exec upsert: sqlite3: database is locked}]
--- FAIL: TestConcurrentServes (11.62s)
=== RUN   TestIndexDuringServe
    e2e_test.go:629: index-during-serve: 381 serve writes completed; index output: embedding: using mock embedder (lexical only)
        Indexed 310/314 nodes into embedding store.
    e2e_test.go:644: serve write during index failed: tool context_write returned error: [{Type:text Text:upsert embedding: exec upsert: sqlite3: database is locked}]
--- FAIL: TestIndexDuringServe (1.85s)
FAIL
FAIL	github.com/nurozen/context-marmot/e2e	13.711s
```

## What the failures show

- `TestConcurrentServes` (failure mode 2 setup — serve A tight `context_query`
  loop holding SHARED locks, serve B `context_write` burst): B's write dies
  with an instant `sqlite3: database is locked` (`upsert embedding: exec
  upsert`) after ~250 successful writes, while A's reads continue (~6000
  queries in 10s). No retry, no busy wait — the reproduced instant-SQLITE_BUSY
  behavior. Reproduced in 2/2 runs at roughly the same point.
- `TestIndexDuringServe` (failure mode 1 — concurrent writer processes): the
  lock error strikes in *both directions* across the two runs:
  - run 1: `marmot index` exits 1 before doing any work —
    `open embedding store: init schema: create embeddings table: sqlite3:
    database is locked` (serve's write activity holds the file lock at the
    moment index opens the DB).
  - run 2: index succeeds, but a serve-side `context_write` fails with
    `upsert embedding: exec upsert: sqlite3: database is locked` while index's
    upsert burst holds the write lock.
- Timings: the failures appear well inside the sustain windows (B fails ~1.5s
  into its burst by call count; index collides immediately), confirming the
  contention window the tests create is far larger than needed.

No strengthening was required — both tests reproduced on the first attempt
(and again on the second). The full indefinite PENDING-lock wedge (a call
exceeding the 5s per-call deadline) was not needed to go red; the instant
`database is locked` failures trip the same assertions.

## Green on the WS1 tree (same tests, working-tree binary)

```
=== RUN   TestConcurrentServes
    e2e_test.go:532: sustained 10s: 1549 queries (A), 1574 writes (B)
--- PASS: TestConcurrentServes (10.59s)
=== RUN   TestIndexDuringServe
    e2e_test.go:629: index-during-serve: 47 serve writes completed; index output: embedding: using mock embedder (lexical only)
        Indexed 316/320 nodes into embedding store.
--- PASS: TestIndexDuringServe (0.13s)
PASS
ok  	github.com/nurozen/context-marmot/e2e	11.823s
```

Note the throughput shift under WAL: writes jump from ~256 (all-but-wedged
behind the reader) to ~1574 in the same 10s window, and queries drop from
~6000 to ~1549 because the query loop now shares the file with a writer that
actually makes progress — readers and the writer coexist instead of the
writer failing instantly.

The baseline worktree was removed after the runs
(`git worktree remove <scratchpad>/baseline`).
