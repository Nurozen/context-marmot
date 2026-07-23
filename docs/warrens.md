# Warrens

A Warren is a git-backed collection of project Marmot vaults. It is useful when
several repositories belong to the same product, platform, or organization and
agents need cross-project context without cloning every codebase into one
repository.

> **Dens:** central dens under `$MARMOT_HOME` coexist with warrens.
> Use dens for per-project identity vaults and reverse routes; keep warrens for
> multi-project graph mounts. See [dens.md](./dens.md).
> Everything below is the **shipping** warren model.

Warrens are mounted explicitly. Registered projects stay dormant until activated
with `marmot warren mount`, so large company graphs do not become queryable by
accident.

## Quickstart: zero to first bridge

One workspace (`my-project`) wants its queries to traverse a bridge into a
second project (`pb`) published through a Warren. Everything below is
copy-pasteable; adjust paths.

**0. Give your workspace an identity.** A `vault_id` in `.marmot/_config.md`
is the one prerequisite for bridges involving your *own* project: the
Warren's copy of your project is recognized as *you* by comparing its
`vault_id` with yours.

```bash
cd ~/src/my-project
marmot configure --vault-id my-project
```

**1. Author the Warren** (in the warren repository, usually its own git
repo):

```bash
mkdir product-warren && cd product-warren && git init
marmot warren init --id product-platform
# Import your own project WITHOUT --vault-id so the copy keeps your
# vault_id — that is what identifies it with your workspace:
marmot warren project import my-project ~/src/my-project/.marmot
marmot warren project import pb ~/src/pb/.marmot --vault-id pb-vault
marmot warren bridge add my-project pb --relations references,calls
marmot warren doctor            # exits 0 when the manifest is coherent
```

**2. Consume it** (back in your workspace):

```bash
cd ~/src/my-project
marmot warren register product-platform /path/to/product-warren
# register announces: project "my-project" ... matches this workspace's vault ID
marmot warren mount --warren product-platform pb
```

