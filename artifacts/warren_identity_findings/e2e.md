# e2e scanner findings

## e2e

Scope note: the e2e folder contains no product code — it pins behavior. Relevance is (a) which
tests pin behavior R1/R2 will change (mostly: none directly — the self-mount/collision path has
ZERO e2e coverage today, a gap R1 must fill), (b) verbatim evidence of the actual warren CLI
surface and output strings for the UX pass, and (c) confirmation the web UI has no warren surface.

### e2e/warren_test.go:24-42 (constants) — R1/R2: no self-mount coverage exists
All warren e2e projects are imported with `--vault-id` values (`pa-vault`, `pb-vault`) that never
collide with the consumer workspace's local vault. The fixture vault `_config.md`
(e2e/fixture/vault/_config.md) has **no vault_id at all**:

```
---
version: "1"
namespace: default
embedding_provider: mock
token_budget: 8192
---
```

So no e2e test exercises refuseVaultIDCollision, the local-vault WARN path, self-mount aliasing,
or doctor's `vault_id_collision_workspace`. R1 needs new e2e scenarios (self-mount alias resolves
to live vault; `warren edit` refused on self-mount; true collision refused unconditionally;
doctor/mount agree). No existing warren e2e test pins the behavior R1 removes, so none should
break — but they should be re-run since they all traverse mount/ReloadWarrenState.

```go
24	const (
25		warrenID = "wa"
27		projA      = "pa"
28		projAVault = "pa-vault"
...
37		projB      = "pb"
38		projBVault = "pb-vault"
```

### e2e/warren_test.go:49-95 (seedWarren) — R2/UX: import surface
Verbatim import surface: `warren project import <projectID> <vaultPath> --vault-id <id>` and
`warren init --id <id>` run from the warren root. R2's "identified-local project" interaction with
import is currently untested. Note import here always *overrides* vault_id via `--vault-id`; the
context's claim "import preserves vault_id" is the default path not exercised by e2e.

```go
64		if out, err := runCLI(warrenRoot, "warren", "init", "--id", warrenID); err != nil {
80		if out, err := runCLI(warrenRoot, "warren", "project", "import", projA,
81			filepath.Join(srcA, ".marmot"), "--vault-id", projAVault); err != nil {
```

### e2e/warren_test.go:129-131 (burrowCachePath) — R1: bridge/materialized path convention
Pins the workspace-local materialized path (`internal/warren.materializedProjectPath`):

```go
130		return filepath.Join(consumer, ".marmot", ".marmot-data", "warrens", warrenID, "projects", projectID, ".marmot")
131	}
```

R1's alias must ensure self-mounts never materialize/serve from this path.

### e2e/warren_test.go:149-231 (TestWarrenRegisterMountQueryServe) — UX: full verb surface + `--dir` flags
Verbatim CLI surface used everywhere (note the flag pattern: `--dir .marmot` on every verb,
`--warren <id>` named flag, project ID positional):

```go
152		runCLI(consumer, "warren", "register", "--dir", ".marmot", warrenID, warrenRoot)
155		runCLI(consumer, "warren", "mount", "--dir", ".marmot", "--warren", warrenID, projA)
174		runCLI(consumer, "warren", "burrow", "--dir", ".marmot", "--warren", warrenID, "--materialize", projB)
197		runCLI(consumer, "query", "--dir", ".marmot", "--query", hotwalQuery)
```

UX friction evidence: every single warren invocation needs `--dir .marmot` even when run from the
project root — no default vault discovery is relied on in any e2e call. Context's "flag soup"
mentions `--root vs --warren-dir`; the e2e suite uses neither — only `--dir`, `--warren`, `--id`,
`--vault-id`, `--query`. (Verify against cmd/ source; e2e suggests --root/--warren-dir may be out
of date or unused on these paths.)

### e2e/warren_test.go:264-288 (status --json) — UX: output ergonomics friction, pinned schema
`warren status --json` output can be preceded by stderr warnings (test must scrape for the JSON
array — concrete evidence that machine-readable output is polluted):

```go
268		// The JSON array may be preceded by stderr warnings in CombinedOutput.
269		start, end := strings.Index(out, "["), strings.LastIndex(out, "]")
273		var statuses []struct {
274			ProjectID string `json:"project_id"`
275			Active    bool   `json:"active"`
276		}
```

R2 display work: status JSON schema currently has `project_id`/`active` (at minimum) — an
identified-local project needs representation here; this test will need updating for R2.

### e2e/warren_test.go:296-334 (index --force refusal) — R1-adjacent: pinned error text
Pins the B4 read-flock refusal message; the self-mount alias must NOT take this shared read lock
on the local vault against itself:

```go
318		if !strings.Contains(out, "open read-only by another marmot process") {
```

### e2e/warren_test.go:342-433 (editable write-back + lifecycle) — R1: editable gating tests
`warren edit --dir .marmot --warren <id> <proj>` (edit implies mount, output must contain
"editable"); MCP `context_write` to `@pa-vault/auth/login` lands in the warren CHECKOUT. R1's
"refuse editable on self-mounts" needs a sibling test; this test pins the non-self editable path
and should survive unchanged. Also pins teardown verbs verbatim: `burrow --drop --all`,
`unmount --all`, `unregister`, and `warren list` empty-state string `"No Warrens registered."`.

