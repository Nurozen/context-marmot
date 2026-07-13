# findings

## web

### web/embed.go:1-6
Workstream 1 (dependency upgrade / go.mod hygiene): this is the only Go file in `web/` — it embeds the built UI into the binary via `embed.FS`. It does not touch SQLite or ncruces/go-sqlite3, but it is a `go:embed` consumer (used by the `ui` HTTP server, see `cmd/marmot/pipeline.go`), so `make build` requires `web/dist` to exist; any go.mod churn/rebuild during the upgrade must keep `dist/` built or compilation fails.

```go
1  package web
2
3  import "embed"
4
5  //go:embed all:dist
6  var Assets embed.FS
```

### web/e2e/serve.sh:22-36
Workstream 2 (single-owner daemon): the Playwright e2e harness spawns `marmot index` then `marmot ui` against a temp vault (with `HOME` overridden to isolate `~/.marmot` state). If daemon election / lock files / unix sockets land under `~/.marmot` or the vault dir, these two sequential processes (index, then ui) each build their own engine against the same vault — this script is a test path that must not block on the lock, and `HOME=$WORK` means any socket/lock path derived from `$HOME` lands in the temp dir (good), but a path derived from the vault dir would live in the copied `.marmot`. Also the `kill "$SERVER_PID"` teardown means the ui process must release its lock/socket cleanly on SIGTERM.

```sh
22  export HOME="$WORK"
23
24  cp -R "$ROOT/e2e/fixture/vault" "$WORK/.marmot"
25  cp -R "$ROOT/e2e/fixture/src" "$WORK/src"
26  mkdir -p "$WORK/.marmot/.marmot-data"
27
28  "$BIN" index --dir "$WORK/.marmot"
29
30  # Run the server as a child (not exec) so the EXIT trap still fires to remove
31  # the temp vault when Playwright terminates this script.
32  cd "$WORK"
33  "$BIN" ui --dir .marmot --port "$PORT" --no-open &
34  SERVER_PID=$!
35  trap 'kill "$SERVER_PID" 2>/dev/null; wait "$SERVER_PID" 2>/dev/null; rm -rf "$WORK"' EXIT INT TERM
36  wait "$SERVER_PID"
```

### web/playwright.config.ts:11-16
Workstream 2 (test impact): Playwright starts the ui server via serve.sh and health-checks `/api/version` with a 60s timeout. If `marmot ui` gains lock-election behavior (e.g. waiting on or deferring to a daemon owner), startup latency or refusal-to-start would break this webServer readiness check.

```ts
11  webServer: {
12    command: 'bash e2e/serve.sh 3299',
13    url: 'http://127.0.0.1:3299/api/version',
14    reuseExistingServer: false,
15    timeout: 60_000,
16  },
```

### web/vite.config.ts:9-17
Minor / workstream 2 config surface: the dev-server proxy hardcodes the ui backend at `http://localhost:3274`. If the daemon rework changes the ui command's default port or moves the API behind a unix socket, this proxy target must be updated (vite can only proxy to TCP).

```ts
 9  server: {
10    port: 5173,
11    proxy: {
12      '/api': {
13        target: 'http://localhost:3274',
14        changeOrigin: true,
15      },
16    },
17  },
```

No SQLite usage, engine construction, scheduler/watcher code, MCP transport, or lock/PID handling exists anywhere in `web/` — everything else (`src/*.ts`, `e2e/ui.spec.ts`) is browser-side UI code talking to `/api/*` over HTTP.
