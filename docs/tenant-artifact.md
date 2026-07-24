# TenantArtifact — declaring a lake tenant from a solution

**Contract version:** 0.1.0
**Status:** DRAFT (pre-1.0 minors can break)
**Owner ticket:** S-1874 (design: platform repo `docs/design/demos-over-the-lake.md`)
**Wire type:** `contract.TenantArtifact` (leaf kind `tenant`, key `<solution>.tenant.<name>`)

A `TenantArtifact` is the one announce-wire artifact that declares a **data
plane**: an append-only, signed lake tenant plus the projections, views and
ingest that make it usable. On operator approval the platform materializes it
into exactly what the in-tree boot modules build imperatively — no in-tree
privilege required. Your demo/solution's data then lives on an immutable,
verifiable record (`solid lake verify` covers it) instead of any local store.

Unlike every other data artifact (catalog/projection/job, which carry opaque
`Body` YAML), the tenant declaration is **typed**. The platform enforces these
fields at materialization — reserved names, SQL guardrails, explicit
retention — and a typed wire is what gets you those errors at publish time:
`PublishSolution` calls `Validate()` and refuses a bad declaration before it
ever reaches the bus. The platform re-validates independently; the publish
check is a courtesy, not the gate.

## Lifecycle

1. **Announce** — the artifact rides your solution's KV tree like any other
   leaf. Nothing materializes yet.
2. **Approve** — approval gates everything (S-1503): tenant provision,
   binds, views, ingest job, catalog seed all wait for the operator.
3. **Materialize** — the platform provisions the lake tenant (its own root
   under the estate layout, signed with the shared estate key), binds the
   projections into each workspace in the roster, applies views/seeds through
   the engine funnel, registers the generic FILE-door ingest runnable and
   seeds its job DISABLED (the operator enables it).
4. **Retention** — enforced lake-side per your declaration; drops are
   recorded in the gen ledger (an auditable absence, not silent deletion).
5. **Uninstall** — the tenant directory and engine schemas are part of the
   install footprint and are torn down with the solution.

## The declaration

```go
contract.TenantArtifact{
    Name: "salesdemo",                    // lowercase ident; reserved names refused
    Streams: []contract.StreamDecl{{
        Name: "sales_events",
        Columns: []contract.ColumnDecl{   // ordered — this IS the landing order
            {Name: "event_time", Type: "TIMESTAMP", Role: contract.RoleTime},
            {Name: "workspace",  Type: "VARCHAR"},
            {Name: "deal_id",    Type: "VARCHAR"},
            {Name: "amount",     Type: "DECIMAL(18,2)"},
            {Name: "src_slice",  Type: "VARCHAR"},
        },
        Labels:   []string{"workspace"},  // the reserved scoping label
        Residual: true,
    }},
    Projections: []contract.ProjectionDecl{
        {Name: "deals_latest", Stream: "sales_events", Kind: contract.ProjectionLatest,
         KeyColumns: []string{"deal_id"}, TimeColumn: "event_time"},
        {Name: "events_copy", Stream: "sales_events", Kind: contract.ProjectionCopy},
        {Name: "deal_totals", Stream: "sales_events", Kind: contract.ProjectionDerive,
         DeriveFrom: "events_copy", KeyColumns: []string{"deal_id"},
         DeriveSQL: "SELECT deal_id, SUM(amount) AS total FROM {from} GROUP BY deal_id"},
    },
    Views: []contract.ViewDecl{
        {Name: "open_deals", SQL: "SELECT * FROM salesdemo.deals_latest WHERE status = 'open'"},
        {Name: "thresholds", Kind: contract.ViewKindSeed,
         SQL: "SELECT * FROM (VALUES (1, 100)) t(rule_id, threshold)"},
    },
    Ingest: &contract.IngestDecl{
        Stream: "sales_events", SourceKind: "test_local", SourcePattern: "demo/*.ndjson",
    },
    Retention: contract.RetentionDecl{Class: contract.RetentionWindow, Days: 90},
    Binding:   contract.TenantBindingSolution,
}
```

## Streams

A stream is the wire shape of one landed record: name + ordered typed
columns. The physical landing order is fixed by the lake — the reserved `gen`
arrival column first, then your columns **in declared order**, then `payload`
(the raw record) and `residual` when enabled. Because order is physical, the
declared order is frozen the moment data lands: append new columns, never
reorder (the additive-only rule, same as every wire).

Rules (all enforced by `Validate`):

- Column types use the platform's anti-injection grammar (letters, digits,
  `_`, space, `(`, `)`, `,`) — a grammar, not a closed enum, so
  `DECIMAL(18,2)` passes and injection vectors do not.
- Exactly one column per stream carries `Role: contract.RoleTime` — the
  event-time column that drives slices, drains, and retention.
