# LakeArtifact — declaring a lake from a solution

**Contract version:** 0.2.0
**Status:** DRAFT (pre-1.0 minors can break)
**Owner ticket:** S-1874 (design: platform repo `docs/design/demos-over-the-lake.md`) · S-2212 (label-scoped binding)
**Wire type:** `contract.LakeArtifact` (leaf kind `lake`, key `<solution>.lake.<name>`)

A `LakeArtifact` is the one announce-wire artifact that declares a **data
plane** — you declare a LAKE ('tenant' is the platform's plumbing word for a
lake's slot in the estate): an append-only, signed lake tenant plus the projections, views and
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
contract.LakeArtifact{
    Name: "salesdemo",                    // lowercase ident; reserved names + _admin suffix refused
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
         // The dedup belt: {from} exposes the arrival-gen column as "__gen"
         // (quoted — the platform's physical bookkeeping column). Without it
         // a corrected event (same event_id, later gen) DOUBLE-COUNTS.
         DeriveSQL: `WITH dedup AS (
             SELECT * FROM {from}
             QUALIFY row_number() OVER (PARTITION BY event_id ORDER BY "__gen" DESC) = 1
         )
         SELECT deal_id, SUM(amount) AS total FROM dedup GROUP BY deal_id`},
    },
    Views: []contract.ViewDecl{
        {Name: "open_deals", SQL: "SELECT * FROM salesdemo.deals_latest WHERE status = 'open'"},
        {Name: "thresholds", Kind: contract.ViewKindSeed,
         SQL: "SELECT * FROM (VALUES (1, 100)) t(rule_id, threshold)"},
    },
    Ingests: []contract.IngestDecl{{
        Stream: "sales_events", SourceKind: "test_local", SourcePattern: "demo/*.ndjson",
    }},
    Retention: contract.RetentionDecl{Class: contract.RetentionWindow, Days: 90},
    Binding:   contract.LakeBindingSolution,
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
  reserved one: a stream feeding a projection bound on workspace IDENTITY must
  declare it and label it, and your writer (datagen, exporter) fills it with
  the owning workspace id per row. A stream whose rows are not workspace-owned
  labels a domain column instead (`team`, `region`) and its projections bind on
  that — see **Label-scoped binding** below.
- `Provenance` (optional) classifies the rows and decides whether a `workspace`
  column is legal on them at all — see **Provenance** below.

## Descriptions are metadata, not comments

Every `description` you ship — on a stream column, a catalog body's tables
and columns, a projection — is **read by two audiences you don't see**: the
agent grounds on it when writing SQL against your data, and end users read it
on the platform's catalog cards. It is product surface, not source
annotation.

The litmus test, sentence by sentence: *does this help someone USE the
data?*

**Ship it** — meaning; value domains (`status ∈ {covered, empty_slot,
no_slot} — the coverage verdict for this app × obligation × day`); units and
formulas (`cost_usd = (tokens)/1e6 * model_price`); join guidance (`join
obligations.key for label + article`); windowing/anchoring guidance (`First
event time (UTC). Window on it.`); caveats a querier needs (`always '' in
v1`).

**Keep it in your own source comments** — how the table is derived or built
("a DERIVE over X", "content-free by construction"); internal effort or gap
codenames; which dashboards/skills consume it; ticket references. Your
declaration file has comments; that is where the engineering story lives.

A good description is one or two sentences. If you find yourself explaining
the pipeline, you are writing a comment into the product.

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
substitutes the schema-qualified table; never write the table name yourself
(`Validate` refuses SQL without the token).

**Derive keys must be SOURCE columns.** The platform's touched-key scoping
reads a derive's `KeyColumns` from the DeriveFrom table (which keys did this
gen touch?), so a computed key — `date_trunc('week', …)` — cannot exist:
carry the dimension on the wire (a `week` column your writer stamps) and key
on that. `Validate` enforces it for transform-less copies.