```go
348		out, err := runCLI(consumer, "warren", "edit", "--dir", ".marmot", "--warren", warrenID, projA)
352		if !strings.Contains(out, "editable") {
416		runCLI(consumer, "warren", "burrow", "--dir", ".marmot", "--warren", warrenID, "--drop", "--all")
430		if !strings.Contains(out, "No Warrens registered.") {
```

### e2e/warren_test.go:444-608 (TestWarrenGitRoadmapLoop) — R2/UX: refresh --pull, propose, provenance
Pinned output strings (error-message-quality inventory for the UX pass):
`"checkout pulled"`, `"Re-materialized burrow cache(s): <proj>"`, `"uncommitted change"`
(dirty refusal), `"never pushes"` (propose), branch pattern
`marmot/propose/<proj>-[0-9]{8}-[0-9]{6}`. Provenance file lives at
`.../warrens/<wid>/projects/<pid>/provenance.md` (sibling of the `.marmot` cache) and contains the
clone HEAD commit. R2 must define propose/refresh semantics for an identified-local project (today
propose is pathspec-limited to `projects/<pid>/` inside the warren clone — meaningless for a live
local vault).

```go
521		if !strings.Contains(out, "checkout pulled") {
524		if !strings.Contains(out, "Re-materialized burrow cache(s): "+projB) {
561		if !strings.Contains(out, "uncommitted change") {
579		if !strings.Contains(out, "never pushes") {
582		branch := regexp.MustCompile(`marmot/propose/` + projA + `-[0-9]{8}-[0-9]{6}`).FindString(out)
```

### e2e/warren_refresh_test.go:16-38 (seedWarrenFixture) — R2: workspace/warren state schema, hand-written
Pins the on-disk schemas R2's identity record must extend/migrate. Warren manifest and per-project
`_warren.md` frontmatter, verbatim:

```go
24		filepath.Join(warrenRoot, "_warren.md"): "---\nwarren_id: wp\nversion: 1\nprojects:\n    - project_id: proj-a\n      path: projects/proj-a/.marmot\n---\n",
25		filepath.Join(projVault, "_config.md"):  "---\nversion: \"1\"\nvault_id: proj-a-vault\nnamespace: default\nembedding_provider: mock\n---\n",
26		filepath.Join(projVault, "_warren.md"):  "---\nproject_id: proj-a\nwarren_id: wp\nvault_id: proj-a-vault\n---\n",
```

(The CONSUMER workspace `_warren.md` — where mounts/aliases live and where R2's identity record
would go — is only ever created via the CLI in e2e, never asserted structurally; R2 tests should
add schema assertions.)

### e2e/warren_refresh_test.go:45-92 (TestWarrenMountWhileOwnerLive) — R1: ReloadWarrenState liveness pin
The only e2e directly exercising ReloadWarrenState freshness: mount+`warren refresh` (output must
contain `"refreshed"`) from a second process while a MARMOT_DAEMON=1 owner serves; owner's watcher
fires on the workspace `_warren.md` touch (1s debounce, 20s poll). R1's "skip rt.Set for
self-mounts" changes this code path; this test uses a non-colliding vault_id so it should pass
unchanged, but it is the template for an R1 self-mount-alias liveness test.

```go
71		if !strings.Contains(out, "refreshed") {
77		// The owner's watcher fires on the _warren.md touch (1s debounce);
```

### e2e/e2e_test.go:112-125 (hermeticEnv/runCLI) — harness facts
Isolation is `HOME=<projdir>` only (routes.yml etc. land under temp HOME); `MARMOT_E2E_BIN` env
selects a prebuilt binary (line 35-39), otherwise `go build ./cmd/marmot`. New R1/R2 e2e tests
should follow this pattern. Build tag: `//go:build e2e` on all three files.

### e2e/e2e_test.go:997-1109 (TestUIServer) — UX: web UI has zero warren surface (CONFIRMS context)
The entire UI e2e coverage is `ui --dir .marmot --port N --no-open` plus exactly four endpoints:
`GET /`, `/api/graph/default`, `/api/namespaces`, `/api/search?q=`, `/api/node/default/auth/login`.
No warren endpoint exists or is tested — confirming the context claim that mount/unmount/edit/
refresh are CLI-only. Any UX-pass web work needs new endpoints + e2e subtests here.

```go
1002	cmd := exec.Command(binPath, "ui", "--dir", ".marmot", "--port", fmt.Sprint(port), "--no-open")
1076	code, body := get("/api/graph/default")
```

### Context-staleness flags
- "import preserves vault_id": true only for the default path; every e2e import overrides with
  `--vault-id`, so the preserving path (the one that creates R1's collision) is untested in e2e.
- Flag-soup examples `--root` / `--warren-dir` / `--aliases` never appear in e2e; the exercised
  surface is uniformly `--dir`/`--warren`/`--id`/`--vault-id`. Cross-check cmd/ before planning.
- Tests needing updates: R1 → none break, several new scenarios needed (self-mount alias, editable
  refusal, unconditional collision refusal, doctor consistency); R2 → warren_test.go:273-276
  status JSON struct and likely list/status assertions; migration test from R1 alias state.
