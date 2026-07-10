#!/bin/bash
# Starts a marmot UI server against a temp copy of the e2e fixture vault,
# plus a warren fixture (two-project warren: the workspace itself as an
# identified project + a mounted foreign project with a manifest bridge)
# so the warren Playwright project has real management surface to drive.
# Used by playwright.config.ts as the webServer command.
# Usage: bash e2e/serve.sh [port]
set -euo pipefail

PORT="${1:-3299}"
ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
BIN="$ROOT/bin/marmot"

if [ ! -x "$BIN" ]; then
  echo "bin/marmot not found — run 'make build' first" >&2
  exit 1
fi

WORK="$(mktemp -d)"

# Isolate spawned marmot processes from the developer's real ~/.marmot state
# (e.g. routes.yml vault registrations) so the fixture server is hermetic.
export HOME="$WORK"

cp -R "$ROOT/e2e/fixture/vault" "$WORK/.marmot"
cp -R "$ROOT/e2e/fixture/src" "$WORK/src"
mkdir -p "$WORK/.marmot/.marmot-data"

# Give the workspace a vault_id so the warren below can identify it (R2:
# identity is derived from a vault_id match, and the shared fixture's
# _config.md deliberately carries none).
cat > "$WORK/.marmot/_config.md" <<'EOF'
---
version: "1"
vault_id: e2e-web-vault
namespace: default
embedding_provider: mock
token_budget: 8192
---
# ContextMarmot Web E2E Fixture Vault

Temp copy of the shared e2e fixture with a vault_id for warren identity.
EOF

"$BIN" index --dir "$WORK/.marmot"

# ── Warren fixture leg ──────────────────────────────────────────────
# Author a warren containing (a) the workspace's own vault imported WITHOUT
# --vault-id (preserves e2e-web-vault -> identified project) and (b) a
# foreign project, bridged to the self project.
WARREN="$WORK/warren"
mkdir -p "$WARREN"
"$BIN" warren init --id wui --warren-dir "$WARREN"
"$BIN" warren project import self "$WORK/.marmot" --warren-dir "$WARREN"

OTHER="$WORK/other-src"
mkdir -p "$OTHER/.marmot/.marmot-data" "$OTHER/.marmot/svc"
cat > "$OTHER/.marmot/_config.md" <<'EOF'
---
version: "1"
namespace: default
embedding_provider: mock
---
EOF
cat > "$OTHER/.marmot/svc/beacon.md" <<'EOF'
---
id: svc/beacon
type: concept
namespace: default
status: active
edges:
    - target: "@e2e-web-vault/db/users"
      relation: references
---

Beacon service exercising the warren bridge into the workspace vault.
EOF
"$BIN" index --dir "$OTHER/.marmot"
"$BIN" warren project import other "$OTHER/.marmot" --warren-dir "$WARREN" --vault-id other-vault
"$BIN" warren bridge add self other --warren-dir "$WARREN" --relations references

# Diverge the LIVE workspace from the warren snapshot AFTER the import so
# the UI can prove identity nodes are served live (the snapshot copy in the
# warren checkout never carries this marker).
printf '\nLIVE-MARKER: served from the live workspace vault.\n' >> "$WORK/.marmot/db/users.md"

# Consumer side: register the warren and mount ONLY the foreign endpoint —
# the self project stays identity-only (never mounted).
(cd "$WORK" && "$BIN" warren register --dir .marmot wui "$WARREN")
(cd "$WORK" && "$BIN" warren mount --dir .marmot --warren wui other)

# Run the server as a child (not exec) so the EXIT trap still fires to remove
# the temp vault when Playwright terminates this script.
cd "$WORK"
"$BIN" ui --dir .marmot --port "$PORT" --no-open &
SERVER_PID=$!
trap 'kill "$SERVER_PID" 2>/dev/null; wait "$SERVER_PID" 2>/dev/null; rm -rf "$WORK"' EXIT INT TERM
wait "$SERVER_PID"
