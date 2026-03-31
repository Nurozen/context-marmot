#!/bin/bash
# Seed a demo ContextMarmot vault with a small project graph.
# Usage: cd testdata/demo && bash seed.sh
#
# After running, open .marmot/ in Obsidian to see the graph,
# or use the CLI to query it.

set -euo pipefail

MARMOT="../../bin/marmot"
DIR=".marmot"

# Clean and init
rm -rf "$DIR"
"$MARMOT" init --dir "$DIR"

echo "--- Writing nodes via MCP ---"

# We'll write nodes by creating the markdown files directly,
# then use the CLI to query and verify.

# Module node: auth
cat > "$DIR/auth.md" << 'NODEEOF'
---
id: auth
type: module
namespace: default
status: active
edges:
  - target: auth/login
    relation: contains
  - target: auth/validate_token
    relation: contains
  - target: auth/logout
    relation: contains
---

Authentication module handling user login, token validation, and logout.

## Relationships

- **contains** [[auth/login]]
- **contains** [[auth/validate_token]]
- **contains** [[auth/logout]]

## Context

```typescript
// src/auth/index.ts
export { login } from './login';
export { validateToken } from './validate_token';
export { logout } from './logout';
```
NODEEOF

# Function: auth/login
mkdir -p "$DIR/auth"
cat > "$DIR/auth/login.md" << 'NODEEOF'
---
id: auth/login
type: function
namespace: default
status: active
source:
  path: src/auth/login.ts
  lines: [1, 35]
  hash: abc123
edges:
  - target: auth/validate_token
    relation: calls
  - target: db/users
    relation: reads
---

Authenticates a user with email and password. Checks credentials against the users table, generates a JWT token on success.

## Relationships

- **calls** [[auth/validate_token]]
- **reads** [[db/users]]

## Context

```typescript
// src/auth/login.ts
import { validateToken } from './validate_token';
import { findUser } from '../db/users';

export async function login(email: string, password: string): Promise<Token> {
  const user = await findUser(email);
  if (!user || !await verifyPassword(password, user.passwordHash)) {
    throw new AuthError('Invalid credentials');
  }
  const token = generateJWT({ userId: user.id, role: user.role });
  return { token, expiresIn: 3600 };
}
```
NODEEOF

# Function: auth/validate_token
cat > "$DIR/auth/validate_token.md" << 'NODEEOF'
---
id: auth/validate_token
type: function
namespace: default
status: active
source:
  path: src/auth/validate_token.ts
  lines: [1, 20]
  hash: def456
edges:
  - target: config/jwt_secret
    relation: reads
---

Validates a JWT token and returns the decoded payload. Used by middleware and the login flow.

## Relationships

- **reads** [[config/jwt_secret]]

## Context

```typescript
// src/auth/validate_token.ts
import { verify } from 'jsonwebtoken';
import { JWT_SECRET } from '../config';

export function validateToken(token: string): TokenPayload {
  try {
    return verify(token, JWT_SECRET) as TokenPayload;
  } catch {
    throw new AuthError('Invalid or expired token');
  }
}
```
NODEEOF

# Function: auth/logout
cat > "$DIR/auth/logout.md" << 'NODEEOF'
---
id: auth/logout
type: function
namespace: default
status: active
source:
  path: src/auth/logout.ts
  lines: [1, 15]
  hash: ghi789
edges:
  - target: db/sessions
    relation: writes
---

Invalidates the current session by removing it from the sessions store.

## Relationships

- **writes** [[db/sessions]]

## Context

```typescript
// src/auth/logout.ts
import { deleteSession } from '../db/sessions';

export async function logout(sessionId: string): Promise<void> {
  await deleteSession(sessionId);
}
```
NODEEOF

# Module: db
cat > "$DIR/db.md" << 'NODEEOF'
---
id: db
type: module
namespace: default
status: active
edges:
  - target: db/users
    relation: contains
  - target: db/sessions
    relation: contains
---

Database access layer. Provides typed query functions for the users and sessions tables.

## Relationships

- **contains** [[db/users]]
- **contains** [[db/sessions]]

## Context

```typescript
// src/db/index.ts
export { findUser, createUser } from './users';
export { createSession, deleteSession } from './sessions';
```
NODEEOF

# Function: db/users
mkdir -p "$DIR/db"
cat > "$DIR/db/users.md" << 'NODEEOF'
---
id: db/users
type: function
namespace: default
status: active
source:
  path: src/db/users.ts
  lines: [1, 25]
  hash: jkl012
edges: []
---

Queries the users table. Provides findUser (by email) and createUser operations.

## Relationships

## Context

```typescript
// src/db/users.ts
import { pool } from './pool';

export async function findUser(email: string): Promise<User | null> {
  const result = await pool.query('SELECT * FROM users WHERE email = $1', [email]);
  return result.rows[0] || null;
}

export async function createUser(email: string, passwordHash: string): Promise<User> {
  const result = await pool.query(
    'INSERT INTO users (email, password_hash) VALUES ($1, $2) RETURNING *',
    [email, passwordHash]
  );
  return result.rows[0];
}
```
NODEEOF

# Function: db/sessions
cat > "$DIR/db/sessions.md" << 'NODEEOF'
---
id: db/sessions
type: function
namespace: default
status: active
source:
  path: src/db/sessions.ts
  lines: [1, 20]
  hash: mno345
edges: []
---

Manages session records in the sessions table. Provides create and delete operations for session lifecycle.

## Relationships

## Context

```typescript
// src/db/sessions.ts
import { pool } from './pool';

export async function createSession(userId: string): Promise<Session> {
  const result = await pool.query(
    'INSERT INTO sessions (user_id, expires_at) VALUES ($1, NOW() + INTERVAL \'1 hour\') RETURNING *',
    [userId]
  );
  return result.rows[0];
}

export async function deleteSession(sessionId: string): Promise<void> {
  await pool.query('DELETE FROM sessions WHERE id = $1', [sessionId]);
}
```
NODEEOF

# Concept: config/jwt_secret
mkdir -p "$DIR/config"
cat > "$DIR/config/jwt_secret.md" << 'NODEEOF'
---
id: config/jwt_secret
type: concept
namespace: default
status: active
edges: []
---

JWT signing secret loaded from environment variable JWT_SECRET. Used by auth/validate_token to verify tokens.

## Relationships

## Context

```typescript
// src/config/index.ts
export const JWT_SECRET = process.env.JWT_SECRET || 'dev-secret-change-me';
```
NODEEOF

echo ""
echo "=== Vault created with 8 nodes ==="
echo ""
echo "Try these commands:"
echo ""
echo "  # Verify graph integrity"
echo "  $MARMOT verify --dir $DIR"
echo ""
echo "  # Query for authentication-related code"
echo "  $MARMOT query --dir $DIR --query 'user authentication login'"
echo ""
echo "  # Query for database operations"
echo "  $MARMOT query --dir $DIR --query 'database query users'"
echo ""
echo "  # Query with small token budget (see truncation)"
echo "  $MARMOT query --dir $DIR --query 'authentication' --budget 500"
echo ""
echo "  # Open in Obsidian: just open $DIR as a vault"
echo ""
