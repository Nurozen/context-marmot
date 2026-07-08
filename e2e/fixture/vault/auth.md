---
id: auth
type: module
namespace: default
status: active
edges:
    - target: auth/login
      relation: contains
    - target: auth/validate
      relation: contains
tags:
    - auth
---

Authentication module handling user login and token validation.

## Relationships

- **contains** [[auth/login]]
- **contains** [[auth/validate]]

## Context

Top-level module for the fixture project's authentication concerns.
