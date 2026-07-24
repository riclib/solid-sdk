# The contracts — what a solution writes against

These are the versioned, status-tagged external contracts of the Solid
platform: the declarative surfaces a solution author (human or Claude)
writes against, and the same documents the toolkit validates against.

| Contract | Governs | Status |
|---|---|---|
| [`solution-stores.md`](./solution-stores.md) | how your solution's data is declared, stored, served, and governed | DRAFT v0.2 |
| [`store-backed-catalogs.md`](./store-backed-catalogs.md) | declared-only exposure — the platform serves exactly the surface you declare | SHIPPED 1.0.0 |
| [`workflow-defs.md`](./workflow-defs.md) | the workflow YAML your solution ships (when to act) | SHIPPED 1.0.1 |
| [`dashboard-dsl.md`](./dashboard-dsl.md) | dashboard queries + widgets, in YAML (what to watch) | DRAFT 0.14.1 |
| [`incremental-load-pipeline.md`](./incremental-load-pipeline.md) | fetch → decode → keep for your source data | DRAFT 0.2.0 |
| [`lake-artifact.md`](./lake-artifact.md) | declaring a lake (streams, projections, views, ingests, retention) from a solution | DRAFT 0.1.0 |

Each document carries its own version, stability policy, and owner ticket in
its header. SHIPPED means implemented and enforced by the platform; DRAFT
means the surface may still move (pre-1.0 minor versions can break).

**Path convention.** These contracts moved here from the platform repo's
`docs/sdk/` (S-1743, 2026-07-15; pointer stubs remain there). Bare
repo-relative paths inside them — `domains/…`, `infra/…`, `app/…`,
`docs/design/…`, `docs/ideas/…` — refer to the **platform repo**, where the
implementations live. Sibling contracts are linked relatively (`./…`).

**Editing rule.** This folder is the source of truth. When a platform ticket
changes a contract surface, the doc edit rides a solid-sdk PR alongside the
platform PR.
