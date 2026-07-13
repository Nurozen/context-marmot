# UI Validation Environment 2 — Setup

Built 2026-07-11 by the setup subagent. Root (`$ROOT` below):
`/private/tmp/claude-501/-Users-nurozen-Documents-GitHub-context-marmot/a8ea1a71-058a-4b46-83e9-b3e41520a199/scratchpad/ui-validate2`
Binary: `/Users/nurozen/Documents/GitHub/context-marmot/bin/marmot`
(`v0.1.10-14-gc4711ac-dirty`, commit c4711ac). Full CLI transcripts in `$ROOT/logs/`
(`setup.log`, `warren-alpha.log`, `warren-beta.log`, `warren-gamma.log`, `doctor.log`, `api.log`).
The previous validation UI (old pid 98918 serving scratchpad/ui-validate) was killed via
`pkill -f "marmot ui --dir .marmot --port 3311"`; the user's two `marmot serve` processes
(pids 25237, 36147) were verified untouched before and after.

## Workspace — `$ROOT/workspace` (vault_id `ws-main`)
- 6 source files with cross-calls: `go/main.go` -> `go/store.go` (`NewStore`/`Put`/`Get`) and
  `go/render.go` (`Render`); `ts/api.ts` -> `ts/client.ts` (`fetchUser`) and `ts/format.ts`
  (`formatUser`).
- Vault `$ROOT/workspace/.marmot`, `embedding_provider: mock` (classifier: embedding-distance
  fallback), `vault_id` set via `marmot configure --vault-id ws-main --dir .marmot`.
- Indexed: total=16 added=16 errors=0.

## Seed projects (each mock-embedded, 2 Go files with a cross-call, indexed total=4)
| source dir | vault_id | used by |
|---|---|---|
| `$ROOT/proj-b-src` | pb-vault | warren-alpha proj-b |
| `$ROOT/proj-c-src` | pc-vault | warren-alpha proj-c |
| `$ROOT/proj-d-src` | pd-vault | warren-beta proj-d (read-only) |
| `$ROOT/proj-e-src` | pe-vault | warren-beta proj-e (materialized) |
| `$ROOT/proj-g-src` | pg-vault | warren-gamma proj-g |

## warren-alpha — `$ROOT/warren-alpha` (rich, healthy)
- `warren init --id warren-alpha`; imports: proj-local (from workspace vault, keeps ws-main),
  proj-b (pb-vault), proj-c (pc-vault); bridges proj-local->proj-b and proj-b->proj-c
  (relations: references).
- Registered in the workspace; register emitted the R2 identity note:
  `project "proj-local" ... matches this workspace's vault ID "ws-main" — served as your live vault`.
- Mounted proj-b and proj-c; `warren edit proj-c` -> editable.
- `warren status`: proj-b mounted/editable=false/available=true; proj-c mounted/**editable=true**/
  available=true; proj-local **identity** (path = workspace `.marmot`).
- **Expected panel**: identity chip on proj-local, proj-b mounted read-only, proj-c mounted +
  editable, two manifest bridges, doctor healthy.

## warren-beta — `$ROOT/warren-beta` (all dormant, policy + cache states)
- Two projects: proj-d (pd-vault), proj-e (pe-vault).
- `warren project set-readonly proj-d` -> D4 manifest policy (`readonly: true`, manifest v2):
  `Project "proj-d" is read-only for consumers`.
- `warren burrow --materialize proj-e` (CLI noted burrow implies materialize) -> mounted with
  materialized cache at `workspace/.marmot/.marmot-data/warrens/warren-beta/projects/proj-e/.marmot`
  (`burrow cache for "proj-e": cache from 2026-07-11T07:33:02Z`); then unmounted — cache kept.
- Read-only refusal exercised live: mounted proj-d, `warren edit proj-d` failed exit=1 with
  `warren edit: warren author marked project "proj-d" read-only; edits must go through the warren
  repository itself`; proj-d then unmounted to restore the all-dormant state.
- Final `warren status`: proj-d dormant, proj-e dormant, burrow cache for proj-e present.
- **Expected panel**: mount buttons only (nothing mounted); proj-d shows read-only policy and
  refuses edit if mounted+edited; proj-e shows materialized/burrow-cache state; warren row
  MATERIALIZED=true.

## warren-gamma — registered at `$ROOT/warren-gamma`, checkout MOVED (unreachable)
- `warren init --id warren-gamma`; imported proj-g (pg-vault); registered; mounted proj-g; then
  `mv warren-gamma warren-gamma-moved`.
- `warren status --warren warren-gamma` now prints:
  `warren "warren-gamma" UNREACHABLE at $ROOT/warren-gamma — re-run 'marmot warren register ...'
  or 'marmot warren unregister --warren warren-gamma'` plus the manifest-unreadable warning
  (status degraded to workspace state); proj-g mounted but **available=false**.
- **Expected panel**: warren-gamma surfaced as unreachable (C6-adjacent), proj-g mounted but
  unavailable.

## `marmot warren list` (workspace view)
```
WARREN_ID     REACHABLE  ACTIVE  EDITABLE  MATERIALIZED  IDENTITY
warren-alpha  true       2       1         false         proj-local
warren-beta   true       0       0         true          -
warren-gamma  false      1       0         false         -
```

## Doctors (see `$ROOT/logs/doctor.log`)
- `warren doctor --warren-dir $ROOT/warren-alpha` -> `Warren "warren-alpha" manifest looks healthy.` exit=0
- `warren doctor --warren-dir $ROOT/warren-beta` -> `Warren "warren-beta" manifest looks healthy.` exit=0
- `warren doctor --workspace --dir $ROOT/workspace/.marmot` -> exit=0,
  `doctor: 0 error(s), 0 warning(s), 1 info` (info self_identity for warren-alpha/proj-local);
  stderr carried the warren-gamma manifest-unreadable warning.

## UI server (LEAVE RUNNING)
- Started detached from `$ROOT/workspace`:
  `nohup marmot ui --dir .marmot --port 3311 --no-open > $ROOT/logs/ui.log 2>&1 & disown`
- **PID: 37520**, URL: **http://localhost:3311**. Log: `$ROOT/logs/ui.log`.
- `GET /api/version` -> `{"version":0,"app_version":"v0.1.10-14-gc4711ac-dirty"}` (answered after 1s poll).
- `GET /api/warrens` (full JSON in `$ROOT/logs/api.log`):
  - warren-alpha: `editable_projects: ["proj-c"]`, `active_projects: ["proj-b","proj-c"]`,
    `identified_projects: ["proj-local"]`
  - warren-beta: `materialized: true`, `active_projects: []`
  - warren-gamma: `active_projects: ["proj-g"]`, path points at the moved-away
    `$ROOT/warren-gamma` (unreachable on disk)
- `GET /api/doctor/workspace` -> one `info`/`self_identity` issue for proj-local, no errors/warnings.
