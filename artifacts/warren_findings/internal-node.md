# internal/node

Small, warren-agnostic package (parser.go, writer.go, store.go, types.go + tests). No warren, daemon, embedding, manifest, flock, or routes references. It matters to the plan in three ways: (a) it contains a *second* frontmatter splitter with the same delimiter-anywhere family of bug as warren.go's `parseMarkdownYAML` (Tier 1.7 must fix both or the regression test suite is incomplete); (b) `node.NewStore` is instantiated at 12 non-test sites including remote/registry paths that Tier 2 Rebuild logic touches; (c) its atomic-write and hermetic t.TempDir test patterns are the templates the test program should reuse.

### parser.go:47-69 — splitFrontmatter (Tier 1.7 parallel bug)
Relevance: warren_review.md 1.7 cites only `warren.go:1110-1125` (`parseMarkdownYAML`). This package has an independent splitter with a related defect: the closing delimiter search `strings.Index(rest, "\n---")` (line 60) accepts any line *beginning* with `---` (e.g. `----`, `--- foo`, or a `---` line inside a YAML block scalar / multiline value) as the close, and `closingIdx+4` (line 66) assumes exactly `\n---` with body following. A frontmatter value containing a line starting with `---` truncates the YAML; a body is fine (body `---` occurs after the real close). Fix 1.7's anchored-regex approach should be applied/shared here; the review's claim that the bug is only in warren.go is incomplete for this folder.
```go
57	afterOpen := strings.Index(trimmed, "---") + 3
58	rest := trimmed[afterOpen:]
59
60	closingIdx := strings.Index(rest, "\n---")
61	if closingIdx < 0 {
62		return nil, "", fmt.Errorf("missing YAML frontmatter closing ---")
63	}
64
65	fm := rest[:closingIdx]
66	body := strings.TrimLeft(rest[closingIdx+4:], "\r\n")
```
Note: `RenderNode` (writer.go:68-70) emits `---\n<yaml>---\n`, so round-trip of a node whose Summary/Context/YAML value contains a line-leading `---` is the regression test the plan's "frontmatter `---`-in-body round-trip" item (review line 161) should target for BOTH parsers.

### parser.go:17-43, 130-145 — ParseNode / ParseNodeMeta signatures (API-stability constraint)
```go
17	func ParseNode(data []byte, filePath string) (*Node, error)
130	func ParseNodeMeta(data []byte, filePath string) (*NodeMeta, error)
```
Consumed widely (graph.LoadGraph via Store, api/handlers, mcp/engine, pipeline). Keep signatures stable; only splitFrontmatter internals need changing.

### store.go:59-61 — NewStore call sites (Tier 2 registry/Rebuild wiring)
```go
59	func NewStore(basePath string) *Store {
```
Non-test call sites the plan will touch when adding always-create VaultRegistry + Rebuild and refresh-under-search:
- internal/namespace/inventory.go:152, internal/namespace/registry.go:125 (registry paths)
- internal/mcp/engine.go:146 (buildEngine-side store)
- internal/daemon/owner.go:335 (`graph.LoadGraph(node.NewStore(dir))` — the owner watcher reload path; any reloadWarrenState helper will follow this pattern)
- internal/api/handlers.go:412, 858 (`node.NewStore(mount.Path)` — ActiveMounts/mount-path consumers; editable+materialized write-loss fix (Tier 1) lands where these stores write into mount paths)
- cmd/marmot/pipeline.go:51, 776, 955, 1076, 1209, 1283

### store.go:129-174 — SaveNode atomic temp+rename (Tier 1 copyDir/manifest-RMW template)
```go
129	func (s *Store) SaveNode(node *Node) error {
147	tmp, err := os.CreateTemp(dir, ".node-*.md.tmp")
168	if err := os.Rename(tmpPath, target); err != nil {
```
Relevance: the exact atomic-write pattern (temp in same dir, defer-cleanup `success` flag, rename) the Tier 1 fixes for `_warren.md`/routes.yml RMW and copyDir hardening should reuse. Note: no fsync — copyDir hardening may want to add it.

### store.go:228-270 — ListNodes silent-skip semantics (Tier 1.6 adjacent)
Files failing `ParseNodeMeta` are silently skipped (lines 256-259, `return nil // skip malformed files`), and dirs/files starting with `_` or `.` are skipped (238-245). Constraint: warren burrow/import copies must preserve `_`-prefixed control files even though this walker ignores them; also parser corruption from the 1.7 bug degrades silently here (no stderr warning), matching the review's swallowed-errors theme though it does not cite this site.

### node_test.go:321-401, 403-447, 593-630 — reusable test templates
Relevance: test program. `TestRoundtrip` (321) is the template for the `---`-in-body round-trip regression; `TestStore_SaveAndLoad` (403) and `TestAtomicWrite_NoPartialFile` (593) show the hermetic `t.TempDir()` vault-store setup with zero env dependence — no `MARMOT_ROUTES` isolation needed in this package (it never reads routes), so no hermeticity bug here.

### types.go:75-94 — NodeMeta / IsActive
`Status == "" || Status == StatusActive` (line 94) is the activeness rule remote/warren search must respect; no changes needed, listed as a constraint.

## Review-accuracy flags
- warren_review.md 1.7 scopes the frontmatter bug to `warren.go:parseMarkdownYAML` only; `internal/node/parser.go:60` has a sibling defect (closing `---` matched as prefix, not exact line; breaks on `---`-leading lines inside frontmatter values). Plan should fix/factor both, ideally into one shared anchored splitter.
- No line drift found for this folder (the review cites no internal/node lines directly).