- `gen`, `payload`, `residual` are reserved and cannot be declared.
- `Labels` name declared columns usable as scoping labels. `workspace` is the
  reserved scoping label: any stream feeding a workspace-scoped projection
  must declare it and label it. Your writer (datagen, exporter) fills it with
  the owning workspace id per row.

## Projections

Three kinds, mirroring the platform's workspace-store engine:

| Kind | Answers | Required fields |
|---|---|---|
| `copy` | the full event history | — (`TransformSQL` optional) |
| `latest` | current state per key (state changes) | `KeyColumns`, `TimeColumn` |
| `derive` | aggregates that must never drift (recompute-and-replace) | `DeriveSQL` (reads `{from}`), `DeriveFrom` (a `copy` in this artifact), `KeyColumns` |

SQL guardrails (mirrored from the platform's projector; the platform
re-validates and your statements run under the statement log and engine
caps): single statement only; `TransformSQL` is a bare `SELECT` — no `WITH`,
no `SELECT DISTINCT/ALL`; `DeriveSQL` may be `SELECT` or `WITH…SELECT`.
Both read their source through the **`{from}` token** — the platform
substitutes the schema-qualified table; never write the table name yourself.

**Ordering grain.** `latest` picks the observation with the highest **gen**,
and the FILE-door lands one gen per file — so the landed file is the ordering
grain. Two updates of one key inside a single file tie (unspecified winner);
an update that must win rides a later file. Hour-sliced datagen output gets
this right by construction.

**Schema naming.** Projections serve under the DuckDB schema named after the
tenant (`<tenant>.<table>`); unscoped surfaces under `<tenant>_admin`. This
is also why tenant names must avoid DuckDB's own schemas (`main`, `temp`,
`system`, …) — Validate refuses them.

**Scoped vs unscoped.** By default a projection binds once per workspace in
the roster, and the platform adds the `workspace` label predicate — each
workspace engine sees only its own rows, by construction. `Unscoped: true`
binds the projection once, label-less, into the **admin engine** under the
tenant's admin schema: cross-workspace data lives only there (the access
boundary). Use it for the billing/ops-style surfaces an operator reads.

## Views and seeds

`ViewDecl` SQL is always a bare `SELECT` (or `WITH…SELECT`) — the platform
supplies the `CREATE` wrapper, so a declaration can never smuggle DDL/DML:

- `ViewKindView` (default) → `CREATE OR REPLACE VIEW <name> AS <sql>` at
  every bind (idempotent, follows re-binds).
- `ViewKindSeed` → `CREATE TABLE IF NOT EXISTS <name> AS <sql>` — create-only
  reference data, never overwritten by a re-bind.

`Unscoped` on a view applies it in the admin engine instead of each
workspace engine. View SQL runs at query time in the reader's session, so it
must **schema-qualify** the projection tables it reads (`FROM
<tenant>.<table>`, or `<tenant>_admin.<table>` for unscoped surfaces).

## Ingest — the FILE door

There is **no special ingest API**. Your writer emits envelope files (NDJSON)
into a source the platform walks — the same production pipeline the in-tree
systems use, pointed at your files. Declaring `Ingest` materializes the
generic FILE-door runnable plus a job seeded **DISABLED** (operator enables):

- walk the declared source (`SourceKind` + `SourcePattern`),
- skip slices younger than the seal margin (`SealMarginMinutes`, default 15),
- dedup already-landed files by byte hash,
- land **one gen per file**, stamping `SliceColumn` (default `src_slice`,
  which the stream must declare) as the drain-surviving cursor.

Corrections are just re-landed files: a new file for an old slice lands under
a new gen, and downstream `latest`/`derive` projections converge — no dupes,
no mutation. That property (plus `Rebuild ≡ steady state` for derives) is the
whole point of running a demo on the lake.

`Envelope`/`EnvelopeRef` (mutually exclusive, both optional) carry an
envelope schema for promote-time decode; when neither is set, envelope fields
promote to declared columns by name.

## Retention

`Retention` is REQUIRED and always explicit — there is no default, and
"forever" must be declared, never assumed:

- `{Class: "window", Days: N}` — sealed files whose slice is entirely older
  than N days are dropped **as an auditable event**: the drop is recorded in
  the gen ledger, so the record shows a signed absence, not a silent gap.
- `{Class: "forever"}` — the full mirror (the audit-style ruling, per-system).

Demos declare 90 days.

## Binding

`Binding: contract.TenantBindingSolution` is v1's only value: the bind roster
is the set of workspaces an operator has bound to **your solution**. No
workspace binds your solution → nothing is bound, and the catalog seed waits
too (discovery never exceeds the served surface).

## Reserved names

`Validate` refuses tenant names that collide with the estate's in-tree
tenants — `conversations`, `metrics`, `audit`, `solidmon`, `adf_ops`,
`cdhkpi` — plus DuckDB's own schemas (`main`, `temp`, `system`,
`information_schema`, `pg_catalog`) and anything prefixed `solid`. The
reserved list is additive-only.
