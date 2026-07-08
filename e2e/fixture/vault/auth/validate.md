---
id: auth/validate
type: function
namespace: default
status: active
edges:
    - target: auth/login
      relation: references
tags:
    - auth
---

ValidateToken checks a session token's signature and expiry before granting access.

## Relationships

- **references** [[auth/login]]

## Context

Companion function node used to exercise behavioral edges in queries.
