# TypeScript SDK

ContextMarmot can generate a type-safe TypeScript SDK from its MCP tool schemas --- a single self-contained `.ts` file with full interfaces and a ready-to-use client class.

## Generate locally

```bash
# Write to file
marmot sdk --out ./marmot-sdk.ts

# Pipe to stdout
marmot sdk

# Custom server URL
marmot sdk --base-url http://myserver:8080 --out ./sdk.ts
```

## Fetch from running server

```bash
# When marmot ui is running, the SDK is served at /sdk.ts
curl http://localhost:3000/sdk.ts > marmot-sdk.ts
```

## Usage

```typescript
import { MarmotClient } from './marmot-sdk';

const client = new MarmotClient('http://localhost:3000');

// Semantic query
const result = await client.query({ query: 'authentication flow', depth: 3 });

// Write a node
await client.write({
  id: 'auth/jwt-handler',
  type: 'function',
  summary: 'Validates and decodes JWT tokens',
  edges: [{ target: 'auth/middleware', relation: 'calls' }],
});

// Check graph health
const health = await client.verify({ check: 'all' });

// Read graph data directly
const graph = await client.getGraph('default');
```

## What's included

- Full TypeScript interfaces for all 5 MCP tools (`query`, `write`, `verify`, `delete`, `tag`)
- `MarmotClient` class with typed methods for tools + graph read APIs
- Domain types: `MarmotNode`, `MarmotEdge`, `GraphData`, `HeatPair`, `BridgeInfo`
- JSDoc comments on all interfaces and methods
- Zero dependencies --- works with any fetch-compatible runtime (Node 18+, Deno, Bun, browsers)

## Why local generation?

The SDK is generated from the exact tool schemas compiled into your `marmot` binary, so it's always in sync. No npm package to version separately. Regenerate after upgrading marmot.
