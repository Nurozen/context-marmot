# docs

No code lives here; findings are documentation claims that pin current behavior (and must change with R1/R2) plus UX-surface evidence. 16 docs files; only warrens.md, architecture.md, bridges.md, current_limitations.md are relevant. The rest (benchmark, crud-classifier, data-structures, development, embedding-providers, implementation_plan, language_comparison, spec-*, typescript-sdk) do not touch warren identity or the warren UX surface.

## warrens.md

### docs/warrens.md:262-267 — R1 (MUST REWRITE: documents the exact behavior R1 removes)
The self-mount-with-warning behavior is documented as "the documented way to activate Warren bridges". R1's alias semantics obsolete this paragraph entirely; the C7 unconditional refusal changes the first sentence too ("from another Warren" scoping goes away).
```
262: Mounting refuses a project whose `vault_id` is already claimed by a project
263: mounted from another Warren (vault IDs are one flat routing namespace per
264: workspace; a duplicate would silently answer queries from the wrong
265: project). Mounting the Warren copy of the *local* project — same vault ID as
266: the workspace vault, the documented way to activate Warren bridges — is
267: allowed with a warning about the routing shadowing it causes.
```

### docs/warrens.md:224-228 — R1 (doctor/mount inconsistency documented as intended)
Doctor's `vault_id_collision_workspace` error is framed as catching *legacy* state ("Mount refuses new collisions") — but per the plan context, mount currently permits the local-vault collision that doctor flags as a hard error. This paragraph papers over the R1 inconsistency and needs updating alongside the fix (alias mounts must not trip this check post-R1).
```
224: `marmot warren doctor --workspace` runs in a consuming workspace instead of a
225: warren repo: it reports vault-ID collisions across the local vault and all
226: registered warrens' active projects (`vault_id_collision_workspace`, an
227: error). Mount refuses new collisions; this catches legacy state written by
228: older binaries.
```

### docs/warrens.md:291-302, 384-405 — R1 (editable gating docs; no self-mount exclusion documented)
"edit implies mount" (291) and editable/materialized mutual exclusion (400-405) are documented, but nothing says editable is refused on self-mounts — R1 adds that refusal; both sections need a sentence. Snippet at 291:
```
291: Enable writes for one project (edit implies mount — an unmounted project is
292: auto-mounted, and the command says so):
```

### docs/warrens.md:376-379 — R1 (bridge endpoint semantics that alias changes)
Bridge endpoints resolve project_id -> vault_id; "Both bridge endpoints must be active mounted projects" (line 378). Under R1 an aliased local endpoint is NOT a mounted project — this constraint statement must gain the alias case; under R2, an "identified" project satisfies it without any mount.
```
376: Both bridge endpoints must be active mounted projects. Dormant projects stay out
377: of the queryable graph even if a bridge references them, and relations not listed
378: in the Warren bridge are rejected on write.
```

### docs/warrens.md:53-67 — R1/R2 (vault_id identity contract; import preserves it)
```
58: warren_id: product-platform
59: vault_id: project-a-vault
...
65: `vault_id` is the ID used in qualified node references such as
```
Also 157-158: "Import rewrites the copied project `.marmot/_warren.md` to the target Warren/project identity" — vault_id preserved on import is the root cause of the R1 collision. R2's identify-verb docs slot naturally after the register section (~line 235).

### docs/warrens.md:104-194 — UX (author-side CLI surface; evidences flag soup + rename gap)
Verbatim: `--warren-dir` (106), `warren init --id` (112), `project import <id> <path> --vault-id --alias` (118-120), `project add <id> --vault-id --path --alias` (173-176, note `--path` here vs positional path on import), `--generate-id` on both (166, 184), `project rename project-a payments-api` (192). Rename doc confirms it's ID-only — nothing says the `projects/<project-id>/` directory is renamed, matching the known RenameProject gap. Consumer verbs use `--warren <id>` while author verbs use `--warren-dir <path>` — two different warren-selection idioms in one command family.

### docs/warrens.md:235-337 — UX (consumer verb surface; no quickstart)
register/list/mount(--all)/unmount/status/edit(--off)/burrow(--drop)/unregister(--force) all documented with rationale prose, but the doc is reference-shaped: there is no zero-to-first-bridge walkthrough anywhere in docs/ (confirms the onboarding gap). The one "how do I activate a bridge involving my own project" answer is the 265-267 self-mount paragraph — i.e., the current onboarding path IS the thing R1/R2 replace.

### docs/warrens.md:338-353 — R1/UX (propose)
Propose is documented as resolving "the sole editable project by default". Interaction with an R2 identified-local project (which is edited in place, never via editable mount) is undefined — doc will need an explicit statement.

### docs/warrens.md:457-462, 483-491 — UX (web UI warren capability, verbatim)
Confirms UI is read-only for warren management: graph selector entries plus an automatic refresh call only.
```
459: - `GET /api/warren/product-platform/graph` returns active mounted Warren nodes.
...
462: The web UI exposes active Warrens in the graph selector as `Warren <id>`.
...
485: - `POST /api/warren/{id}/refresh` reloads the serving engine's warren state
486:   directly. The web UI's refresh button calls this automatically when a
487:   Warren view is selected.
```

### docs/warrens.md:465-495 — R1 (freshness/reload; alias must be honored by reload path)
Documents that every register/mount/edit rewrite of `.marmot/_warren.md` is picked up by live daemon owners within ~1s via ReloadWarrenState. R1's skip-rt.Set-for-alias logic must hold on this hot-reload path too, not just startup; doc section unchanged in shape but the reload semantics description is where alias behavior should be mentioned.

## architecture.md

### docs/architecture.md:365-408 — R1/R2 (Warren Mounts section)
"Only active mounts are added to the engine" (380) and "active and available Warren project bridge endpoints are converted from project IDs to vault IDs and fed into the same cross-vault validation and vault registry path" (392-394) both change under R1 (alias endpoint = local .marmot path, no registry entry for the warren copy) and R2 (identified project participates with no mount at all). API scope list at 403-408 matches warrens.md.

## bridges.md

### docs/bridges.md:85-118 — R1 (warren project bridges)
```
116: Only active mounted Warren projects participate. A bridge to a dormant Warren
117: project resolves no nodes ...
118: accepted for writes until that project is mounted.
```
Same "must be mounted" claim; needs the alias/identity carve-out. Also 57: cross-vault bridges register in `~/.marmot/routes.yml` — the routing table R1's alias skips writing for the local vault_id.

## current_limitations.md

### docs/current_limitations.md — UX (stale relative to warren work)
Contains no warren section at all despite listing resolved/known limitations for older subsystems — the file predates the warren workstreams and neither documents warren limitations (self-mount shadowing, rename-ID-only, no UI management) nor got updated by workstreams A-D. Candidate for the UX pass.

## Out-of-date-context check
Nothing in the provided CONTEXT is contradicted by docs; docs/warrens.md:262-267 confirms the warn-not-refuse local self-mount exactly as the plan describes. One nuance: warrens.md:227 asserts "Mount refuses new collisions" for the cross-warren case, so C7 refusal already exists for non-local conflicts per docs — R1's "make refusal unconditional" is specifically about removing the local-vault exemption, matching the plan.
