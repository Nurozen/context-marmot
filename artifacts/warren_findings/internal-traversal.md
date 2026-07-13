# internal/traversal — warren-review facts

## internal/traversal

The warren_review.md inventory does not cite any line in this folder (grep for "traversal" in the review returns nothing), and no misreads were found. This package is nonetheless load-bearing for Tier 2 ("always-create VaultRegistry + Rebuild", refresh-safe-under-concurrent-search) because it defines the interface VaultRegistry must satisfy and it silently swallows resolve errors (relevant to Tier 1 error un-swallowing and Tier 3 unreachable-warren surfacing).

### bridged.go:10-22 — VaultGraphProvider interface + BridgedGraphResolver (API constraint for Tier 2)
Any "always-create VaultRegistry + Rebuild/TTL" change must keep `ResolveGraph(vaultID string) (*graph.Graph, error)` stable — this single-method interface is the only contract traversal has with `namespace.VaultRegistry` (registry.go:75-76 implements it). Swapping the registry instance atomically behind this interface is the safe refresh point for "refresh under concurrent search": `BridgedGraphResolver` is constructed per-request in mcp/engine.go, so replacing `Engine.VaultRegistry` (or making ResolveGraph internally rebuild) requires no traversal changes.

```go
10	// VaultGraphProvider resolves graphs for remote vaults by vault ID.
11	// Implemented by namespace.VaultRegistry.
12	type VaultGraphProvider interface {
13		ResolveGraph(vaultID string) (*graph.Graph, error)
14	}
...
19	type BridgedGraphResolver struct {
20		Local  *graph.Graph
21		Vaults VaultGraphProvider // nil = single-vault mode
22	}
```

### bridged.go:27-50 and 55-63 — errors from ResolveGraph are swallowed (Tier 1 error un-swallowing / Tier 3 unreachable-warren surfacing)
`GetNode` returns `nil,false` and `GetEdges` returns `nil` when `ResolveGraph` errors — an unreachable/broken warren vault is indistinguishable from a nonexistent node. GraphResolver's signature (`GetNode(id) (*node.Node, bool)`) has no error channel, so surfacing must happen at the provider (VaultRegistry logging/collecting resolve failures) rather than here. Note GetNode deep-copies Edges (lines 43-48), so a registry Rebuild that replaces cached graphs cannot corrupt in-flight traversal results via edge mutation — supports concurrent-refresh safety.

```go
32	g, err := r.Vaults.ResolveGraph(vaultID)
33	if err != nil {
34		return nil, false
35	}
...
60	g, err := r.Vaults.ResolveGraph(vaultID)
61	if err != nil {
62		return nil
63	}
```

### bridged.go:82-92 — parseVaultPrefix (vault-ID grammar constraint)
`"@vault-id/node-id"` split on first `/`; vault IDs therefore cannot contain `/`. Relevant to the Tier 3 vault-ID collision refusal rules and the MCP-vs-API @-write asymmetry: whatever ID validation the plan adds must match this parser.

```go
82	func parseVaultPrefix(id string) (string, string) {
83		if !strings.HasPrefix(id, "@") {
84			return "", id
85		}
86		rest := id[1:]
87		idx := strings.Index(rest, "/")
88		if idx < 0 {
89			return "", id
90		}
91		return rest[:idx], rest[idx+1:]
92	}
```

### traversal.go:15-18 — GraphResolver interface (stability constraint)
`Traverse` (traversal.go:43) and `Compact` consume this; mcp/handlers.go:118-130 call `traversal.Traverse(resolver, cfg)` then `traversal.Compact(resolver, subgraph, budget)`. Plan should not need to touch these signatures.

```go
15	type GraphResolver interface {
16		GetNode(id string) (*node.Node, bool)
17		GetEdges(id string, direction graph.Direction) []node.Edge
18	}
```

### bridged_test.go:93-98 — mockVaultProvider (reusable test template)
In-memory `map[string]*graph.Graph` fake for VaultGraphProvider — the right template for hermetic warren traversal tests (no disk, no MARMOT_ROUTES). Used throughout bridged_test.go, bridged_integration_test.go, and bridged_stress_test.go (as `stressVaultProvider`); the multi-vault + superseded-node + budget-truncation scenarios in bridged_integration_test.go:533-664 are directly reusable patterns for the warren e2e test program.

```go
93	// mockVaultProvider implements VaultGraphProvider for testing.
94	type mockVaultProvider struct {
95		graphs map[string]*graph.Graph
96	}
98	func (m *mockVaultProvider) ResolveGraph(vaultID string) (*graph.Graph, error) {
```

## Consumers outside this folder (call sites the plan touches)
- cmd/marmot/pipeline.go:235-275 — buildEngine warren wiring: `warren.ActiveMounts(dir)` at :242, `rt.Set(mount.VaultID, mount.Path)` at :245 (gated on `mount.Available`), `warrenRuntimeBridges` at :248, and conditional `namespace.NewVaultRegistry(vaultID, dir, bridges, rt)` + `engine.WithVaultRegistry(vr)` at :272-273 — this is the exact conditional (`hasCrossVaultBridges || hasRoutes` at :266-267) Tier 2 "always-create VaultRegistry" removes.
- internal/mcp/engine.go:291-310 — `WithVaultRegistry(vr *namespace.VaultRegistry)` (:292) stores the registry and caches LocalVaultID; `graphResolver()` (:303-310) builds a fresh BridgedGraphResolver per call — natural hook for reloadWarrenState (swap `e.VaultRegistry`).
- internal/namespace/registry.go:75-76 — `func (r *VaultRegistry) ResolveGraph(vaultID string) (*graph.Graph, error)` is the concrete implementation (lazy load; where remote-graph cache TTL / Rebuild lands).
