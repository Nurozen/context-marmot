# Development

## Build and test

```bash
make build         # Build binary to bin/marmot
make test          # Run all tests with race detector
make test-v        # Verbose test output
make test-cover    # Generate coverage report
make vet           # Run go vet
make fmt           # Format code
make tidy          # Tidy go.mod
```

Run integration tests:

```bash
go test -race -tags integration -count=1 ./internal/
```

## End-to-end tests

The `e2e/` package exercises the built binary against a static fixture vault
(`e2e/fixture/`): CLI flows (index, status, query, verify, sdk, staleness
detection), the MCP server over stdio JSON-RPC (all six tools), and the
embedded web UI over HTTP.

```bash
make e2e         # Go e2e suite (builds the binary itself)
make e2e-ui      # Playwright browser validation of the graph UI
make e2e-all     # Both
```

Browser tests need one-time setup:

```bash
cd web && npm install && npx playwright install chromium
```

The Playwright suite (`web/e2e/ui.spec.ts`) starts a UI server on a temp copy
of the fixture vault via `web/e2e/serve.sh`, then validates graph rendering,
node detail interaction, and search — failing on any console or page error.

## Node Format

Nodes are markdown files with YAML frontmatter:

```markdown
---
id: auth/login
type: function
namespace: default
status: active
tags:
  - auth
  - security
source:
  path: src/auth/login.ts
  lines: [1, 35]
  hash: a3f8b2c1
edges:
  - target: auth/validate_token
    relation: calls
  - target: db/users
    relation: reads
---

Authenticates a user with email and password.

## Relationships

- **calls** [[auth/validate_token]]
- **reads** [[db/users]]

## Context

// typescript
export async function login(email: string, password: string) { ... }
```

## Edge types

**Structural** (acyclic --- enforced):
`contains`, `imports`, `extends`, `implements`

**Behavioral** (cycles allowed):
`calls`, `reads`, `writes`, `references`, `cross_project`, `associated`
