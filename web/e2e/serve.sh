#!/bin/bash
# Starts a marmot UI server against a temp copy of the e2e fixture vault.
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

"$BIN" index --dir "$WORK/.marmot"

# Run the server as a child (not exec) so the EXIT trap still fires to remove
# the temp vault when Playwright terminates this script.
cd "$WORK"
"$BIN" ui --dir .marmot --port "$PORT" --no-open &
SERVER_PID=$!
trap 'kill "$SERVER_PID" 2>/dev/null; wait "$SERVER_PID" 2>/dev/null; rm -rf "$WORK"' EXIT INT TERM
wait "$SERVER_PID"
