<!--
  ┌──────────────────────────────────────────────────────────────────────┐
  │  Dashboard DSL — Query & Widget Contract                               │
  ├──────────────────────────────────────────────────────────────────────┤
  │  Contract version : 0.16.0                                             │
  │  Status           : DRAFT — contract not yet frozen (pre-1.0)          │
  │  Stability         : unstable; minor versions may break (see §2)       │
  │  Surface           : external — authored by humans, the editor, and    │
  │                      the LLM; third parties write against this.        │
  │  Last updated      : 2026-07-19                                        │
  │  Owner ticket      : S-1140 (design) / S-1524 (this correctness pass)  │
  │  Implements        : shipped — frame, macros, dialect (see §1.1)       │
  │  Supersedes        : the hand-rolled window SQL in                     │
  │                      domains/dq/dashboard_data.go                      │
  └──────────────────────────────────────────────────────────────────────┘
-->

# Dashboard DSL — Query & Widget Contract

**Contract version 0.16.0 · Draft · `dsl_version: "0.16"`**

This is the **first external-contract document** (born in the platform repo’s `docs/sdk/`, now homed here). The DSL is a
surface third parties author against — so it carries a version number and a
stability policy (§2), and changes to it are governed, not incidental.

> **Scope split.** This document specifies the **query + widget substrate**:
> the document shape, the data-source/dialect model, the time-frame, the query
> templating macros, and template variables. It is the *contract*.
>
> The **product** — the editor, drag-resize, glass authoring, conversation
> widgets, fork-on-edit — is specified in
> [`docs/design/dashboard-dsl-and-editor.md`](../design/dashboard-dsl-and-editor.md).
> That doc consumes this one.

> **Delivery (announce wire).** This document specifies the YAML a dashboard
> *is*. As of `solid-sdk` v0.3.0 that same YAML also travels as the `Body` of a
> `DashboardArtifact` leaf when a solution **announces** over NATS KV (the
> dependency-inversion path; `app/solutionbus` consumes it). The announce /
> manifest contract itself (the `SolutionManifest`, the artifact kinds, the KV
> tree) is canonical in `solid-sdk/contract` — its author-facing home is
> this `docs/` folder (moved from the platform repo, S-1743). In-tree
> registration stays a live path; the schema in this doc is identical on both.

---

## 1. Overview

A dashboard is a standalone YAML document. It declares a grid of widgets; each
widget declares a `source` whose `query` runs against a data engine and projects
into a typed widget. The contract has four moving parts:

```
  ┌─────────────┐   resolve    ┌──────────┐   render    ┌──────────────┐   execute   ┌─────────────┐
  │ Session     │ ───────────▶ │  Frame   │ ──────────▶ │   Template   │ ──────────▶ │   Store     │
  │ TimeRange   │  + frame:    │ from,to, │  FuncMap    │  engine      │  Query(     │ (or catalog │
  │ + variables │   mode       │ interval │ (dialect)   │  {{ macros }}│  rendered,  │  / comply)  │
  └─────────────┘              └──────────┘             └──────────────┘  opts)       └─────────────┘
        L0                          L1                        L2                            L3
```

- **L0 — Inputs.** The session time-range (from the picker/calendar) and the
  resolved values of any declared template variables.
- **L1 — Frame.** The widget's `frame:` mode projects the session range into a
  concrete `(from, to, interval)`. Pure, dialect-agnostic.
- **L2 — Templating.** The `query` string is a Go `text/template`. A dialect-
  specific FuncMap expands a fixed macro vocabulary (§5, §6) into SQL fragments
  or metric-query tokens.
- **L3 — Execution.** The rendered query runs through the resolved engine. For
  string-frame dialects (SQL) the frame is already in the text; for out-of-band
  dialects (metrics) the frame rides the executor's options.

Every source returns the same result contract: `QueryResult{ Columns, Rows }`.
Projection into the typed widget (metric-card, line-chart, …) is uniform and
out of scope for this contract.

### 1.1 Conformance / implementation status

The query substrate (L1 Frame + L2 templating) is **shipped**. The renderer
(`app/widgets/dashboard.go` + `dsl_template.go`, `dsl_dialect.go`, `dsl_frame.go`)
resolves the Frame, expands the macro FuncMap per dialect, lints the template,
and executes — picker changes **do** re-window YAML-backed queries. Implemented:
all §6.2 macros (`from`/`to`/`timeFilter`/`anchor`/`interval`/`timeGroup`,
`var`/`filter`/`values`, `search`), the §5 frame modes, the control-flow lint
(§6.1), load-time `Validate` (§9, `infra/dashboard/validate.go`), the catalog
binding modes incl. `catalog: all` (§4.5), named heatmap axes (§8.3), and
drill-down (§8.2).

Still **(planned)** and called out inline where they appear: `dsl_version`
runtime enforcement (§2.2) and chained-variable cycle detection (§7.4). A few
checks are deliberately deferred rather than planned (drill-down *target
resolution* is workspace-scoped at render time, S-1499) — noted in §9.

> **Doc-vs-code note.** This is a Draft contract (pre-1.0, §2.1): the schema is
> stable but section numbers and macro details may still move. The `Dialect`
> extension shape in §10 describes the *intended* registration recipe; the live
> interface is narrower (no `ApplyFrame` — see §10).

---

## 2. Versioning & stability

The contract uses **`MAJOR.MINOR.PATCH`**.

| Bump | Meaning | Examples |
|---|---|---|
| **MAJOR** | Breaking. An existing document may stop working. | Remove/rename a macro; change a macro's expansion semantics; remove or repurpose a field; tighten validation that previously passed. |
| **MINOR** | Additive. Old documents keep working. | New macro; new widget `kind`; new optional field; new dialect; new `frame:` mode. |
| **PATCH** | No schema change. | Doc clarifications, examples, bug-fixes in expansion that restore intended behaviour. |

### 2.1 Pre-1.0 caveat

While **MAJOR is 0**, the contract is **unstable**: minor versions MAY break.
`1.0.0` is declared when **M1.6 ships and the macro vocabulary freezes**. Do not
treat 0.x as a stable third-party target.

### 2.2 Declaring & checking the version

A document SHOULD declare the contract version it targets:

```yaml
dsl_version: "0.1"   # MAJOR.MINOR — PATCH is never declared
```

Runtime compatibility rule **(planned)**:

- **MAJOR** must equal the runtime's major, else the document is rejected at load.
- Runtime **MINOR** must be `>=` the declared minor (the runtime understands
  every feature the document might use). A newer document on an older runtime is
  rejected with a clear "requires dsl ≥ X.Y" error.
- A missing `dsl_version` is treated as the runtime's current version (with a
  load-time warning) — convenient for hand-authoring, explicit for published
  contracts.

### 2.3 Changelog