**The arrival-gen column is `"__gen"`.** The lake's landing column is the
reserved `gen`, but the SERVED projection tables `{from}` reads expose the
arrival gen as the platform's bookkeeping column `__gen` (quote it — the
name starts with an underscore). Every derive over a stream that can receive
corrections needs the dedup belt shown in the example above: keep the
latest-gen version of each event identity, or corrections double-count.

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

There is **no special ingest API** — and in v1 the FILE door is a lake's
ONLY write door, so **at least one `Ingests` entry is required** (the
platform's lake refuses a tenant with no sources). Your writer emits envelope files (NDJSON)
into a source the platform walks — the same production pipeline the in-tree
systems use, pointed at your files. Each `Ingests` entry materializes a
generic FILE-door runnable plus a job seeded **DISABLED** (operator enables)
— one door per file-fed stream, all typically walking the same configured
source with per-stream `SourcePattern`s:

- walk the declared source (`SourceKind` + `SourcePattern`),
- skip slices younger than the seal margin (`SealMarginMinutes`: 0 = the
  platform default of 15; >= 1 = that literal margin; **-1 disables the gate**
  — files land immediately, for dev/test_local sources whose files never
  grow; a literal 0-minute margin is not expressible, use 1),
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

## Binding — the roster

`Binding: contract.LakeBindingSolution` is v1's only value: the bind roster
is the set of workspaces an operator has bound to **your solution**. No
workspace binds your solution → nothing is bound, and the catalog seed waits
too (discovery never exceeds the served surface).

The roster says **which workspaces**. The next section says **which rows each
one sees**.

## Label-scoped binding

By default a projection binds on **workspace identity**: the stream carries a
`workspace` column, and the platform adds `workspace = '<ws>'` per bind. That
works when your rows already know which workspace they belong to. Often they
do not — an incident knows its `team`, and which workspace owns that team is an
org question, not a data one.

Label-scoped binding is for exactly that case:

```go
Streams: []contract.StreamDecl{{
    Name:       "incidents",
    Provenance: contract.ProvenanceSource,   // never workspace-stamped
    Columns: []contract.ColumnDecl{
        {Name: "opened_at",   Type: "TIMESTAMP", Role: contract.RoleTime},
        {Name: "team",        Type: "VARCHAR"},
        {Name: "incident_id", Type: "VARCHAR"},
        {Name: "src_slice",   Type: "VARCHAR"},
    },
    Labels: []string{"team"},                // the stream EMITS the label
}},
ScopeLabels: []contract.ScopeLabelDecl{{
    Name:        "team",
    Description: "ServiceNow assignment group; one team is owned by exactly one workspace.",
    Exclusive:   true,                       // the exclusivity law, below
}},
Projections: []contract.ProjectionDecl{{
    Name: "incidents_latest", Stream: "incidents", Kind: contract.ProjectionLatest,
    KeyColumns: []string{"incident_id"}, TimeColumn: "opened_at",
    ScopeLabel: "team",                      // bind on the label, not on identity
}},
```

The values a workspace claims (`team: [payments, billing]`) are **platform
config**, not part of your announce. They are resolved **at bind time**, and
three properties follow from that lateness:

- **A reorg is a config change, not a migration.** Move a team to another
  workspace by editing the claim; the next bind resolves the new owner.
- **History follows.** Because the rows carry the label and never an owner, the
  moved team's whole history moves with it — the new owner inherits the
  operational record and the precedent corpus, without rewriting a landed byte.
- **The record stays immutable.** Land-time stamping was rejected for exactly
  this: it bakes today's org chart into an append-only record and puts a config
  concern in the landing layer.

Rules `Validate` enforces (the platform re-validates independently):

- A declared scope label must be a **column of some stream and listed in that
  stream's `Labels`** — a label no stream emits binds to nothing.
- A projection's `ScopeLabel` must be declared in `ScopeLabels` **and** emitted
  by its own stream. The bind fails closed, so this refuses at publish instead.
- `workspace` is never declared in `ScopeLabels`: it is the reserved identity
  label, and a projection binds on it by leaving `ScopeLabel` empty.
- `ScopeLabel` with `Unscoped` refuses — an unscoped projection binds once,
  label-less, into the admin engine.

### The exclusivity law

`Exclusive: true` says a value of this label is owned by **exactly one
workspace per lake**.

Set it whenever visibility implies ownership — which is what a **generative**
workflow makes true. When a landed row does not merely become *visible* but
OPENS something (a case, an investigation, a notification) in every workspace
that can see it, two workspaces claiming `team: payments` do not share a view:
they each open their own case for the same incident and duplicate the work.
This is the doctrine family "one writing solution per store", applied to
labels.

The SDK cannot check ownership — the claims live in platform config, not in
your artifact. What it does is make the requirement explicit on the wire and
ship the checker **both sides run**, so a platform refusal and a partner-side
preflight report the identical conflict:

```go
err := contract.ValidateExclusiveClaims("cdl", "team", []contract.LabelClaim{
    {Workspace: "ws-platforms", Value: "plataformas"},
    {Workspace: "ws-apps",      Value: "plataformas"}, // ← refused, loudly
})
```

The platform runs it at **bind/approval** and refuses there; it names every
conflicting value, not just the first, because the operator fixing a reorg
wants the whole list.

### The unclaimed edge

A row whose label value **no workspace claims** binds nowhere and generates
nothing. That is a legal state, not an error — but it must never be a silent
gap: the platform surfaces it as an **unowned count on the admin screen**, read
from the lake's own tenant surface (which sees every landed row regardless of
claims). Nothing is declared here for it; it is the platform's obligation, and
it is the reason "nobody claims it" is a visible number rather than an absence.

