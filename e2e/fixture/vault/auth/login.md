---
id: auth/login
type: function
namespace: default
status: active
source:
    path: src/auth.go
    lines: [0, 0]
    hash: b5bce013a4877643b4bfc00c819c6dfed40a38f2cffec3e7802bff85f3fdca67
edges:
    - target: db/users
      relation: reads
tags:
    - auth
---

Login validates user credentials against the users table and issues a session token.

## Relationships

- **reads** [[db/users]]

## Context

Fixture function node with a real source file reference so staleness and
integrity checks exercise the hash pipeline end to end.