| Version | Date | Change |
|---|---|---|
| 0.1.0 | 2026-05-29 | Initial draft. Document shape, dialect model, frame modes, macro vocabulary, typed variables. |
| 0.2.0 | 2026-05-29 | Additive: `smooth` / `area` presentation fields on `line-chart` widgets (S-1145). |
| 0.3.0 | 2026-05-30 | Template variables reworked into `dimensions:` + `filters:` blocks (one struct, two lifecycles); per-variable `icon`; `value_type: bool` toggle. Replaces the single `variables:` block (was `(planned)`, unimplemented) (S-1146). |
| 0.4.0 | 2026-05-30 | Additive: per-variable `options_refresh: on_load \| on_window` for query-derived option lists (default `on_load` — unbounded frame + page-load refresh, can't self-empty). Replaces the unconditional frame-binding of option queries (§7.4) (S-1152). |
| 0.5.0 | 2026-05-31 | Additive: `orientation: horizontal \| vertical` presentation field on `bar-chart` widgets (default `horizontal`) (S-1185). |
| 0.5.1 | 2026-05-31 | Behaviour (no schema change): a time-axis `line-chart` now spans the chosen frame window instead of clamping to the data extent — empty/sparse periods stay visible. Restores intended windowing (S-1187). |
| 0.6.0 | 2026-06-05 | Additive: new `multistat` widget `kind` — one query → a responsive auto-fit grid of mini-stats (the N-tile generalisation of `metric-card`). New optional fields `series` / `value` / `sort` / `limit`; reuses `format` / `label` / `status_text` (S-1265). |
| 0.7.0 | 2026-06-05 | Additive: new `text` widget `kind` — renders a single string from a 1-row query (plain, HTML-escaped; no markdown at v1). No new fields — uses `source.query` + `title` (S-1268). |
| 0.8.0 | 2026-06-05 | Additive: `polished-table` search + pagination. New optional fields `searchable` / `search_columns` / `page_size` (scoped to `kind: polished-table`) and a new `{{ search }}` macro that expands to a case-insensitive `ILIKE` substring predicate over the search columns (empty term / no columns → `TRUE`). Defaults preserve the static path (`searchable: false` + `page_size: 0` → unchanged) (S-1269). |
| 0.9.0 | 2026-06-06 | Additive: catalog binding modes (§4.5) + the catalog axis (§7.7). `source.catalog` now accepts a `{{ var }}` macro (expanded through the same template pipeline as `source.query`, before the query) and the sentinel `all` (the multi-catalog union session exposing `all_tables` / `all_columns` / `all_relationships` / `all_importstats`, tagged with a `catalog` column; `all` is a reserved catalog id). New variable fields `from: workspace_catalogs` (a built-in option source, mutually exclusive with `options`/`query`) and `prepend` (literal values ahead of a `from`-derived list, e.g. `[all]`). A `from: workspace_catalogs` dimension must be single-select (no `multi`/`include_all`). A literal-id / macro-less `catalog:` is byte-for-byte unchanged (S-1266 / S-1267). |
| 0.10.0 | 2026-06-06 | Additive: optional widget field `flat: true` — renders a widget without the raised panel surface (no background/border/shadow), so it reads as flat page content next to raised tiles. Default `false` (raised) preserves every existing widget. v1 is honoured by the `text` widget; other kinds ignore it (S-1274). |
| 0.11.0 | 2026-06-13 | Additive: optional widget-level field `drilldown` (a `LinkSpec`: `target` page id + `params` mapping each target **variable** to a mark-coordinate placeholder — `{row}`/`{col}` on a heatmap, `{name}` on a donut, `{category}`/`{series}` on a bar-chart, `{<column>}` on a polished-table). Lifts the previously-dormant `LinkSpec` (until now only on `ColumnSpec.link`) to a widget block so a mark click navigates to a filtered dashboard. A widget with no `drilldown` is byte-for-byte unchanged. Rendering + nav are specified in `docs/design/dashboard-drilldown.md`. (drill-down) |
| 0.12.0 | 2026-06-13 | **BREAKING (heatmap only):** `kind: heatmap` now declares its axes by **name** — `col` / `row` (bare column name ≡ `{field, type: category}`, or an object with `type: time` + `unit` + `format`) and `value` (the cell-color column) — replacing the implicit positional contract (`cols[0]` parsed as a date and formatted `MM-DD`, `cols[1]` = row label, `cols[2]` = status, with a silent categorical fallback). A `category` axis renders verbatim in query order; a `time` axis buckets by `unit` and sorts chronologically. Existing heatmap documents MUST migrate (declare `col`/`row`/`value`; date heatmaps add `type: time`). Motivation + migration: `docs/design/dashboard-drilldown.md` §3. (heatmap-axes) |
| 0.12.1 | 2026-06-13 | PATCH (doc-only, no schema change): §8.2 (drilldown) + §8.3 (heatmap axes) point at the worked example in `gitstore/solution/internaldemo/dashboards/` — `internaldemo.overview.yaml` (entities × controls heatmap + `drilldown:`) drills into `internaldemo.detail.yaml`. (drill-down-example) |
| 0.13.0 | 2026-07-04 | Additive: a variable's option `query:` MAY return an optional **count column** — named `count`/`n`/`cnt`, or simply the second column — read as the occurrence count per value and surfaced in the filter-picker UI. Counts are presentational only; selection semantics and the variable macros never read them, and a missing/non-numeric count column degrades to values-only. No YAML field changes (§7.4). UI (§7.6): the `+ Filter` cascading value submenu is replaced by the filter-picker panel — per-filter searchable value tables with counts, multi-select checkboxes; filter option queries resolve lazily on panel open instead of per header render (S-1606). (option-counts) |
| 0.14.0 | 2026-07-05 | Additive: new `info-card` widget `kind` — one query → an adaptive grid of status-colored cards, each pairing a status pill with a heading and an optional prose body (the prose sibling of `multistat`: status + heading + body where multistat is label + number). New optional fields `status` / `heading` / `body` (columns declared by name) + `per_row` (grid column ceiling, default 4, 1..12); reuses `sort` / `limit` (overflow → "+M more"). `status` carries the semantic `StatusLevel` vocabulary (normal / warning / error / info); an unrecognised value degrades to the neutral/unknown pill, never an error. A single-row query renders one standalone card via the same path (S-1620). |
| 0.14.1 | 2026-07-15 | PATCH (doc-only, no schema change): §7.4 large-table hazard warning (S-1730). `on_load` renders `{{ timeFilter }}` as `TRUE`, so a query-derived option list scans the WHOLE table on every full-page header render, independent of the picker period — an unbounded scan on a fat fact table (on the demo estate an `on_load` `DISTINCT` over a 72M-row `metrics` table, stacked with an uncapped engine, seized the box). Guidance: on large tables declare `options_refresh: on_window` or point the option query at a materialised dimension/labels table; reserve `on_load` for small, slowly-changing reference sets. No schema change — `on_load` remains the default and its unbounded semantics are unchanged. (large-table-hazard) |
| 0.15.0 | 2026-07-19 | Additive (heatmap time axes, v4 #882/#883/#884): new units `hour` and `auto` (`auto` derives bucket width from the resolved window targeting ~40 columns — 24h→1h, 7d→4h, year→weeks; frameless falls back to `day`). A time COLUMN axis is now **frame-driven**: buckets pre-seeded across the resolved window, dataless buckets render as empty cells instead of missing columns (time ROW axes unchanged). Duplicate cells now keep the **worst** status (severity: fail > warn > unknown > info > ok), replacing previously-unspecified last-write-wins — queries may emit one graded row per source point and let the widget fold. Dense axes (>16 cols) thin labels to every ~12th; cell tooltips keep the exact bucket. (heatmap-window-buckets) |
| 0.16.0 | 2026-07-19 | Additive (nav groups, S-1771): new optional dashboard-level fields `group` (string) + `nav_order` (int). Dashboards sharing a `group` collapse into ONE workspace-nav item rendered as a dropdown of members; `nav_order` positions a dashboard in the nav and within its group (lower = first; unset keeps announce-order). Presentation only: `?page=<id>` deep links, drill-downs, and widget identity are untouched — the dropdown is chrome, not routing. Group display text is owned by the document; the platform never infers hierarchy from page-id segments. (nav-groups) |
| 0.12.2 | 2026-06-28 | PATCH (doc-only, no schema change): correctness pass (S-1524). The substrate is shipped, not "target" — §1.1 + header rewritten; §6.1 lint and §9 load-time validation no longer marked (planned); §10 `Dialect` interface corrected (no `ApplyFrame`; registry is package-local in `widgets`); dead `solutions/internaldemo/*` worked-example paths repointed to `gitstore/solution/internaldemo/` (the moved, renamed files; no `CLAUDE.md`). `dsl_version` enforcement (§2.2) + chained-var cycle detection (§7.4) remain genuinely planned. Added an announce-wire delivery pointer. (correctness-pass) |

---

## 3. Document shape

```yaml
dsl_version: "0.1"
id: dq.home                     # stable registration ID (required)
title: "ISOP Carta — Data Quality"

group: "KPI Detail"             # optional — nav dropdown this dashboard files under (0.16.0)
nav_order: 2                    # optional — position in the nav / within the group (lower = first)

header:                         # optional PageHeader chrome (see editor doc §4.7)
  title: "Data Quality"
  subhead: "Snapshot"
  deck: "…"
  tag: "DQ · LIVE"
  live_pill: true
  time_range_picker: true       # shows the picker; widgets refresh on time_window

default_source:                 # merged into every widget's source (per-widget overrides win)
  kind: data
  catalog: isop-carta           # OR: store: <store-id>

dimensions:                     # optional — mandatory scope controls; see §7
  - name: env
    # …
filters:                        # optional — add-on-demand refinements; see §7
  - name: ci
    # …

rows:
  - variant: chart              # optional row styling hint
    widgets:
      - id: tables-profiled     # stable widget identity (required, unique in doc)
        kind: metric-card       # widget family (see §8)
        span: 3                 # 1..12 grid columns
        title: "Tables monitored"
        label: "TABLES · MONITORED"
        format: compact
        status_text: "at latest snapshot"
        frame: as_of            # §5 — default as_of
        source:
          query: |
            SELECT COUNT(DISTINCT (table_schema, table_name)) AS cnt
            FROM p.profiles
            WHERE dt = {{ anchor "p.profiles" "dt" }}
        refresh:
          on: [time_window, variable]
```

**Required fields:** `id` (document), and per widget `id`, `kind`, `source.query`
(after `default_source` merge). Everything else is optional with documented
defaults.

`id` is the stable handle for refresh, glass, and (future) anchored
conversations. It is part of the contract: renaming a widget `id` is a breaking
change to anything that targets it.

---

## 4. Data sources & dialects

### 4.1 The `source` block

```yaml
source:
  kind: data            # v0.1 only `data`; `conversation` reserved (editor doc M3)
  catalog: <id>         # exactly one of catalog | store, after default_source merge
  store: <id>
  query: |              # the templated query string (§6)
    …
```

`source` is merged with `default_source`; per-widget keys win. After the merge a
source MUST resolve to exactly one engine (`catalog` xor `store`) and a non-empty
`query`.

### 4.2 Engines & the dialect families

The query language and frame-application strategy are determined by the resolved
engine — **never declared in YAML**, and the renderer **never switches on type**.

| Engine | Resolves to | Dialect family |
|---|---|---|
| `catalog: <id>` | catalog meta session (DuckDB) | **SQL** |
| `store: <id>` where `Store.Type()` ∈ {postgres, duckdb, sqlite, databricks, comply} | `domains/store.Store` | **SQL** (flavor per type) |
| `store: <id>` where `Store.Type()` ∈ {prometheus, metricstore} | `domains/store.Store` | **metrics** |

Two families, one difference that matters to this contract:

- **SQL family** — the frame is injected **into the query string** via macros
  (`{{ timeFilter }}`, `{{ anchor }}`). The executor receives only `limit`.
- **Metrics family** — the frame is applied **out-of-band**: `from`/`to`/`interval`
  ride the executor options (`start`/`end`/`step`/`type` — already part of
  `Store.Query`'s `opts` contract). String-frame macros (`timeFilter`, `anchor`)
  are **no-ops**; only `interval`/`rate_interval` touch the string.

A **dialect** is registered per `StoreType` next to the store builder
(`domains/store/<type>/store.go` `init()`), so adding any new store type carries
its dialect through the same recipe — see §10.

### 4.3 Result contract

Every engine returns `QueryResult{ Columns []string; Rows []map[string]any }`.
The metric/Prometheus stores already project their series into columns+rows.
Downstream widget projection treats all engines identically.

### 4.4 Out of scope: vectors

Vector search is **not** a dialect. Either it's expressible as SQL against a
DuckDB/SQLite store (built-in vector functions — then it's just an ordinary SQL
widget, no new surface), or it belongs to a separate vector paradigm outside
this contract. The DSL has no vector concept.

### 4.5 `source.catalog` — three binding modes (v0.9.0)

`source.catalog` accepts three resolver modes — **one field, no new `kind`**:

```yaml
catalog: licenciamento-sle        # literal id   → one catalog (the original mode)
catalog: '{{ var "catalog" }}'    # macro        → switchable catalog (the catalog axis)
catalog: all                      # sentinel     → every workspace catalog, unioned
```

- **Literal id** — the single-catalog meta session (`c.tables`, `c.columns`, …),
  unchanged from earlier versions.
- **`{{ var }}` macro** — `source.catalog` is expanded through the **same template
  pipeline as `source.query`**, *before* the query, against the dashboard's
  resolved variables. The macro must resolve to a single value (a literal id or
  `all`); the `{{ var }}` macro errors on a multi/All selection, so the binding
  variable **must be single-select**. A `catalog:` value with no `{{` is returned
  verbatim (fast-path), so existing dashboards are byte-for-byte unchanged.
  Expansion order: resolve variables → expand `source.catalog` → expand
  `source.query` → execute.
- **`all` sentinel** — the **multi-catalog union session** over every catalog the
  render-ctx workspace can see. `all` is a **reserved catalog id**: the catalog
  validator rejects a catalog literally named `all` so the sentinel can't collide.

**The `all_*` view vocabulary.** Under `catalog: all`, the engine ATTACHes each
workspace catalog READ_ONLY and exposes **catalog-tagged union views** in the
main schema. Queries read these instead of the single-catalog `c.*`:

| View | Shape |
|---|---|
| `all_tables` | `SELECT '<catalog-id>' AS catalog, * FROM <each>.tables UNION ALL …` |
| `all_columns` | `… <each>.columns …` |
| `all_relationships` | `… <each>.relationships …` |
| `all_importstats` | `… <each>.importstats …` |

The injected `catalog` column is the authoritative catalog id (from the handle /
filename), so comparisons/ratios are plain `GROUP BY catalog` and the YAML stays
catalog-count-agnostic:

```yaml
- kind: multistat
  source: { catalog: all, query: "SELECT catalog, COUNT(*) n FROM all_tables GROUP BY catalog" }
```

A workspace with zero catalogs yields empty (but queryable) `all_*` views — an
overview degrades to "no rows", not an error. Metadata-only for v1 (profiles are
not unioned). `all` resolves to the same DuckDB **SQL** dialect as a single
catalog.

---

## 5. The time frame (L1)

Time is a **frame primitive**, not a template variable. Every query gets the
frame for free, derived from the session range and the widget's `frame:` mode.

### 5.1 `frame:` modes

```yaml
frame: as_of        # default
```

| Mode | `(from, to)` resolution | Use |
|---|---|---|
| `as_of` | The session window verbatim; queries read the **latest snapshot within it** via `{{ anchor }}`. | Stat tiles ("rules passing *now*"). |
| `trailing` | Widened to a trailing span ending at the session `to` (default lookback below); narrower selections expand, wider ones are honoured verbatim. | Trend/heatmap tiles that need ≥2 points. |
| `window` | The session window verbatim; queries read the **whole range** via `{{ timeFilter }}`. | Aggregations over the selected span. |

Trend widening lives in **one declarative field**, never in SQL. This
generalizes `domains/dq/dashboard_data.go::trendWindow`.

```yaml
frame: trailing
lookback: 30d        # optional; default = 30 daily points (resolution-derived)
```

### 5.2 Half-open interval

The range is **`[from, to)`** — inclusive start, exclusive end — at every layer.
`{{ timeFilter "dt" }}` → `dt >= <from> AND dt < <to>`. This matches the existing
Go `windowRangeSQL` and avoids double-counting the boundary instant.

### 5.3 Resolution & `interval`

The page's time resolution (`daily`, `hourly`, …) and the frame span derive
`{{ interval }}` (the bucket width). Resolution also sets literal precision
(daily → date, hourly → timestamp). Resolution comes from the solution-page
registry (`infra/pages`), not the DSL.

### 5.4 Empty / out-of-data windows

No anchor clamping. If the picker lands outside the data, `{{ anchor }}` returns
`NULL`, the query yields 0 rows, and the widget renders empty. Empty is the
honest answer — the DSL never falls back to fixtures.

---

## 6. Query templating (L2)

### 6.1 Engine

The `query` is a Go **`text/template`** with default `{{ }}` delimiters.
`text/template` (not `html/template`) — there is **no escaping**; quoting is the
macros' job (§6.4).

**Control flow is forbidden.** `{{if}}`, `{{range}}`, `{{with}}`, `{{define}}`,
`{{template}}`, field access, and assignment are rejected by a lint pass over the
parsed tree (`app/widgets/dsl_template.go::lintNoControlFlow`, which also rejects
unknown macros). The blessed surface is the FuncMap (§6.2) plus simple pipe
access. Authors never need a loop — multi-value expansion is a macro's job (§7),
which is what keeps both the lint and the LLM-authoring path safe.

`missingkey=error` is set: a reference to an undeclared variable is a hard error,
not a silent empty string.

### 6.2 Macro vocabulary

Macros are **portable intents**; each dialect expands them into its own idiom.
The *names* are the contract; the *expansions* are dialect-local.

**Time macros** — resolve against the widget's computed Frame:

| Macro | Intent | SQL expansion | Metrics expansion |
|---|---|---|---|
| `{{ from }}` / `{{ to }}` | Frame bounds as a literal | `TIMESTAMP '2026-04-28 00:00:00'` | RFC3339 / unix (rarely used in-string) |
| `{{ timeFilter "col" }}` | Restrict to the frame | `col >= <from> AND col < <to>` (or `TRUE` if unbounded) | *no-op* (`TRUE`); frame rides `opts` |
| `{{ anchor "rel" "col" }}` | The as-of snapshot timestamp | `(SELECT MAX(col) FROM rel WHERE col >= <from> AND col < <to>)` | instant-at-`to` semantics |
| `{{ interval }}` | Bucket width | `INTERVAL '1 day'` | step (`1d`) |
| `{{ timeGroup "col" }}` | Bucketing expression | `time_bucket(INTERVAL '1 day', col)` (DuckDB) / `date_bin(…)` (PG) | n/a |
| `{{ rate_interval }}` | Range-selector width | n/a | `[5m]` window |

**Variable macros** — resolve against declared variables (§7):

| Macro | Intent | Single | Multi | Select-all | Empty |
|---|---|---|---|---|---|
| `{{ var "name" }}` | One value | `'isop'` | *error — use `filter`/`values`* | — | — |
| `{{ filter "col" "name" }}` | A predicate for `col` | `col = 'isop'` | `col IN ('a','b')` | `TRUE` (wildcard) / enumerated | per `on_empty` |
| `{{ values "name" }}` | Quoted value list | `'isop'` | `'a','b'` | all options | `` (empty) |

`{{ filter }}` is the workhorse: the **same SQL** works whether the variable is
single, multi, or all-selected — flipping `multi: true` switches `=` → `IN`
without touching the query.

**Search macro** — resolves against the polished-table search box (§8.x):

| Macro | Intent | Term set | Term empty / no columns |
|---|---|---|---|
| `{{ search }}` | A case-insensitive substring predicate over `search_columns` | `(colA ILIKE '%term%' ESCAPE '\' OR colB ILIKE '%term%' ESCAPE '\')` | `TRUE` (no-op) |

`{{ search }}` takes no arguments — it reads the widget's `search_columns` and
the live search term. It is **only available on `kind: polished-table`** with
`searchable: true`; for any other widget the term is never threaded (the macro,
if written, renders `TRUE`). Substring `ILIKE`, **not** full-text/BM25 — FTS is
not reachable from the read-only catalog meta session, and a substring scan over
a few-thousand metadata rows is instant (see
`docs/design/intelligent-catalog-dashboards.md` §5). The term is escaped for
both SQL string literals (quote-doubling) and `ILIKE` wildcards (`%` / `_` / `\`
are treated as literals via an explicit `ESCAPE '\'`).

### 6.3 Reserved names

The macro names above (including `{{ search }}`), plus the document keys
`dsl_version`, `id`, `title`, `header`, `default_source`, `variables`, `rows`,
and the widget keys `kind`, `span`, `source`, `frame`, `lookback`, `refresh`,
`searchable`, `search_columns`, `page_size`, `flat`, `drilldown`, `col`, `row`,
`value`. New names enter via MINOR bumps.

### 6.4 Safety

- **Time values** are server-derived `time.Time`, rendered as dialect literals —
  injection-safe by construction.
- **Variable values** are quoted/escaped by `var`/`filter`/`values` according to
  the variable's `value_type` (§7). Identifiers (column/relation args to macros)
  are validated with `store.SanitizeColumn` — the single existing injection
  guard, reused, not reinvented.
- **The search term** (`{{ search }}`) is user free-text: it is single-quoted
  with embedded quotes doubled, and `ILIKE` wildcards (`%` / `_` / `\`) are
  neutralised via an explicit `ESCAPE '\'`, so a search box can neither break
  out of the literal nor smuggle a wildcard. `search_columns` go through
  `store.SanitizeColumn` like every other identifier.
- The query body **between** macros is the author's responsibility. We do not
  parse or sanitize arbitrary SQL.

### 6.5 Portability scope — macros, not queries

Macros render flavor-correct across dialects. The **surrounding query does not
transpile**: `QUALIFY`, `EPOCH()`, `string_agg(… ORDER BY …)` and other
DuckDB-isms are the author's responsibility and will not run unchanged on
Postgres. The contract guarantees correct **frame + variable injection** across
engines — nothing more.

---

## 7. Template variables (§ L0)

Variables are user-declared **scope controls** — rendered next to the time
picker, bound into queries via the variable macros (§6.2). They come in two
flavors that share one struct, one render path, and one macro:

- **Dimensions** — mandatory scope (env, region). Always shown; always resolve
  to a value (worst case `All`). The dashboard *is a view of* its dimensions.
- **Filters** — optional refinements (ci, active). Hidden until added; absent →
  no constraint.

The split is **UI + lifecycle, not query semantics**: at the query layer a
dimension set to `All` and a filter that was never added emit the *same* SQL
(`TRUE`). There is no second engine — `{{ filter }}` (§6.2) absorbs
single / multi / all / absent for both. This is the Grafana template-variable
model, split into the always-present and the add-on-demand halves.

### 7.1 Declaration

Two top-level blocks; entries share the same fields (§7.2). The block — not a
per-entry flag — is what declares "mandatory scope" vs "optional refinement",
and sets the lifecycle + sensible defaults.

```yaml
dimensions:                       # mandatory scope — always shown
  - name: env
    label: "Environment"
    icon: server                  # riclib/icon name (optional)
    value_type: string            # string | number | identifier | bool
    default: prod
    options: [prod, staging, dev]
  - name: region
    label: "Region"
    icon: globe
    multi: true
    include_all: wildcard         # "All" → no constraint
    query: |                      # query-derived options, frame-aware (xor `options:`)
      SELECT DISTINCT region AS value FROM p.profiles WHERE {{ timeFilter "dt" }}

filters:                          # optional refinements — added on demand
  - name: ci
    label: "CI only"
    icon: git-branch
    value_type: bool              # toggle: All / Yes / No
  - name: active
    label: "Active"
    icon: activity
    value_type: bool
```

Shared fields: `name` (required), `label`, `icon` (riclib/icon name),
`value_type` (`string` default | `number` | `identifier` | `bool`), `multi`,
`include_all`, `on_empty`, `default`, `options_refresh` (`on_load` default |
`on_window` — meaningful only with a `query:` option source; see §7.4), and
exactly one option source (`options:` static list **xor** `query:` **xor**
`from:` built-in — see §7.7). No field is exclusive to a block.

### 7.2 Cardinality & the special states

The runtime binds each variable to a typed value:

```go
type VarValue struct {
    Selected  []string  // 0..n chosen values
    IsAll     bool      // "Select all" chosen
    Multi     bool      // declared cardinality
    ValueType string    // string | number | identifier | bool
}
```

- **Single** (`multi: false`) — `Selected` has one entry; `{{ var }}` and
  `{{ filter }}` both apply.
- **Multi** (`multi: true`) — `{{ filter }}` → `IN (...)`; `{{ var }}` errors.
- **Bool** (`value_type: bool`) — a tri-state toggle (All / Yes / No);
  `{{ filter }}` emits `col = true` / `col = false` / `TRUE`.
- **Select all** — controlled by `include_all`:
  - `wildcard` — `{{ filter }}` emits `TRUE` (no enumeration; cheapest, robust to
    large/dynamic option sets).
  - `enumerate` — `{{ filter }}` emits `IN (<all options>)`.
  - `false` — no "all" option offered.
- **Empty / absent** — controlled by `on_empty`:
  - `no_filter` — `{{ filter }}` emits `TRUE` (don't blank the dashboard).
  - `empty_result` — emits `FALSE`.
  - An **inactive filter** (never added) is simply absent from the bound set —
    `{{ filter }}` sees no value and emits `TRUE`, identical to a dimension at All.

All states are absorbed by the macro in **one tested Go function** — never by
YAML control flow.

### 7.3 Dimensions vs filters

| | Dimension | Filter |
|---|---|---|
| Default | always a value (→ `All`) | absent (no constraint) |
| Mandatory | yes — can't be empty (multi+`include_all` → All; single needs `default`) | no |
| UI | persistent chip in the cluster | behind "+ Filter", removable |
| Cardinality | leans multi/all | leans toggle/single |
| Lifecycle | always bound | add → bound; remove → absent |

Only the lifecycle + defaults differ; the fields, the macro, and the SQL are
shared.

### 7.4 Query-derived options are queries

A variable's `query` runs through the **same** Frame + dialect + macro pipeline
as a widget query. That buys chaining for free, but a naively frame-bound option
query can empty its own dropdown — so freshness is an explicit per-variable knob.

**Chained variables work for free.** Variable *B*'s option query may reference
`{{ var "A" }}` (or a dimension). Resolution order follows declaration order
(dimensions before filters); cycles are a load-time error **(planned)**.

**Optional count column (0.13).** The option query yields its values from the
`value` column (falling back to the first column when none is named `value`).
It MAY yield one more column — named `count`, `n`, or `cnt`, or simply the
**second** column — read as the occurrence count for that value:

```sql
SELECT action AS value, COUNT(*) AS n FROM events
WHERE {{ timeFilter "ts" }} GROUP BY 1 ORDER BY 2 DESC
```

Counts are **presentational only** — the filter-picker UI shows them so users
can filter by significance rather than name. Selection semantics, the variable
macros, and chaining never read them. A missing or non-numeric count column
degrades to values-only; it is never an error. Counts render against the same
frame as the values, so `options_refresh` governs their meaning too:
`on_window` counts reflect the picker window; `on_load` counts are
window-independent. The cheap-by-construction caveat below applies with extra
force — a `GROUP BY` + `COUNT(*)` over a fat fact table costs more than a
`DISTINCT`; where that is expensive, materialize a small labels table and
count against *it*.

**The hazard.** If an option query carries `{{ timeFilter "dt" }}`, a narrow
picker window can return *no rows* — the control whose entire job is "what can I
slice by" goes blank precisely when the user has zoomed in. And because the
header self-refreshes on every window change, an unguarded option query re-runs
on every render.

**`options_refresh`** makes that an explicit choice. The **frame binding**
below is **live** (S-1152); the **reuse across partial header refreshes**
(resolve-once-per-page-render + stash) is the **planned** optimization — until
it lands, an `on_load` list is window-independent but still re-queried on each
header refresh (cheap by the caveat below):

| `options_refresh` | Frame | When the list refetches | Use |
|---|---|---|---|
| `on_load` *(default)* | **unbounded** — `{{ timeFilter }}` renders `TRUE`, so the list cannot self-empty | once per **full page render**, then reused across partial header refreshes; a **page reload** re-resolves | reference dimensions (schemas, regions, tenants) — slowly-changing, not a function of the window |
| `on_window` | the session window | re-resolves on `time_window` | "only show values present in the selected window" — the real but minority case |

Default is `on_load`. The two failure modes are not symmetric: `on_window`'s is
an *empty, broken-looking* dropdown; `on_load`'s is merely *stale* (a value
onboarded today shows up on the next reload). Stale-but-present beats
correct-but-empty for a scope control — and `on_load` resolves the empty-dropdown
hazard above for free.

There is **no per-chip refresh button**: the full-page GET *is* the refresh
affordance. This is Grafana's "on dashboard load", minus its "refresh on
dashboard creation" mode — baking options at save time goes stale and is a known
bug source, so the DSL never freezes options.

> **Caveat — `options_refresh` is a freshness knob, not a performance lever.** It
> governs *which trigger refetches the list*, and nothing about cost. An option
> query MUST be cheap by construction; do **not** reach for caching machinery to
> mask a slow `DISTINCT` over a fat fact table. Where a value set is genuinely
> expensive to compute, that is the **solution designer's** call and has many
> homes — materialize a small labels/dimension table in the dataload, stand up a
> local DuckDB label store refreshed periodically, and so on. It is hard to
> predict *where* a given workload should be optimized; it is easy to see that the
> option-refresh knob is **not** the place. This contract specifies the freshness
> semantics and leaves performance to the layer that owns the data.

> **⚠️ Large-table hazard (S-1730).** The `on_load` frame renders
> `{{ timeFilter }}` as **`TRUE`** — the option query scans the WHOLE table on
> every full-page header render, *independent of the picker period*. On a fat
> fact table that is an unbounded scan: on the demo estate an `on_load`
> `SELECT DISTINCT name FROM metrics` over a 72M-row / 1.5GB table, stacked with
> an uncapped engine, drove load to ~200 and seized the box. This is by design
> (unbounded is what makes the list window-independent), so on any large table
> **do not** leave a query-derived option list on the `on_load` default:
> declare `options_refresh: on_window` to bound the scan to the window, or
> materialise a small dimension/labels table and point the option query at that.
> Reserve `on_load` for genuinely small, slowly-changing reference sets.

### 7.5 Metrics dialect

`{{ filter "label" "name" }}` against a metrics store renders a label matcher
(`label=~"a|b"`) rather than an `IN` clause; select-all wildcard → `label=~".+"`
or a dropped matcher. Same macro, dialect-local expansion.

### 7.6 UI placement

Dimensions render as persistent chips clustered immediately **left of the time
selector** (`[env][region] [time]`) with a per-chip option dropdown; filters via
a `+ Filter` affordance that opens the **filter-picker panel** (S-1606): one
section per declared filter, each a searchable value table showing the optional
count column (§7.4), with checkbox (multi) or radio (single/bool) selection and
an "All" clear row. Active filters render as removable chips whose face click
re-opens the panel focused on their section. Each variable carries its optional
`icon`. Both persist per-tab like `TimeRange` and fan out a `variablesChanged`
refresh that re-renders the affected widgets + the header cluster — selections
apply per toggle, so the dashboard re-slices live behind the open panel. Filter
option queries run only when the panel opens (lazily), not per header render.

### 7.7 Built-in option sources — `from:` (v0.9.0)

Alongside static `options:` and frame-aware `query:`, a variable may draw its
options from a **built-in source** via `from:`. v1 ships one value:

| `from:` | Option list |
|---|---|
| `workspace_catalogs` | the render-ctx workspace's available catalog IDs (its `AvailableCatalogs`, falling back to every active catalog when the workspace declares no ceiling) |

`from:` is **mutually exclusive** with `options:` and `query:` (one option source
per variable). No SQL runs — the list is enumerated from the workspace.

**`prepend:`** places literal values **ahead** of a `from:`-derived list (deduped
against it). This is how the **catalog axis** offers the `all` (union) option as
an ordinary first option — *not* via `include_all` (which is a `WHERE`-predicate
semantic, the wrong axis; see §4.5 and the design doc §2.1):

```yaml
dimensions:
  - name: catalog
    label: Catalog
    from: workspace_catalogs       # options = the workspace's catalogs …
    prepend: [all]                 # … with the union sentinel first
    default: all                   # or the workspace's default catalog
default_source:
  catalog: '{{ var "catalog" }}'   # the catalog axis binds source.catalog (§4.5)
```

**Guardrail.** A `from: workspace_catalogs` dimension binds a `source.catalog`
engine through `{{ var }}`, which needs a determinate single value — so it must be
**single-select**: `multi: false` and **no** `include_all`. The validator rejects
both, turning the render-time `{{ var }}` error into a loud load-time config
error. (The full cross-reference of *which* variable a given `source.catalog`
binds is harder; the validator enforces the simpler invariant on the catalog-axis
dimension itself.)

---

## 8. Widgets

```yaml
- id: <unique>
  kind: metric-card        # see table
  span: 3                  # 1..12
  title: "…"
  source: { … }
  frame: as_of
  refresh: { on: [time_window, variable] }
  flat: true               # any kind — drop the raised panel surface (default false); v1 honoured by `text`
  # kind-specific presentation fields:
  label: "…"               # metric-card
  format: compact|percent|raw
  status_text: "…"
  smooth: true             # line-chart — smoothed curves (default false → straight)
  area: true               # line-chart — fill under each series (default false)
  orientation: vertical    # bar-chart — vertical columns (default "" → horizontal bars)
  row_id_column: rule_id   # polished-table
  status_pill_columns: [status]
  searchable: true         # polished-table — render a search box; pairs with {{ search }} in the query
  search_columns: [name]   # polished-table — columns the {{ search }} macro ILIKEs (empty → no-op)
  page_size: 25            # polished-table — rows per page (0 → no pagination unless searchable, then default 25)
  series: catalog          # multistat — column that labels each tile
  value: n                 # multistat — column rendered as the big number
  sort: -n                 # multistat/info-card — order ("-col" desc / "col" asc; default = query order)
  limit: 12                # multistat/info-card — cap how many cells fit; overflow → "+M more"
  status: state            # info-card — column carrying the semantic StatusLevel value (the card pill)
  heading: name            # info-card — column rendered as each card's title
  body: detail             # info-card — optional column rendered as each card's prose body
  per_row: 4               # info-card — max cards per row (grid column ceiling; default 4, 1..12)
```

| `kind` | Shape | First-column convention |
|---|---|---|
| `metric-card` | single value | `rows[0][cols[0]]` |
| `line-chart` | X + N series | col[0] = X (time/category), col[1..] = series; `smooth`/`area` style it. A **time** X-axis spans the chosen frame window, not just the data extent — sparse/empty periods stay visible (S-1187). |
| `bar-chart` | categories + values | col[0] = category, col[1..] = series; `orientation: horizontal`(default)`\|vertical` |
| `donut` | name + value slices | col[0] = name, col[1] = value |
| `status-list` | labelled rows + status | documented per widget |
| `heatmap` | row × column grid | Axes declared by name — `col` / `row` / `value` (§8.3), not positional. `col`/`row` are `category` (verbatim, query order) or `time` (bucketed, chronological); `value` is the cell-color column. One row per `(col, row)` cell (aggregate in SQL). The entities × controls grid is `col: control, row: empresa, value: status`. |
| `polished-table` | full table | all columns; `row_id_column` / `status_pill_columns`; `searchable` + `search_columns` add a `{{ search }}`-backed search box, `page_size` paginates server-side (Go-side slice of the search-filtered set) |
| `multistat` | series key + value → N tiles | `series` = tile label (default col[0]), `value` = big number (default col[1]); reuses `format`/`label`/`status_text`; `sort`/`limit` cap + order, overflow → "+M more" |
| `info-card` | status + heading + body → N cards | Columns declared by name — `heading` = card title (required), `status` = semantic `StatusLevel` pill (required; unknown → neutral, never an error), `body` = optional prose. `per_row` caps the adaptive grid columns (default 4, 1..12); `sort`/`limit` order + cap, overflow → "+M more". A one-row query renders one standalone card (same path). |
| `text` | single string | first cell of the first row, coerced to string; plain HTML-escaped text (no markdown at v1) |

Projection details (empty handling, formatters) live with the widget renderers
and are governed by the widget contract, not this DSL.

### 8.1 `refresh.on`

Events that re-fetch the widget:

| Event | Fires when |
|---|---|
| `time_window` | the session time-range changes |
| `variable` | any bound template variable changes |
| `conversation_appended` | *(reserved — editor doc M3)* |

### 8.2 Drill-down (`drilldown`)

A widget-level `drilldown` makes the widget's marks navigate to another dashboard
page with the clicked mark's value(s) preset as **declared variables** on the
target. It is a `LinkSpec` — the same type as `ColumnSpec.link`:

```yaml
- id: posture-heatmap
  kind: heatmap                  # rows = empresa, cols = control
  drilldown:
    target: controls.okrs        # a registered page id (resolved via pages.Get)
    params:                      # target variable  →  mark-coordinate placeholder
      empresa: "{row}"           # bind the `empresa` variable to the row axis
      control: "{col}"           # bind the `control` variable to the col axis
```

- `params` reads **target-variable → mark-coordinate placeholder** — the same
  direction as a polished-table column link (`var: "{column}"`), so one mental
  model spans every widget.
- **Placeholders are per widget kind:** `heatmap` → `{row}` / `{col}`; `donut` →
  `{name}`; `bar-chart` → `{category}` / `{series}`; `polished-table` →
  `{<column>}`. A heatmap binding **both** axes drills by row, column, or cell.
- **For a heatmap the placeholder resolves against the declared axis (§8.3), and
  the axis `type` decides the drill target:** a `category` axis seeds a
  **variable** (`?<var>=<value>`); a `time` axis seeds a **period**
  (`?from=…&to=…`) — same gesture, different URL.
- Every variable named in `params` MUST be a **declared dimension or filter** on
  the **target** dashboard. The value is honoured exactly as if the user picked it
  — it flows through the `{{ filter }}` macro's quoting/sanitising, so the URL
  introduces no new injection surface.
- `target` is same-workspace at v1 (`LinkSpec.kind: page`); `external` /
  `workspace-page` are reserved.

A **categorical** drill-down does not carry the time window — it rides the
surviving tab session across in-workspace navigation. A **time-axis** drill (§8.3)
*is* a window change; encoding the window in the URL for shareable / cold-load
links is a later, separate addition.

Rendering is a **data-attribute** contract consumed by an interactive-widget JS
module (the picker/calendar carve-out), not per-mark URLs — the per-mark-URL shape
does not scale to a dense `entities × controls` grid. See
`docs/design/dashboard-drilldown.md` §4.3–4.4 for the emitted attributes and the
navigation handler, and §11 for the SSR reconciliation.

> **Worked example.** `gitstore/solution/internaldemo/dashboards/` is a drill-down
> showcase: `internaldemo.overview.yaml` (an entities × controls heatmap carrying
> this `drilldown:` block) drills into `internaldemo.detail.yaml` (declaring the
> `empresa` / `control` target dimensions seeded from the URL). The patterns —
> register target-before-source, `include_all: wildcard` on drill-target
> dimensions, the `__all__` un-drilled-axis sentinel — are visible in those two
> documents.

### 8.3 Heatmap axes (`col` / `row` / `value`)

A `kind: heatmap` declares its three axes by **name**, not by column position:

```yaml
- kind: heatmap
  col:   control            # column axis — bare name ≡ { field: control, type: category }
  row:   empresa            # row axis
  value: status             # the column whose value maps to the cell color
  source: { catalog: brisa, query: "SELECT control, empresa, status FROM …" }
```

- **`col` / `row`** take a bare column name (`type: category` implied) or an
  object `{ field, type, unit, format }`. **`value`** is a bare column name.
  SELECT column order is irrelevant.
- **`type: category` (default)** — the value renders **verbatim** as both the cell
  key and the axis label; axis order follows the query's `ORDER BY` (no lexical
  re-sort).
- **`type: time`** — the value is parsed and **bucketed** by `unit` (`hour` |
  `day` | `week` | `month` | `quarter` | `year` | `auto`), labelled via `format`
  (a Go time layout, e.g. `"01-02"` / `"Jan 2006"`; `quarter` / `week` use a
  built-in formatter; sub-day buckets default to `"01-02 15:04"`), and sorted
  chronologically:

  ```yaml
  col: { field: dt, type: time, unit: day, format: "01-02" }
  ```

- **A time COLUMN axis is frame-driven (0.15.0)**: its buckets are generated
  from the resolved time window, pre-seeded across the whole span — the grid
  always covers the period the user picked, and a bucket with no data renders
  as an **empty cell** rather than a silently missing column. (A time ROW
  axis, and any axis without a resolvable frame, stays data-derived as
  before.)
- **`unit: auto` (0.15.0)** derives the bucket width from the window span,
  aiming at ~40 columns whatever range is picked: 24 h → 1 h buckets, 7 d →
  4 h, 30 d → a day, a year → weeks. The ladder is 1m/5m/15m/30m/1h/2h/4h/
  6h/12h then day/week/month — the narrowest width keeping the window at or
  under ~44 columns. Without a frame, `auto` falls back to `day`.
- **Duplicate cells keep the WORST status (0.15.0)** — severity order
  `fail/error/critical/high` > `warn/warning/elevated` > *unknown* >
  `inconclusive/info` > `pass/ok/healthy`. This replaces the previously
  *unspecified* last-write-wins: a query MAY now simply emit one graded row
  per source point (e.g. per hourly KPI round) and let the widget fold them —
  the wall exists to surface the bad hour, and a later ok must not paint over
  it. Pre-aggregating in SQL remains valid.
- Rendering notes: past ~16 columns the axis labels the first bucket of every
  ~12th column (cell tooltips keep the exact bucket); the heatmap is a
  server-rendered grid and refreshes wholesale on window change (it is exempt
  from the chart-canvas morph preservation that protects uPlot widgets).

> **Breaking change (0.12.0).** This replaces the prior positional contract
> (`cols[0]` parsed as a date and formatted `MM-DD`, `cols[1]` = row label,
> `cols[2]` = status, with a silent categorical fallback). Existing heatmaps must
> declare `col`/`row`/`value`; date heatmaps must add `type: time`. Motivation +
> the 13-document migration: `docs/design/dashboard-drilldown.md` §3.

Worked example: `gitstore/solution/internaldemo/dashboards/internaldemo.overview.yaml`
(categorical `col`/`row`/`value` heatmap) — see §8.2.

### 8.4 Info cards (`status` / `heading` / `body` / `per_row`)

A `kind: info-card` projects **one row → one card**: a status-colored card
pairing a status pill with a heading and an optional prose body, laid out in an
adaptive grid. It is the prose sibling of `multistat` (label + big number) — use
it for a status roster (a solution fleet, a set of checks, a service inventory)
rather than a numeric strip. Columns are declared **by name**:

```yaml
- kind: info-card
  span: 12
  per_row: 4                # max cards per row (grid column ceiling); default 4, 1..12
  status: state             # column carrying the semantic StatusLevel value (drives the pill)
  heading: name             # column for the card title (required)
  body: detail              # optional column for the prose body
  sort: heading             # optional — "-col" desc / "col" asc; default = query order
  limit: 24                 # optional — cap cards; overflow → "+M more"
  source: { store: fleet, query: "SELECT name, state, detail FROM solutions" }
```

- **`heading` + `status` are required** (a card's identity is its title + pill);
  **`body` is optional** — a missing/empty body renders heading + pill only.
- **`status`** carries the `StatusLevel` vocabulary (`normal` | `warning` |
  `error` | `info`); the value maps to the pill via the same
  case-insensitive parser the status-list uses. An **unrecognised value degrades
  to the neutral/unknown pill — never an error**. The DSL never names colors.
- **`per_row`** caps the columns; the grid still reflows to fewer on a narrow
  tile. A **one-row query renders a single standalone card** via the same render
  path (`per_row` is inert with one card) — the prose sibling of a `metric-card`.
- A row with an **empty heading** falls back to the status value rather than
  rendering blank.
- `sort` / `limit` mirror `multistat`: sort orders before the cap keeps the
  top-N, and overflow collapses into a trailing "+M more" cell.

---

## 9. Validation & errors

A document is validated at load/register time (`infra/dashboard/validate.go`,
panic-at-register) and the template is linted at render time
(`dsl_template.go`). The split below notes which check runs where:

1. **Schema** *(load)* — required fields present; `span` ∈ 1..12; one engine per
   source after merge; known `kind` / `frame` / `format`.
2. **Version** *(planned)* — `dsl_version` compatible with the runtime (§2.2). Not
   yet parsed or enforced.
3. **Template parse** *(render)* — the `query` parses as `text/template`; **no
   control-flow nodes** (the §6.1 lint).
4. **Macro availability** *(render)* — every macro the query uses is implemented
   by the resolved dialect (the lint rejects unknown macros). *"`{{ anchor }}` is
   not available for store type `prometheus`"* surfaces here.
5. **Variable references** *(render)* — every `{{ var/filter/values "x" }}` names
   a declared variable (`missingkey=error`).
6. **Drill-down** *(load, partial)* — `drilldown` is validated for
   well-formedness at register (kind ∈ {`""`, `page`}, non-empty `target`). The
   `target`-**resolution** check (that the page is registered) is **deferred** to
   render time because the target is workspace-scoped (S-1499), not a global
   load-time guarantee. No declared dimension/filter may use a reserved name
   (`page`, `conv`, `from`, `to`, `preset`, `kind`, `day`, `key`, `writer`,
   `workspace`, `q`, `sort`, `dir`, `search`) — those are shell-nav / time /
   list-filter URL params, and a collision would let a seeded drill-down param be
   mistaken for one (or vice versa).

Errors render as an inline error tile inside the cell; one failed widget never
kills the dashboard (per editor doc §11).

---

## 10. Extensibility

### 10.1 Adding a store type / dialect

A dialect maps a store type to its macro FuncMap. As shipped, the registry lives
**inside the renderer package** (`app/widgets/dsl_dialect.go`): a package-local
`dialectRegistry map[store.StoreType]Dialect` populated in `init()`. Catalog +
comply engines (DuckDB) resolve to `defaultSQLDialect` directly. Adding a store
type with a non-default query language adds an entry there.

> The original design intent was a `dialect.Register(StoreType, …)` call from each
> store's own `init()` (a separate `dialect` package), so a new store type carried
> its dialect through the store recipe. That indirection was **not** built; the
> registry is centralized in `widgets`. Re-homing it is a future MINOR if/when an
> out-of-tree store type needs to ship its own dialect.

The live `Dialect` interface (`dsl_dialect.go`):

```go
type Dialect interface {
    // Template funcs for this query language, bound to the resolved frame, the
    // resolved template-variable values, and the polished-table search inputs.
    FuncMap(f frame, vars map[string]VarValue, s searchInputs) template.FuncMap
    // Which macro names this dialect implements — the renderer rejects a query
    // referencing an unsupported macro before executing (the §6.1 lint).
    Macros() []string
}
```

There is **no `ApplyFrame` method**. For the shipped SQL family the frame is
injected entirely **in-string** via the time macros (the FuncMap closes over the
resolved `frame`), then the **existing** `Store.Query(rendered, opts)` runs — no
type switch in the renderer. The out-of-band (metrics) frame-routing path
described in §4.2 is the intended shape for a metrics dialect, not a shipped
interface method.

### 10.2 Adding a macro or widget kind

Additive → MINOR bump. New macros SHOULD be implemented across all in-family
dialects, or explicitly excluded (and caught by §9.4).

---

## 11. Worked examples

### 11.1 As-of stat tile (SQL)

```yaml
- id: rules-passing
  kind: metric-card
  span: 3
  frame: as_of
  source:
    query: |
      SELECT COUNT(*) AS cnt FROM (
        SELECT DISTINCT rule_id FROM p.dq_rule_results
        WHERE dt = {{ anchor "p.dq_rule_results" "dt" }} AND status = 'PASS'
      )
```

### 11.2 Trend tile (SQL, widened frame)

```yaml
- id: data-volume-trend
  kind: line-chart
  span: 8
  frame: trailing          # widens to a trailing span ending at the picker `to`
  source:
    query: |
      WITH tbl AS (
        SELECT table_schema, table_name, dt, MAX(total_rows) AS tr
        FROM p.profiles
        WHERE {{ timeFilter "dt" }}
        GROUP BY table_schema, table_name, dt
      )
      SELECT dt, SUM(tr) AS total_rows FROM tbl GROUP BY dt ORDER BY dt
```

### 11.3 Multi-select + select-all dimension (SQL)

```yaml
dimensions:
  - name: schema
    label: "Schema"
    icon: layers
    multi: true
    include_all: wildcard
    on_empty: no_filter
    query: |
      SELECT DISTINCT table_schema AS value
      FROM p.profiles WHERE {{ timeFilter "dt" }}

rows:
  - widgets:
      - id: rows-by-schema
        kind: bar-chart
        span: 6
        frame: window
        source:
          query: |
            SELECT table_schema, SUM(total_rows) AS rows
            FROM p.profiles
            WHERE {{ timeFilter "dt" }} AND {{ filter "table_schema" "schema" }}
            GROUP BY table_schema ORDER BY rows DESC
```

`{{ filter "table_schema" "schema" }}` renders `table_schema IN ('a','b')` for a
multi-selection, `TRUE` when "all" is chosen (wildcard), and `TRUE` when nothing
is selected (`no_filter`) — the query text never changes.

### 11.4 Metrics store (frame out-of-band)

```yaml
default_source: { kind: data, store: prod-metrics }   # Store.Type() == prometheus

rows:
  - variant: chart
    widgets:
      - id: request-rate
        kind: line-chart
        span: 12
        frame: window
        source:
          query: |
            sum(rate(http_requests_total{ {{ filter "route" "route" }} }{{ rate_interval }}))
```

Here `from`/`to`/`step` would ride the range-query `opts` out-of-band; only
`{{ rate_interval }}` and `{{ filter }}` touch the string. The same dashboard
could place a SQL widget beside this one — frame is global, dialect is
per-widget. **(Planned.)** The shipped dialects are the DuckDB SQL family
(catalog/comply); the metrics out-of-band frame path is the intended shape (§10),
not yet built — there is no `ApplyFrame` method today.

---

## 12. Open questions (tracked, not yet decided)

- **`timeGroup` flavor coverage** — confirmed for DuckDB (`time_bucket`); Postgres
  (`date_bin`) needs a decision on the minimum-version baseline.
- **`interval` units** — fixed (`1 day`) vs. auto (span ÷ width, Grafana-style).
  v0.1 specifies resolution-derived; auto-interval may arrive in a MINOR bump.
- **Sub-daily `anchor`** — `anchor` semantics on hourly resolution (latest hour
  vs. latest day) need a worked case before freeze.
- **`dsl_version` enforcement strictness** — warn vs. reject on a missing version
  in a *published* (non-hand-authored) document.

---

*This is a draft contract. Until 1.0.0, treat every section as subject to change
under §2.1. Implementation is tracked under S-1140 and milestone M1.6 of the
[Dashboard DSL + editor](https://linear.app/riclib/project/dashboard-dsl-editor-609a563166e6)
project.*
