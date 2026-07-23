# JSON contracts (stave / automation)

Versioned stdout envelopes for `marmot den` (and later warren) `--json` verbs.

- **Schema field:** every document has `"schema": 1` (integer). Consumers negotiate on
  `schema`, never by parsing the marmot binary version. Evolution is **additive only**.
- **Errors:** structured error objects on stdout with non-zero exit codes (not only stderr).
- **Hand-built:** envelopes are dedicated structs — internal types are never marshalled
  directly.

Fixtures pin the **schema:1** envelopes for dens/routes JSON verbs that are
implemented (create/status/list/destroy/link/contribute/route set-project/warren
propose and structured errors). Update them in the same PR as intentional schema
changes; live CLI tests cover behavioral equality for selected fields.

### Fixtures

| File | Verb / purpose |
|------|----------------|
| `den_create.v1.json` | `den create --json` success (pointer written) |
| `den_create_no_pointer.v1.json` | stave attach: `den create … --no-pointer --json` (`pointer_written: false`) |
| `den_create_with_links.v1.json` | create + `links[].resolved_via` |
| `den_status.v1.json` | `den status --json` (link freshness §9: state ok/unpushed/stale/unreachable, additive `source_commit` skew) |
| `den_destroy.v1.json` | destroy + promote counts |
| `den_destroy_contributed.v1.json` | destroy after contribute |
| `dry_run.v1.json` | `--dry-run --json` ops list |
| `error.v1.json` | structured error on stdout (non-zero exit) |
| `den_list.v1.json` | `den list --json` |
| `den_create_duplicate.v1.json` | duplicate den-id create error |
| `den_create_project_collision.v1.json` | project path already owned |
| `route_set_project.v1.json` | `route set-project --json` (D6 archive) |
| `warren_status_additive.v1.json` | additive warren status fields (P2/P3) |
| `warren_add.v1.json` | `warren add --json` success (shared cache clone + pinned checkout) |
| `warren_sync.v1.json` | `warren sync --json` per-warren results (exit 1 only when every warren failed) |
| `den_link.v1.json` | `den link --edit --json` success (cache-backed: additive `worktree` + `branch`; legacy checkout links omit both) |
| `den_contribute.v1.json` | `den contribute --json` success (edit branch + counts) |
| `den_adopt.v1.json` | `den adopt --json` success (vault moved + configs rewritten) |
| `warren_propose.v1.json` | `warren propose --json` success (never auto-pushes) |
| `resolve.v1.json` | `marmot resolve --json` reference-resolution diagnostic (§15.5) |
| `den_unlink.v1.json` | `den unlink --json` success (removed link bodies) |
| `den_bridge_list.v1.json` | `den bridge list --json` (den-scoped cross-vault bridges, §7) |

Every envelope stave parses is mirrored into stave's
`internal/memory/testdata/contracts/` (currently: `den_create*.v1.json` except
the duplicate/collision errors, `den_status`, `den_destroy*`, `den_link`,
`den_contribute`, `dry_run`, `error`, `warren_status_additive`,
`warren_propose`, `warren_sync`). These files are the source of truth; keep
the two repos byte-identical when syncing. Envelopes stave never issues
(`resolve`, `den_unlink`, `den_bridge_list`, `warren_add`, …) are not mirrored.

### Canonical repo URL form (§15.5)

Reference resolution (`marmot resolve`, den `--ref`) matches repo URLs against
warren manifest-v3 `source_url` values in **canonical form**: `host/path` with

- the scheme dropped (`https://`, `ssh://`, `git://`, `file://`, …),
- any `user[:password]@` prefix dropped,
- the scp-style `host:path` separator rewritten to `/`,
- the host lowercased (the path keeps its case), and
- a trailing `.git` suffix and trailing slashes stripped.

So `git@github.com:x/y.git` ≡ `https://github.com/x/y` ≡
`ssh://git@github.com/x/y/` → `github.com/x/y`. Canonicalization is
implemented **marmot-side only** (`warren.CanonicalRepoURL`); consumers pass
raw URLs and compare the canonical strings marmot emits — never reimplement
the rules. `resolve.v1.json` pins the diagnostic envelope
(`resolved_via` ∈ {`warren-url`, `checkout-vault`, `none`}).

**Stave** pins these files (or copies) for the `internal/memory` marmot provider.
Negotiate on `schema`, never by parsing the marmot binary version.

See also: `docs/dens.md`, `artifacts/stave_alignment/synthesis_plan.md` §13 OQ15 / §15.6 / §17.
