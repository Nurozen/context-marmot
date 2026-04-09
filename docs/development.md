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
