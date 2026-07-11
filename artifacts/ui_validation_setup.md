# UI Validation Environment Setup

Built 2026-07-10 by the setup subagent. Root:
`/private/tmp/claude-501/-Users-nurozen-Documents-GitHub-context-marmot/a8ea1a71-058a-4b46-83e9-b3e41520a199/scratchpad/ui-validate`
(referred to as `$ROOT` below). Binary used: `/Users/nurozen/Documents/GitHub/context-marmot/bin/marmot`
(`v0.1.10-10-g41db2da-dirty`).

## Workspace project — `$ROOT/workspace`
- 6 small source files with cross-calls:
  - `go/main.go` -> calls `NewStore`/`Put`/`Get` (`go/store.go`) and `Render` (`go/render.go`)
  - `ts/api.ts` -> imports `fetchUser` (`ts/client.ts`) and `formatUser` (`ts/format.ts`)
- Vault at `$ROOT/workspace/.marmot`, initialised printf-driven with **mock** embeddings and
  **none** classifier (embedding-distance fallback).
- `vault_id: ws-main` set via `marmot configure --vault-id ws-main --dir .marmot`.
- Indexed: `marmot index .` → total=16 added=16 errors=0.

## Warren repo — `$ROOT/warren-repo`
- `marmot warren init --id demo-warren` → manifest `_warren.md` (warren_id demo-warren).
- Projects (imported via `marmot warren project import`):
  - **proj-local** = import of the workspace vault; preserved `vault_id: ws-main`
    (path `projects/proj-local/.marmot`). This matches the workspace vault ID, so it is the
    IDENTITY project.
  - **proj-b** = second seeded project (source `$ROOT/proj-b-src`, 2 Go files `mathutil.go`/
    `calc.go` with a cross-call, mock-embedded, indexed total=4), `vault_id: pb-vault`
    (path `projects/proj-b/.marmot`).
- Bridge: `marmot warren bridge add proj-local proj-b` → manifest bridge
  proj-local -> proj-b (relations: references).
- `marmot warren doctor` (run in warren-repo): `Warren "demo-warren" manifest looks healthy.` (exit 0).

## Workspace registration / mounts
- `marmot warren register --dir .marmot demo-warren $ROOT/warren-repo`
  - Register emitted the R2 identity note: proj-local matches workspace vault ID "ws-main" —
    served as the live vault.
- Mounted ONLY proj-b: `marmot warren mount --warren demo-warren --dir .marmot proj-b`.
- `marmot warren status --warren demo-warren`:

```
PROJECT     STATE     EDITABLE  AVAILABLE  PATH
proj-b      mounted   false     true       $ROOT/warren-repo/projects/proj-b/.marmot
proj-local  identity  false     true       .marmot
```

- `marmot warren list`:

```
WARREN_ID    PATH               REACHABLE  ACTIVE  EDITABLE  MATERIALIZED  IDENTITY
demo-warren  $ROOT/warren-repo  true       1       0         false         proj-local
```

## UI server (LEAVE RUNNING)
- Started detached from `$ROOT/workspace`:
  `nohup marmot ui --dir .marmot --port 3311 --no-open > $ROOT/logs/ui.log 2>&1 & disown`
- **PID: 98918**, URL: **http://localhost:3311** (binds 127.0.0.1). Log: `$ROOT/logs/ui.log`.
- `GET /api/version` → `{"version":0,"app_version":"v0.1.10-10-g41db2da-dirty"}`
- `GET /api/warrens` →

```json
{"warrens":{"demo-warren":{"path":"$ROOT/warren-repo","active_projects":["proj-b"],"identified_projects":["proj-local"]}}}
```

Expected UI state for validation: warren panel shows demo-warren with proj-b `mounted`
(read-only, available) and proj-local `identity`; one manifest bridge proj-local -> proj-b;
doctor healthy; selector identity count 1.