## Provenance — where `workspace` may appear

`Provenance` on a stream is optional (omit it and validation is exactly as
before), and it decides one thing: whether the rows may carry a `workspace`
column.

| Provenance | Rows | `workspace` column |
|---|---|---|
| `source` | ingested from a system of record (incidents, tickets, telemetry) | **refused** |
| `product` | derived by your solution (advisories, scores, classifications) | **refused** |
| `exhaust` | the record of execution (cases opened, invocations, deliveries) | **required** |

The distinction is about what the column would MEAN. On source and product data
a workspace stamp is a config decision frozen into an immutable record — that is
what makes a reorg a migration. On execution exhaust the workspace is a **fact
about what happened**: this ran under that workspace's config, routes and engine.
It is stamped at open and stays truthfully stamped forever, including after a
reorg moves the team elsewhere — the moved team's source and product history
follows the label, and the old case exhaust does not, because it records where it
executed. (The in-tree conversations tenant stamps for the same reason.)

A lake commonly declares both: `incidents` as `source`, bound on `team`; `cases`
as `exhaust`, bound on workspace identity.

## Reserved names

`Validate` refuses lake names that collide with the estate's in-tree tenants
— `conversations`, `metrics`, `audit`, `solidmon`, `adf_ops`, `cdhkpi` —
plus DuckDB's own schemas (`main`, `temp`, `system`, `information_schema`,
`pg_catalog`), anything prefixed `solid`, and any `_admin` suffix (lake
foo's unscoped surfaces serve as the `foo_admin` schema). The sets are
EXPORTED (`contract.ReservedLakeNames`, `contract.ReservedColumns`) as the
single source of truth the platform re-validates against; additive-only.

## Changelog

| Version | Date | Change |
|---|---|---|
| 0.1.0 | 2026-07-08 | Initial contract (S-1874): streams, projections, views, ingests, retention, solution binding. |
| 0.2.0 | 2026-08-14 | MINOR, additive (S-2212): **label-scoped binding** (`ScopeLabels` + `ProjectionDecl.ScopeLabel`, bind-time resolution), the **exclusivity law** (`ScopeLabelDecl.Exclusive` + the shared `ValidateExclusiveClaims` checker) and the unclaimed edge, and **`StreamDecl.Provenance`** (source/product never workspace-stamped; exhaust always is). Every field is optional: an artifact that declares none of them validates and binds exactly as it did at 0.1.0. |