(For a warren published at a git URL, `marmot warren add <url>` + a
register-free `mount` replaces the `register` step — see
[Consume a Warren](#consume-a-warren).)

Identity is automatic: your own project needs **no mount** (mounting it is a
harmless no-op) — mounting the *other* endpoint is the single deliberate act
that turns the manifest bridge on.

**3. What you should see:**

```bash
marmot warren status --warren product-platform
# PROJECT     STATE     EDITABLE  AVAILABLE  PATH
# my-project  identity  false     true       /Users/you/src/my-project/.marmot
# pb          mounted   false     true       /path/to/product-warren/projects/pb/.marmot

marmot warren doctor --workspace   # exits 0, with a self_identity info line
marmot query --query "..."         # may return @pb-vault/... nodes across the bridge
```

Cross-vault edges into the mounted project use qualified IDs
(`@pb-vault/service/api`) and validate against the Warren's bridge policy.
The rest of this document is the reference behind each step.

## Repository layout

A Warren has a top-level `_warren.md` manifest and project vaults below
`projects/<project-id>/.marmot/`.

```text
product-warren/
  _warren.md
  projects/
    project-a/
      .marmot/
        _config.md
        _warren.md
        ...
    project-b/
      .marmot/
        _config.md
        _warren.md
        ...
```

Example top-level manifest:

```yaml
---
warren_id: product-platform
version: 1
projects:
  - project_id: project-a
    path: projects/project-a/.marmot
  - project_id: project-b
    path: projects/project-b/.marmot
bridges:
  - source: project-a
    target: project-b
    relations: [calls, reads, references, cross_project]
---

# Product Platform Warren
```

Each project has its own `.marmot/_warren.md` identity file:

```yaml
---
project_id: project-a
warren_id: product-platform
vault_id: project-a-vault
aliases:
  - payments-api
---
```

`vault_id` is the ID used in qualified node references such as
`@project-a-vault/service/api`.

## Workspace layout

Warren state is local to the workspace and stored in the workspace `.marmot`
directory:

```text
virtual-mono/
  .marmot/
    _config.md
    _warren.md
    .marmot-data/
      warrens/
        product-platform/
          projects/
            project-b/
              .marmot/
                ...
  project-a/
  project-b/
```

The workspace `_warren.md` records registered Warren paths, active projects,
editable projects, and whether materialized caches are enabled. This is local
workspace configuration; keep the Warren repo itself in git.

Mutations of both `_warren.md` roles are serialized across processes with a
sibling `_warren.md.lock` flock file, so concurrent `marmot warren` commands
never drop each other's changes. The lock file is inert local state:
`marmot warren init` adds `_warren.md.lock` to the Warren repo's `.gitignore`,
and the workspace-side lock lives under `.marmot/` next to the state it
guards. The same mechanism protects `~/.marmot/routes.yml`
(`routes.yml.lock`). Locks are released by the kernel when a process exits —
even on SIGKILL — so there is never a stale lock to clean up. (On network
filesystems BSD flock semantics vary by server; on Windows the lock degrades
to today's last-writer-wins behavior.)

## Build and maintain a Warren

Run authoring commands inside a Warren repository, or pass `--warren-dir` to
point at one.

Create the top-level manifest:

```bash
marmot warren init --id product-platform
```

Add projects explicitly:

```bash
marmot warren project import project-a ../project-a/.marmot \
  --vault-id project-a-vault \
  --alias payments-api
```

Use `project import` when a project already has a local `.marmot/` vault and
you want to copy it into the Warren under `projects/<project-id>/.marmot`.
Import copies regular files only, skips symlinks and device/socket files, strips
obvious inline secret fields or API-key-looking values from `_config.md`, and
always excludes transient or sensitive files:

- `.marmot-data/.env`
- `.marmot-data/embeddings.db-wal`
- `.marmot-data/embeddings.db-shm`
- `.obsidian/workspace.json`
- `.obsidian/workspace-mobile.json`

Before copying, import checkpoints the source `embeddings.db` (flushing its
write-ahead log into the main database file), so excluding the `-wal`/`-shm`
sidecars never loses recent writes — the copy is a complete point-in-time
snapshot even while a `marmot serve` holds the database open.

Materialized (burrowed) copies use the same hardened copier and the same
exclusion list: burrowing a project never copies `.marmot-data/.env` or DB
sidecars into the local cache, never follows symlinks, and skips FIFOs and
other irregular files. Re-burrowing replaces the cache atomically, so files
deleted from the Warren checkout disappear from the cache instead of being
resurrected.

Each burrow cache carries a `provenance.md` sibling recording the checkout
commit it was copied from (`source_commit`, empty for non-git warrens), the
manifest path it came from, and the materialization time. `marmot warren
status` renders it (`cache at <commit> (N behind)` when git can compute a
distance, else `cache from <timestamp>`), and `marmot warren refresh --pull`
uses it to re-materialize only stale caches. A cache without provenance is
simply treated as stale and re-copied on the next `refresh --pull`.

`_heat/` is excluded by default; pass `--include-heat` to keep it. Harmless
`.obsidian/` configuration is copied by default; pass `--no-obsidian` to omit
the whole directory. Import rewrites the copied project `.marmot/_warren.md` to
the target Warren/project identity, but it does not make the project read-only,
create package metadata, register the Warren in your workspace, mount projects,
commit changes, or push to git.

Without `--vault-id`, import preserves the source vault's `vault_id` — which
is what makes *identity* work: workspaces whose `_config.md` carries that
same `vault_id` recognize the imported copy as themselves (see "Consume a
Warren"). Identity is keyed on `vault_id` alone, so a vault_id-preserving
re-import (`project remove` + `project import`) re-establishes it
automatically, and importing with a distinct `--vault-id` is the documented
opt-out: the copy becomes a foreign project, mountable and burrowable like
any other.

If you want Marmot to choose the import project ID from the source
`.marmot/_warren.md` metadata or the source folder name, use:

```bash
marmot warren project import --generate-id ../payments/.marmot
```

Use `project add` when the vault is already placed in the Warren and you only
need to register it in the manifest:

```bash
marmot warren project add project-a \
  --path projects/project-a/.marmot \
  --vault-id project-a-vault \
  --alias payments-api
```

`project_id` is durable command and UI identity. It is explicit by default so it
can outlive folder renames. If you want Marmot to choose a conservative ID from
existing project metadata or the path folder name, use:

```bash
marmot warren project add --generate-id --path projects/payments/.marmot
```

Maintain project entries:

```bash
marmot warren project list
marmot warren project list --json
marmot warren project rename project-a payments-api
marmot warren project remove payments-api
```

`project rename` also moves the conventional `projects/<old-id>/` directory
to `projects/<new-id>/` (pass `--keep-path` to rename the ID only;
unconventional paths are never moved, and an existing target directory is
refused). The move is a plain filesystem rename, so `git add -A` in the
warren repo records it as a rename. The project's `vault_id` is deliberately
untouched — it is the identity key consumers route (and identify their own
workspace) by, so it must stay stable across renames; change it only by
re-importing with `--vault-id`. The command says what happened to both:

```text
Renamed project "api" -> "api-service" (moved projects/api -> projects/api-service)
note: vault_id "api-vault" unchanged — vault identity is stable across renames; re-import with --vault-id to change it
```

Add Warren-owned bridge policy between projects:

```bash
marmot warren bridge add project-a project-b --relations calls,reads,references
marmot warren bridge list
marmot warren bridge remove project-a project-b
```

Validate and normalize the Warren:

```bash
marmot warren doctor
marmot warren doctor --json
marmot warren format
```

`doctor` checks the top-level manifest, project paths, project identity files,
ID consistency, duplicate vault IDs, bridge endpoints, bridge relations,
accidental materialized cache folders, and missing embedding databases. Missing
embeddings are warnings because a graph can be valid before it is indexed. It
also warns on cross-project **embedding model skew** (`model_skew`: projects
indexed with different models cannot see each other in semantic search), on
project databases indexed by an older marmot (`schema_stale`: missing the
status column; re-import the project), on absolute manifest paths
(`absolute_project_path`: they only resolve on the author's machine), and — in
git-backed warrens — notes a missing `_warren.md.lock` gitignore entry
(`lockfile_not_ignored`). All DB inspection is strictly read-only.

`marmot warren doctor --workspace` runs in a consuming workspace instead of a
warren repo and agrees with mount: it errors exactly where mount refuses and
is healthy exactly where mount permits. Its codes:

- `vault_id_collision_workspace` (error) — two claimants that are not the
  local vault and its identified projects share a `vault_id`; queries
  resolve to one of them arbitrarily. Unmount or re-import with distinct
  vault IDs.
- `self_identity` (info) — a Warren project is identified with this
  workspace (its `vault_id` matches); it serves from the live vault.
  Healthy state, reported whether or not anything is mounted.
- `self_alias_mount` (info) — an identified project also has a redundant
  self-mount recorded (state written by an older binary); identity is
  automatic. Clean with `marmot warren unmount --warren <id> <project>`
  (optional; harmless if kept — but note an older binary driving the
  workspace after cleanup loses bridge activation, since pre-identity
  binaries require the mount).
- `self_alias_editable` (error) — legacy state marks an identified project
  editable; `@`-writes would split-brain. Run
  `marmot warren edit <project> --warren <id> --off`.
- `self_alias_materialized` (warning) — a legacy burrow cache shadows the
  workspace's own vault. Drop it with
  `marmot warren burrow --drop --warren <id> <project>`.
- `local_route_mismatch` (warning) — the global routing table maps this
  workspace's `vault_id` to a different path (e.g. after a manual
  `marmot route add`); peers resolving that route read the other copy. A
  warning, not an error: two checkouts of one repo legitimately share a
  `vault_id`.

These commands edit Warren files atomically but never commit, push, pull, or
open PRs. Use normal git workflow to review and publish Warren changes.

## Consume a Warren

### Shared cache (`warren add` / `warren sync`)

The preferred way to consume a warren that lives in a git remote is the
**shared cache** — one clone per machine instead of one user-managed
checkout per workspace:

```bash
marmot warren add https://github.com/acme/product-warren   # or a local path
marmot warren add <url> --id product-platform              # override the URL-derived id
marmot warren sync [<id>]                                  # fetch + re-pin (all warrens when no id)
```

`warren add` clones a bare mirror into `$MARMOT_HOME/warren-cache/<id>.git`,
records the id → clone-URL mapping in the global registry
`$MARMOT_HOME/warrens.yml`, and materializes one shared read checkout at
`$MARMOT_HOME/warren-cache/checkouts/<id>`, pinned to a commit (recorded in
`checkouts/<id>.pin`). `warren sync` fetches each cached warren and re-pins
its shared checkout; per-warren failures ride along without aborting the
loop, and the exit code is non-zero only when *every* warren failed. Both
verbs speak `--json` (`testdata/contracts/warren_add.v1.json`,
`warren_sync.v1.json`).

After pinning, both verbs **regenerate project-vault embeddings keyed to the
pin**: contribute PRs carry markdown only, so a node merged upstream has no
row in the checkout's `.marmot-data/embeddings.db` until add/sync re-embeds
it with that vault's own configured embedder (summary-hash checks keep the
nothing-changed case cheap). The per-warren `reembedded` count is reported in
the JSON envelopes. The reembed never mixes embedding models: a store that
already holds rows from a different model is skipped with a warning naming
both models, and a vault whose configured provider has no API key is skipped
rather than silently written with mock vectors.

Cache-backed warrens are **register-free**: `warren mount`, `warren status`,
and `warren list` resolve the id from the cache directly (list marks such
entries `[cache]`), and `den link --edit` into a cache-backed warren uses a
dedicated edit worktree instead of any user checkout (see
[dens.md](./dens.md)). The `warren register` flow below remains fully
supported for local, user-managed checkouts (register prints a stderr note
pointing at `warren add`); everything from here down applies to both kinds.

### Registered checkouts

Register a Warren in the current workspace:

```bash
marmot warren register product-platform /path/to/product-warren
```

List registered Warrens:

```bash
marmot warren list
marmot warren list --json
```

Activate selected projects:

```bash
marmot warren mount --warren product-platform project-a project-b
```

A bare `mount` (no project IDs) refuses rather than silently activating
every registered project — nothing becomes queryable by accident. To
activate the whole Warren, say so explicitly:

```bash
marmot warren mount --warren product-platform --all
```

Mounting refuses a project whose `vault_id` is already claimed in this
workspace, unconditionally (vault IDs are one flat routing namespace per
workspace; a duplicate would silently answer queries from the wrong
project). The one non-conflict is the Warren copy of the *local* project —
same vault ID as the workspace vault. That project is **identified** with
this workspace: a project whose checkout `vault_id` matches your
`.marmot/_config.md` `vault_id` *is* your workspace. Identity is derived and
always on — register the Warren and it is already in effect; no mount, no
verb, no state file entry. An identified project never claims a route (the
live local vault is the sole answerer for its own vault ID — queries,
`@<vault-id>/…` references, and bridge traversal all resolve to the live
vault with zero staleness), and it can never be made editable or
materialized (a cache or writable copy would be a stale or split-brained
shadow of the live vault). Mounting it explicitly is a harmless no-op that
prints a note and records nothing; `unmount` and `burrow --drop` stay
available to clean up state recorded by older binaries. To opt out of
identity, re-import the Warren's copy with a distinct `--vault-id` (author
side) or unregister the Warren (consumer side).

Deactivate projects again (`unmount` is non-destructive: burrow caches are
kept, and it works even when the Warren checkout has been moved or deleted —
it is the escape hatch for unreachable Warrens):

```bash
marmot warren unmount --warren product-platform project-a
marmot warren unmount --warren product-platform --all
```

Show project state:

```bash
marmot warren status --warren product-platform
marmot warren status --warren product-platform --json
```

The status table's STATE column is `dormant`, `mounted`, or `identity` — an
identified project shows `identity` whether or not anything was ever
mounted, with EDITABLE always false and PATH the live workspace `.marmot`
(that is where its reads actually go). `status --json` carries the same rows
with the additive `self_alias` flag. `warren list` adds an IDENTITY column
listing each Warren's identified projects (`-` when none;
`"identified_projects"` in `--json`), and `GET /api/warrens` carries the
same computed field per Warren.

In `--json` mode stdout is exactly one JSON document; diagnostics
(unreachable-checkout banners, model-skew warnings, deprecation notices, the
vault_id nudge) go to stderr. Scripts can parse stdout whole without
scraping.

When the registered checkout no longer exists, `status` prints an
`UNREACHABLE` banner naming the re-register/unregister escape hatches and
still renders rows from workspace state (AVAILABLE=false), and
`warren list` shows a REACHABLE column (`"reachable"` in `--json`). A live
daemon owner logs the same condition through its reload warnings.

Enable writes for one project (edit implies mount — an unmounted project is
auto-mounted, and the command says so). Identified projects refuse `edit`:
they are read-through views of the live vault, so edit their nodes directly
in this workspace (no `@` prefix) instead; `--off` stays allowed as the
legacy-state escape hatch (it clears the flag without recording a mount):

```bash
marmot warren edit --warren product-platform project-a
```

Disable writes again:

```bash
marmot warren edit --off --warren product-platform project-a
```

### Burrows: local caches of mounted projects

A **burrow** is a materialized local copy of a mounted project's vault,
stored inside your workspace at
`.marmot/.marmot-data/warrens/<warren>/projects/<project>/.marmot`. Mounting
alone reads the project's graph live from the Warren checkout on every
query; burrowing snapshots it locally so queries keep working when the
checkout is unreachable (moved, deleted, on a detached network drive) or
when you want a stable view while the checkout changes underneath you. The
name follows the metaphor: a warren is the shared network of tunnels, a
burrow is your own local den dug from it.

Burrows are read-only snapshots — they are never edited (editable and
materialized are mutually exclusive per project) and never synced back.
`marmot warren refresh --pull` re-materializes stale burrows from the
updated checkout; `warren status` shows each cache's age and, for git-backed
warrens, how many commits behind it is.

Create burrows for selected projects:

```bash
marmot warren burrow --warren product-platform project-b
marmot warren burrow --warren product-platform --all
```

`burrow` always materializes — without a cache the verb would be exactly
`mount`. (A bare `burrow` requires project IDs or `--all`, like `mount`.)
Burrowing is useful when you want offline graph access or a stable local
snapshot while the Warren git checkout changes elsewhere. If materializing
fails partway, projects that were mounted but never cached are unmounted
again, so a failed burrow cannot leave mounted-but-uncached state.

Delete burrow caches (per project or all of them; the warren-level
materialized flag clears when the last cache is gone):

```bash
marmot warren burrow --drop --warren product-platform project-b
marmot warren burrow --drop --warren product-platform --all
```

Remove a Warren from the workspace entirely. Without `--force` it refuses
while projects are still mounted or burrow caches still exist and names the
exact commands to run first; with `--force` it also deletes the Warren's
cache tree:

```bash
marmot warren unregister --warren product-platform
marmot warren unregister --warren product-platform --force
```

Read-only verbs (`list`, `status`, `refresh`, `propose`) and the inverse
verbs (`unmount`, `unregister`, `burrow --drop`) never create a workspace:
in a directory without `.marmot/` they fail instead of planting a
mock-provider vault. Only `register`, `mount`/`burrow`, and `edit` create
the workspace on demand.

`marmot warren propose [--warren <id>] [<project-id>]` packages editable-mount
edits into a reviewable git artifact. It resolves the project (the sole
editable project by default; an explicit ID when there are several), refuses
non-git checkouts and detached HEADs, and — when the project has changes —
creates a timestamped `marmot/propose/<project>-<stamp>` branch holding one
pathspec-limited commit of just that project's files, then returns to the
original branch. Unrelated dirty or staged files are never swept in. Propose
is local-only by design: marmot never pulls, merges, rebases, or pushes —
publishing the branch (`git push -u origin <branch>`) and opening the PR stay
in your hands, and upstream divergence is resolved through normal git flow at
that point. A clean project prints `nothing to propose` and exits 0.
Embeddings DB sidecars under `.marmot-data/` (`embeddings.db` and its
WAL/SHM files) are derived binary state and are never proposed: they are
excluded from both the change scan and the commit, so a project whose only
changes are DB sidecars also reports `nothing to propose`.

For **den** edit links the packaging verb is `marmot den contribute` — it
commits on `marmot/edit/<den>/…` and reports the checkout plus a ready
`push_command`, after which `warren propose` sees a clean tree and reports
`nothing to propose`. Propose remains the path for uncommitted
editable-mount edits made outside the contribute flow (see
[dens.md](./dens.md)).

Propose refuses a project *identified* with this workspace (its `vault_id`
matches yours): your live context never lands in the Warren checkout, so
there is nothing meaningful to commit there. Default selection can never
pick one (identified projects are never editable); only an explicit
`marmot warren propose <self-project>` reaches the refusal. To refresh the
Warren's copy of your project, re-import it in the Warren repo
(`marmot warren project remove` + `marmot warren project import`) and commit
there.

## Bridge policy

Warren bridges are owned by the top-level Warren manifest:

```yaml
bridges:
  - source: project-a
    target: project-b
    relations: [calls, reads, references, cross_project]
```

At runtime, Marmot converts active Warren bridge endpoints from `project_id` to
their project `vault_id`s and uses the existing cross-vault validation path.
Edges between mounted Warren projects use qualified node IDs:

```yaml
edges:
  - target: "@project-b-vault/service/api"
    relation: calls
```

Both bridge endpoints must be active mounted projects *or identified with
this workspace*. An identified project (the Warren copy of this workspace's
own project, matched by `vault_id`) satisfies the endpoint requirement with
no mount at all and resolves to the live workspace vault, not the Warren
copy — mounting the *other* endpoint is the single deliberate act that turns
a manifest bridge on. Dormant foreign projects stay out of the queryable
graph even if a bridge references them, and relations not listed in the
Warren bridge are rejected on write.

## Read and write policy

Mounted Warren projects are read-only by default. They can be queried and viewed,
but Marmot will reject writes to mounted nodes unless that project has been made
editable in the local workspace:

```bash
marmot warren edit --warren product-platform project-a
```

Editability is per project, not per Warren. This supports virtual monorepo
workflows where an agent can reference many services but should only update graph
knowledge for repositories the user is actively editing. Identified projects
(the Warren copy of this workspace's own project) refuse editability
entirely: edit those nodes locally, without the `@` prefix.

When a Warren node is editable, API/UI updates write back to that project's own
`.marmot/` vault and embedding database. Read-only Warren nodes show provenance
in the detail panel and the save button is disabled.

Editable and materialized are mutually exclusive per project: a materialized
(burrowed) cache never syncs edits back to the checkout, so `marmot warren
edit` refuses projects that have a burrow cache (run `marmot warren burrow
--drop --warren <warren-id> <project-id>` or re-mount without materializing
first), and `marmot warren burrow` refuses projects
that are currently editable (run `marmot warren edit <project> --off`
first). If an older state file carries both flags, the checkout path wins
for editable projects and a warning is printed.

MCP `context_write` accepts `@vault-id/node-id` IDs for active **editable**
mounts, exactly like the API/UI path: the write updates that existing node's
summary/context/tags in the mounted project's own checkout (never a cache),
updates its embedding database, and refreshes the engine's cached view. Both
paths go through one shared write-back helper, so they cannot diverge.
Creating brand-new nodes through an `@`-write is not supported — create
nodes in the project's own workspace. Writes to read-only or unmounted
vaults are rejected with the command that would enable them, and
`@`-writes qualified with this workspace's *own* vault ID are always
rejected (write the node locally, without the `@` prefix — an identified
project is a read-through view of the live vault).

### Write policy (author side)

Consumers opt in to editability per workspace; the warren *author* can veto
it per project in the manifest:

```bash
marmot warren project set-readonly project-a          # in the warren repo
marmot warren project set-readonly project-a --off
```

```yaml
projects:
  - project_id: project-a
    path: projects/project-a/.marmot
    readonly: true
```

A `readonly` project cannot be made editable (`marmot warren edit` refuses),
reports `Editable=false` in status/mount provenance regardless of workspace
state (so the UI save button disables itself and MCP/API writes are
rejected), and — as a backstop against stale mount state — every write-back
re-reads the manifest at write time and refuses read-only projects even if an
old editable flag is still cached. Edits to such projects go through the
warren repository itself.

Using `readonly` lifts the manifest to schema **version 2**. Marmot binaries
read manifests newer than they understand best-effort (with a warning) but
refuse to *edit* them, so an older binary can never silently strip fields it
does not know.

## Source provenance (manifest v3) and reference resolution

`marmot warren project import` records where a project's vault came from as
per-project manifest fields — `source_url` (in canonical `host/path` form:
scheme and user dropped, host lowercased, trailing `.git`/`/` stripped, so
`git@github.com:x/y.git` ≡ `https://github.com/x/y` → `github.com/x/y`) and
`source_commit` (the source checkout's HEAD at import time):

```yaml
projects:
  - project_id: project-a
    path: projects/project-a/.marmot
    source_url: github.com/acme/project-a
    source_commit: 0123456789abcdef0123456789abcdef01234567
```

Capture is automatic when the source `.marmot` dir sits inside a git
checkout (origin remote URL + HEAD commit); `--source-url` /
`--source-commit` override it explicitly, and a non-git source simply leaves
the fields off. `marmot warren project add` accepts the same explicit flags
(no auto-detection — an in-warren path has no external source checkout).

These fields power reference-repo resolution. The diagnostic verb

```bash
marmot resolve --url <url> [--path <checkout>] [--json]
```

reports how a reference repo maps onto known knowledge, in resolution order:
`warren-url` (canonical-URL match against a registered cache-backed warren's
project `source_url` values), then `checkout-vault` (an in-checkout
`.marmot` vault at the path), else `none`. It shares the internal resolver
with den `--ref` handling, so its answer can never disagree with a den
link's. The `--json` envelope is pinned by
`testdata/contracts/resolve.v1.json`.

Writing a `source_url`/`source_commit` lifts the manifest to schema
**version 3**; manifests without source fields keep their loaded version and
round-trip untouched, so they stay editable by older binaries. The version
ceiling applies as ever: a pre-v3 marmot binary reads a v3 manifest
best-effort (with a warning) but refuses to edit it, so it can never
silently strip the provenance fields.

## Query behavior

Active Warren projects are included in MCP and CLI graph queries. Results from
mounted projects use qualified node IDs:

```xml
<node id="@project-a-vault/service/api" ...>
```

Plain local graph views stay local:

- `GET /api/graph/default` returns only local `default` nodes.
- `GET /api/search?q=...&ns=default` returns only local `default` results.
- `GET /api/warren/product-platform/graph` returns active mounted Warren nodes.
- `GET /api/search?q=...&ns=_warren/product-platform` returns Warren-scoped results.

The web UI exposes active Warrens in the graph selector as `Warren <id>`.

Cross-vault resolution reads the global routing table at
`~/.marmot/routes.yml` (populated by `marmot route add`, `marmot bridge`,
and warren registration). The `MARMOT_ROUTES` environment variable overrides
it: `MARMOT_ROUTES=off` (also `none` or `0`) disables the global table
entirely — useful for hermetic tests and scratch vaults that must not
inherit your real vault registry — and any other non-empty value is used as
the routes file path instead of the default.

## Freshness and refresh

Warrens track git, not real time. A live engine (a long-running daemon owner
in particular) keeps mounted state fresh through three explicit triggers plus
one time bound:

- `marmot warren refresh [--warren <id>]` verifies the Warren checkout is
  reachable, rewrites the workspace `.marmot/_warren.md` atomically (a no-op
  touch under its lock), and reports active mounts. Every live daemon owner
  watches that file and reloads its warren wiring (routes, mounts, runtime
  bridges, vault registry) within about a second. Without `--pull` it never
  touches git — run `git -C <warren-checkout> pull` yourself if you prefer.
- `marmot warren refresh --pull` additionally fast-forwards the checkout
  first: it requires a git work tree, **refuses a dirty checkout** (editable
  mounts legitimately write there, so marmot never stashes or forces —
  commit or stash yourself, or refresh without `--pull`), runs
  `git pull --ff-only` (non-fast-forward or network failures surface git's
  own error; resolve in the checkout manually), and then re-materializes
  every active burrow cache whose `provenance.md` commit no longer matches
  the fresh `HEAD` (or whose provenance is missing). The atomic cache swap
  makes this safe under a live engine.
- `POST /api/warren/{id}/refresh` reloads the serving engine's warren state
  directly. The web UI's refresh button calls this automatically when a
  Warren view is selected.
- Every `marmot warren register/mount/edit` already rewrites
  `.marmot/_warren.md`, so live owners pick those changes up without an
  explicit refresh.
- Cached remote **graphs** additionally expire after 60 seconds (lazily: the
  next query reloads), bounding staleness from out-of-band changes such as a
  `git pull` inside the checkout or another workspace's re-index. Tune or
  disable with `MARMOT_WARREN_TTL` (a Go duration; `0`/`off` disables).
  Remote **embedding stores** need no TTL: every search is a live read of the
  mounted project's SQLite database.
- Identified projects are exempt from all of this: reload never routes them
  (mounted redundantly or not), so they answer from the live in-memory vault
  with zero staleness.

While a mounted project's `embeddings.db` is open for cross-vault search, the
reader holds a shared advisory lock (`.marmot-data/vault.read.lock` next to
the DB). `marmot index --force` on that vault refuses to delete the database
while any such reader exists — close the reading process or retry later. On
Windows the advisory lock degrades to a no-op and `index --force` behaves as
before (documented platform gap).

## Embeddings and materialization

Each mounted project uses its own embedding database from that project's
`.marmot/.marmot-data/embeddings.db`. The local workspace does not merge all
Warren embeddings into one global database.

When a project is materialized, Marmot reads that project's graph from:

```text
.marmot/.marmot-data/warrens/<warren-id>/projects/<project-id>/.marmot/
```

If no materialized cache exists, Marmot reads the project directly from the
registered Warren checkout.

## Provenance

Warren API and UI responses include provenance for mounted nodes:

```json
{
  "source": "warren_mount",
  "warren_id": "product-platform",
  "project_id": "project-a",
  "vault_id": "project-a-vault",
  "qualified_id": "@project-a-vault/service/api",
  "editable": true
}
```

This lets users and agents distinguish local nodes from mounted Warren nodes and
see whether a selected node can be edited from the current workspace.

## Warrens vs bridges

Use a namespace bridge when two namespaces in the same vault need an explicit
relationship. Use a cross-vault bridge when two independent vaults own their own
bridge files.

Use a Warren when you want a curated set of many project graphs that can be
mounted on demand. Warren project bridges are managed in the Warren repo, not in
each project vault's `_bridges/` folder.
