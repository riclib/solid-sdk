<!--
  ┌──────────────────────────────────────────────────────────────────────┐
  │  Dashboard DSL — Query & Widget Contract                               │
  ├──────────────────────────────────────────────────────────────────────┤
  │  Contract version : 0.23.0                                             │
  │  Status           : DRAFT — contract not yet frozen (pre-1.0)          │
  │  Stability         : unstable; minor versions may break (see §2)       │
  │  Surface           : external — authored by humans, the editor, and    │
  │                      the LLM; third parties write against this.        │
  │  Last updated      : 2026-08-12                                        │
  │  Owner ticket      : S-1140 (design) / S-1524 (this correctness pass)  │
  │  Implements        : shipped — frame, macros, dialect (see §1.1)       │
  │  Supersedes        : the hand-rolled window SQL in                     │
  │                      domains/dq/dashboard_data.go                      │
  └──────────────────────────────────────────────────────────────────────┘
-->

# Dashboard DSL — Query & Widget Contract

**Contract version 0.23.0 · Draft · `dsl_version: "0.23"`**

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
all §6.2 macros (`from`/`to`/`timeFilter`/`spanFilter`/`anchor`/`interval`/
`bucket`/`worstStatus`, `var`/`filter`/`values`, `search`, `key`/`row`/`ancestor`), the §5 frame modes, the control-flow lint
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
| 0.17.0 | 2026-07-19 | Additive (diagram widget, S-1785): new widget `kind: diagram` — one query's rows drawn as a mermaid flowchart, rendered SERVER-SIDE (no chart library, no client JS; the platform's diagram lightbox provides zoom/download). Each row is one EDGE; columns declared by name: `from` / `to` (required — the edge's node labels), optional `edge_label` (annotation on the arrow, e.g. a runs count) and `status` (accents the row's TARGET node: fail/error red, warn amber). Presentation fields: `direction` (LR default, TB/TD/RL/BT) and `limit` (edge cap, default 80 — overflow renders a "+M more edges not drawn" foot note, never silently). Nodes are the distinct labels, ids assigned in first-appearance order — ORDER BY the important edges first; the cap keeps the head. First consumer: solidmon.pipeline's wiring map (triggers → pipeline → children lineage). (diagram) |
| 0.18.0 | 2026-08-11 | Additive (timeline widget, S-2160): new widget `kind: timeline` — one query → Gantt lanes, one lane per `row` value, a true bar per result row spanning `start`→`end` on a continuous time axis, colored by a **canonical status vocabulary** (`ok` / `warn` / `fail` / `running` / `queued` / `cancelled`; the QUERY maps raw system statuses, exactly as for the heatmap). New fields `start` / `end` / `key` (columns, by name) and the blocks `card:` (a declared hover-detail query) + `expand:` (a full nested sub-lane axis + query, itself nestable); reuses `row` / `value` / `limit` / `drilldown`. Two new macros, bound ONLY inside those sub-queries: `{{ key }}` (the hovered bar's `key` value) and `{{ row }}` (the expanded lane's key), both expanding to sanitized SQL literals. A NULL `end` means in-flight (the bar runs to now and keeps its status color); `key` is required exactly when a `card:` is declared, at every `expand:` level. The widget is **system-blind** — it knows lanes, bars, statuses and a window, nothing about the system that produced them. (timeline) |
| 0.18.1 | 2026-08-11 | PATCH (doc-only, no schema change): §6.1 — macros expand BEFORE the SQL is parsed, so a macro named inside a `--` comment is still a call. A comment written to explain `{{ filter }}` is an argument-less invocation of it and fails to render (found while authoring the first `timeline` consumer, S-2162). Write the macro's name, not its call. No behaviour change. (macro-in-comment) |
| 0.19.0 | 2026-08-11 | Time-axis labels follow the resolved unit (S-2163). A `type: time` axis now labels by RESOLUTION rather than one fixed stamp: clock times (`21:00`) inside a single calendar day, day-anchored (`… 23:00 08-11 01:00 …`) across a window that crosses midnight, calendar units unchanged; cell tooltips still carry the full `01-02 15:04` stamp. Tick thinning past ~16 columns now prefers natural boundaries (day starts, then top-of-hour / midnight / Monday-1st, then a positional fill) instead of every Nth column. **Breaking-shaped, under MAJOR 0 (§2): `unit: auto` + `format:` is now a register-time ERROR** — auto resolves a width that changes with the window and one fixed layout cannot follow it (this combination produced the repeated-date axis). No shipped document combined the two; explicit units keep honouring `format:`. (time-axis-labels) |
| 0.20.0 | 2026-08-11 | Additive (S-2171): new time macro **`{{ spanFilter "start" "end" }}`** — the INTERVAL sibling of `timeFilter`, selecting rows whose span OVERLAPS the frame (`start < <to> AND (end IS NULL OR end >= <from>)`; `TRUE` when unbounded, and a half-bounded frame drops only the test it cannot make). A NULL end means STILL RUNNING and overlaps every window after its start. Rule of thumb: window on an INSTANT for event tables, on the INTERVAL for span tables — a `kind: timeline` over `timeFilter(start)` loses every run that outlives the window edge, which is how it was found (zoom inside a four-hour run → "no runs in this range" while the run fills the screen). Additive: no existing document changes, `timeFilter` is untouched. (span-filter) |
| 0.21.0 | 2026-08-11 | Additive (S-2164): fold a widget's own collapse into SQL. Two macros — **`{{ bucket "col" }}`**, the resolution the WIDGET resolved to for the current window (a heatmap's column-axis unit, a timeline's density grid), and **`{{ worstStatus "col" }}`**, the widget's own severity ranking as an aggregate — so `GROUP BY {{ bucket "at" }}, <lane>` produces the marks that were going to be drawn instead of shipping every row to draw them (measured on a 30-day real estate: a run history 155,735 → 17,239 rows, an activity wall 414,792 → 7,002). The bucket is a macro rather than something you write precisely because it follows the picker window; a constant would break every sub-daily preset (0.17.0). Both are opt-in — the widget still runs its own fold and stays the authority on the wall, exactly for a heatmap and closely for a timeline (§8.5) — and both are a register-time ERROR on a widget that has no fold, rather than a grouping invented on the spot. Additive: new widget key **`count:`** on `kind: timeline` (and on `expand:`), naming how many bars a folded row stands for so a merged segment's hover stays honest; absent → one bar per row, so no existing document changes. **Correction, not a change:** `{{ timeGroup "col" }}` has been listed in §6.2 and §1.1 since 0.1.0 and was never implemented — writing it has always been a hard render error. `bucket` is what it was reaching for, done resolution-aware; the `timeGroup` rows are removed rather than deprecated, since nothing could have been written against them. (fold-pushdown) |
| 0.21.1 | 2026-08-11 | PATCH (S-2176): what an ABSENT status means, stated once. NULL, empty and whitespace in a `value` column all mean "the row exists and nothing graded it", and all fold as **`unknown`** — above `ok`/`queued`/`cancelled`/`running`, below `warn` and `fail`. So a merged timeline segment of eleven `ok` runs and one ungraded one reads `unknown`, not `ok`; one holding a `fail` still reads `fail`. This is the severity order's whole point (an unknown must be visible and must never mask a failure) and it is what the heatmap's own order already said since 0.15.0; the timeline previously let an absent status lose to everything, and `{{ worstStatus }}` previously let it lose by construction, because `arg_max` drops a row whose value argument is NULL BEFORE ranking it. Both now normalise identically, so a folded query and an unfolded one agree — a bug-fix in expansion restoring intended behaviour, hence PATCH: no schema change, no field, no macro, and no document stops working. Also states the distinction a heatmap must never blur (§8.3): a bucket NO row landed in renders EMPTY, a bucket that received ungraded rows renders `unknown` — activity nobody graded is not the same as no activity. Grade in SQL with a total `CASE ... ELSE` if you want an ungraded row to read as something specific. (absent-status) |
| 0.21.2 | 2026-08-12 | PATCH (doc-only, no schema change): two §8.5 corrections and one stated constraint (S-2169). **The example was wrong about windowing.** Every windowing slot in the `kind: timeline` example used `{{ timeFilter }}` — the widget's own `source.query` AND the `expand:` sub-query — five lines under the 0.20.0 warning that a timeline windowed on an instant loses every bar that outlives the window edge. Both are now `{{ spanFilter }}`. Every in-tree consumer was already correct, so this was purely an external-surface defect, hitting exactly the readers the document exists for. **Nesting is now stated, because it constrains what a second `expand:` level can mean.** `{{ row }}` binds the IMMEDIATE parent lane and nothing above it — there is no ancestor chain — so each level's lane key (plus the page's variables) must identify its rows on its own; a level keyed on a value that is unique only within its grandparent returns rows that belong to something else, aligned on the right time axis and silently wrong. Also states that a chevron is offered per LEVEL, not per lane: a lane whose sub-query returns nothing still shows one. No field, no macro, no schema change, and no existing document stops working. (nested-expand) |
| 0.22.0 | 2026-08-12 | Additive (S-2188): an `expand:` level now receives its whole ANCESTRY, and a chevron can be offered per LANE. New macro **`{{ ancestor N }}`** — a lane above the one being expanded, indexed from the OUTERMOST, so a level at depth *d* has indices `0..d-1` and `{{ ancestor (d-1) }}` is `{{ row }}`; reaching past a level's own ancestry is a register-time error. This is what makes a second level expressible when its lane key is unique only *within* its grandparent: 0.21.2 had to state that `{{ row }}` bound the immediate parent and nothing above it, so a level keyed on a step name matched steps of that name in every pipeline — rows that belong to something else, aligned on the right time axis. A depth-2 query now restates the scope it hangs under (`WHERE run_name = {{ ancestor 0 }} AND activity_name = {{ row }}`). New widget key **`has_children:`** on `kind: timeline` (and on `expand:`), naming a column that says whether THIS lane has sub-lanes: read per row and OR'd across the lane (so it survives a `{{ bucket }}` fold), with NULL / absent / `false` / `0` reading as NO. 0.21.2 also had to state that a chevron was offered per LEVEL, so every lane got one and the childless ones opened an empty note — on an estate whose history predates the underlying edge that is every lane on the wall. Declaring `has_children` on a level with no `expand:` is a register-time error. Additive: both are opt-in, a document naming neither is byte-for-byte unchanged, and `{{ row }}` keeps its meaning exactly. (expand-ancestry) |
| 0.22.1 | 2026-08-12 | PATCH (doc-only, no schema change): WHICH errors can actually stop a registration (S-2188 review). §8.5 said an `{{ ancestor N }}` past its level was a "register-time error", and §6.2 said the same of `{{ bucket }}` on a widget with no fold. Both are MACRO errors, and macro errors are not on the register path — they are caught by the declare-time lint over a spec's queries (`ValidateSpecQueries`), which a solution's own suite runs, and which S-2176 deliberately keeps OFF the admission path so a macro slip cannot take a whole dashboard (or a whole announced batch) off an estate. For an in-tree solution the distinction is invisible: the suite is the gate. For an external partner it is the whole story — skip the lint and the document registers cleanly, the tile renders, and the sub-lane fails at the click. §9 now states the rule once, where a reader looks for it: only the SCHEMA checks reject a registration. `has_children` on a level with no `expand:`, `unit: auto` + `format:`, and the rest of §9's numbered checks are schema, and genuinely do reject. No contract change: the same documents are valid, the same errors are raised, in the same places they always were — the doc now says where. (enforcement-points) |
| 0.23.0 | 2026-08-12 | Additive (S-2183 / S-2186): what the un-drilled WIDE view promises. **The lane cap belongs to the widget.** `limit` truncates the lanes the query returned and reports the rest as `+N more lanes`; a query that ALSO caps lanes makes that count structurally zero, so the truncation is real and silent — measured on a live estate, 320 of 360 lanes vanished from an un-drilled wall with nothing on screen saying so. Rank in SQL, truncate in the widget. The cost of over-fetching is usually small because a ranking's dropped lanes are the sparse ones (measured, 30-day window: a run wall 4,124 → 4,564 folded rows, +11%; an estate-wide step wall 4,119 → 13,662, 3.3×). New widget key **`lane_order: appearance | time`** on `kind: timeline` (and on `expand:`), because the cut and the layout are two questions: the cut is always the query's row order, and `lane_order` orders only the SURVIVORS — it never changes which lanes survive. Without it a page that wants the failing steps kept AND the steps read in execution order has to give one of them up. Default `appearance` = the previous behaviour. **Bar width is the true duration**, floored in CSS pixels at render rather than server-side as a percentage of an assumed track width — the old floor grew with the viewport (2px intended, 4.6px at 1512, ~11px at 2560) and at a day-wide window flattened every duration under ~4.8 minutes to one width, so a 0-second skipped step and a 1-minute step were pixel-identical and the long pole was unreadable. Presentation only: no query changes, and a zero-length mark stays visible and hittable. Additive: `lane_order` is opt-in and defaults to today; a document that also caps lanes in SQL keeps working exactly as before, silently, which is the thing to go and fix. (honest-overview) |
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

**Macros expand before the SQL is parsed, so a macro named inside a `--` comment
is still a call** — write the macro's *name* there, not its call, or a comment
explaining `{{ filter }}` becomes an argument-less invocation of it and fails to
render.

### 6.2 Macro vocabulary

Macros are **portable intents**; each dialect expands them into its own idiom.
The *names* are the contract; the *expansions* are dialect-local.

**Time macros** — resolve against the widget's computed Frame:

| Macro | Intent | SQL expansion | Metrics expansion |
|---|---|---|---|
| `{{ from }}` / `{{ to }}` | Frame bounds as a literal | `TIMESTAMP '2026-04-28 00:00:00'` | RFC3339 / unix (rarely used in-string) |
| `{{ timeFilter "col" }}` | Restrict to the frame — rows whose **instant** is in it | `col >= <from> AND col < <to>` (or `TRUE` if unbounded) | *no-op* (`TRUE`); frame rides `opts` |
| `{{ spanFilter "start" "end" }}` | Restrict to the frame — rows whose **interval** overlaps it | `start < <to> AND (end IS NULL OR end >= <from>)` (or `TRUE` if unbounded) | n/a |
| `{{ anchor "rel" "col" }}` | The as-of snapshot timestamp | `(SELECT MAX(col) FROM rel WHERE col >= <from> AND col < <to>)` | instant-at-`to` semantics |
| `{{ interval }}` | Bucket width | `INTERVAL '1 day'` | step (`1d`) |
| `{{ bucket "col" }}` | Group on **the widget's own resolved bucket** | `time_bucket(INTERVAL '14400 seconds', col)` / `date_trunc('day', col)` | n/a |
| `{{ worstStatus "col" }}` | Fold a group to its worst canonical status | `arg_max(col, CASE UPPER(TRIM(col)) WHEN 'FAIL' THEN 5 … END)` | n/a |
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

**Mark macros** — resolve against the timeline mark a sub-query hangs off
(§8.5); each is a hard error outside the block that binds it:

| Macro | Intent | Bound in | SQL expansion |
|---|---|---|---|
| `{{ key }}` | The hovered bar's identity | `card.query` | `'run-42'` |
| `{{ row }}` | The lane being expanded | `expand.query` | `'EMEA_Pipeline'` |
| `{{ ancestor N }}` | A lane ABOVE the one being expanded, indexed from the outermost (v0.22.0) | `expand.query` | `'EMEA_Pipeline'` |

All three expand to sanitized string literals — a mark value is DATA (a run id,
a lane label), escaped exactly as a `value_type: string` variable is.

#### Instants vs intervals — pick the right window macro (v0.20.0)

**Window on an INSTANT for event tables, on the INTERVAL for span tables.** A row
that carries one timestamp (a scrape, a login, a recorded KPI) is an event, and
`{{ timeFilter "col" }}` is right for it. A row that carries a start *and* an end
(a pipeline run, an activity, a job) is an interval, and asking "did it START in
the window" is not the same question as "is it in view".

**A timeline over `timeFilter(start)` loses every run that outlives the window
edge.** Zoom into the middle of a four-hour run and no run started inside the
window, so nothing comes back and the panel says "no runs in this range" — while
the run being looked at spans the entire screen. Every in-flight run hits this
the moment the window moves past its start. Use `{{ spanFilter "start" "end" }}`
for anything a `kind: timeline` draws, and for the CTE that selects which lanes
to show — a run whose middle is on screen must still earn its lane.

`spanFilter` treats a **NULL end as still-running**, so an unfinished row
overlaps every window after its start rather than vanishing into SQL's
three-valued logic. It is half-open at the right (`start < to`) and closed at the
left (`end >= from`): a row ending exactly as the window opens overlaps it, and a
row starting exactly as the window closes belongs to the next one.

Widgets that draw spans clamp a straddling row to the window edges, so an
overlapping row renders as a bar that runs off the side rather than a
mispositioned one.

#### Folding in SQL — `bucket` / `worstStatus` (v0.21.0)

A `kind: heatmap` and a `kind: timeline` both **collapse** their result set before
they draw it: the heatmap folds every row landing in a (bucket, lane) cell to the
worst status, the timeline merges same-lane bars that start within a few pixels
of each other. The drawn marks are therefore bounded — but the ROWS are not, and
the platform materialises every one of them before a widget sees it. A 30-day run
history shipped 155,735 rows to draw 5,028 bars, and paid it again on every window
and variable change.

These two macros let the **query** do that fold, so the rows never leave the
engine:

```sql
SELECT {{ bucket "at_ts" }} AS t,
       activity_name,
       {{ worstStatus "grade" }} AS status
FROM graded
GROUP BY 1, 2
```

- **`{{ bucket "col" }}` expands to the resolution the WIDGET resolved to for the
  current window** — a heatmap's resolved column-axis unit (§8.3), a timeline's
  density grid (§8.5). That is why it is a macro and not something you write:
  the resolution follows the picker (a 24 h selection buckets far finer than a
  30-day one), so a constant in the query would break every sub-daily preset.
- **`{{ worstStatus "col" }}` expands to the widget's OWN severity ranking**
  (`fail > warn > unknown > running/info > ok/queued/cancelled`, §8.5) as an
  aggregate. Use it rather than hand-writing a `CASE`: a second copy of the
  canonical vocabulary in your query is free to drift from the widget's, and a
  folded wall would then disagree with an unfolded one for a reason no reader
  could see. It also **normalises an absent status exactly as the widget does**
  (NULL / blank / whitespace → `unknown`, §8.5) — which a hand-written `arg_max`
  will not, because `arg_max` silently drops a row whose value argument is NULL
  before it ranks anything, so an ungraded row would lose every fold it entered.
- **Both are opt-in.** A query naming neither is unaffected, and the widget still
  runs its own fold afterwards either way — re-bucketing an already-bucketed
  timestamp is idempotent, so the WIDGET remains the authority on what the wall
  looks like. A heatmap's fold is **exact** (the SQL grid *is* the axis's grid);
  a timeline's is **close but not identical** — the marks shift slightly, which
  is what §8.5 is about, and why a folded timeline should declare `count:`.
- **Outside a folding widget they are an error, not a silent expansion.** A
  metric card, a table, or a heatmap whose column axis is `type: category` has no
  resolved bucket, and so does a timeline with no bounded window. `{{ bucket }}`
  says so rather than grouping on something invented — as a macro error, on the
  same footing as `{{ ancestor }}` above: the declare-time lint catches it, the
  register path does not (§9).
- **Both take a BARE column identifier**, like every other column-taking macro
  (§6.4). Alias the value you are folding to something other than the output
  column name — `{{ bucket "at_ts" }} AS t` reads cleanly, `{{ bucket "t" }} AS t`
  reads as a self-reference to the engine.

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

**Mark macros** — resolve against the timeline sub-query's *mark* (§8.5):

| Macro | Intent | Bound in | Expansion | Elsewhere |
|---|---|---|---|---|
| `{{ key }}` | The hovered bar's identity | a timeline `card.query` | `'run-42'` (quoted literal) | *error* |
| `{{ row }}` | The expanded lane's key | a timeline `expand.query` | `'nightly-load'` (quoted literal) | *error* |

Both take no arguments and both render the value as a **string literal**, escaped
the same way a `value_type: string` variable is (quote-doubling) — a mark value is
data (a run id, a pipeline name), never an identifier. Outside the sub-query that
binds it a mark macro is a **hard error**, not an empty expansion: a query written
against a mark that isn't there would otherwise silently match nothing.

Sub-queries are ordinary dashboard queries in every other respect — they run
through the same dialect, the same resolved frame and the same variables as the
widget's own query, so `{{ timeFilter }}` and `{{ filter }}` work in them and a
card obeys the picker window without the author repeating it.

### 6.3 Reserved names

The macro names above (including `{{ search }}`, `{{ key }}`, `{{ row }}`,
`{{ bucket }}` and `{{ worstStatus }}`), plus the document keys
`dsl_version`, `id`, `title`, `header`, `default_source`, `variables`, `rows`,
and the widget keys `kind`, `span`, `source`, `frame`, `lookback`, `refresh`,
`searchable`, `search_columns`, `page_size`, `flat`, `drilldown`, `col`, `row`,
`value`, `start`, `end`, `key`, `count`, `card`, `expand`. New names enter via MINOR bumps.

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
  start: start_time        # timeline — bar start timestamp column (required)
  end: end_time            # timeline — bar end column; NULL → in-flight
  key: external_run_id     # timeline — per-bar identity; bound to {{ key }} in card.query
  card: { query: "…" }     # timeline — declared hover-card detail query
  expand: { row: …, start: …, value: …, query: "…" }   # timeline — nested sub-lane axis
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
| `diagram` | edge rows → mermaid flowchart | Columns by name — `from`/`to` (required), `edge_label`, `status` (§ changelog 0.17.0). Server-rendered SVG; `direction` + `limit` presentation fields; overflow → foot note. |
| `timeline` | lane × time Gantt | Columns by name — `row` (the lane axis, categorical), `start` (required), `end` (optional; NULL → in-flight), `value` (canonical status), `key` (per-bar identity). Optional `card:` / `expand:` blocks (§8.5); reuses `limit` (lane cap) + `drilldown` (`{row}`). One row per BAR — reruns of the same entity share a lane. |
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
  `day` | `week` | `month` | `quarter` | `year` | `auto`), labelled per the
  resolved resolution (below), and sorted chronologically:

  ```yaml
  col: { field: dt, type: time, unit: day, format: "01-02" }
  ```

- **Labels follow the resolved unit (0.19.0).** The axis prints the smallest
  unit that still disambiguates, so a label is readable at a glance instead of
  repeating what never changes:

  | resolved bucket | window | axis reads |
  |---|---|---|
  | sub-day | inside one calendar day | `21:00 21:20 21:40 …` (clock only — the date is constant) |
  | sub-day | crosses midnight | `… 22:00 23:00 08-11 01:00 …` (each day's first bucket carries the date) |
  | `day` | any | `07-30 07-31 08-01 …` |
  | `week` / `month` / `quarter` / `year` | any | unchanged (`W02 2026`, `Jan 2026`, `Q1 2026`, `2026`) |

  **Cell titles always carry the full stamp** (`01-02 15:04`) whatever the axis
  abbreviates to — the hover exists to disambiguate.

- **`format:` (a Go time layout, e.g. `"01-02"` / `"Jan 2006"`) overrides the
  label layout for an EXPLICIT unit.** `quarter` / `week` always use their
  built-in formatter (Go has no quarter or ISO-week verb).
  **`format:` with `unit: auto` is a validation error (0.19.0)** — see below.

- **A time COLUMN axis is frame-driven (0.15.0)**: its buckets are generated
  from the resolved time window, pre-seeded across the whole span — the grid
  always covers the period the user picked, and a bucket with no data renders
  as an **empty cell** rather than a silently missing column. (A time ROW
  axis, and any axis without a resolvable frame, stays data-derived as
  before.)
- **An empty cell and an ungraded cell are different things (0.21.1).** "No row
  landed in this bucket" renders **empty**. A bucket that DID receive rows whose
  `value` was NULL or blank renders `unknown` — the same neutral colour, but a
  real cell with a real hover, and it folds at the `unknown` severity (above
  `ok`, below `warn`; see §8.5). The distinction is deliberate and it is the one
  thing the wall must not blur: a bucket where something happened that nobody
  graded is not the same as a bucket where nothing happened, and rendering the
  first as the second would hide activity rather than merely fail to colour it.
- **`unit: auto` (0.15.0)** derives the bucket width from the window span,
  aiming at ~40 columns whatever range is picked: 24 h → 1 h buckets, 7 d →
  4 h, 30 d → a day, a year → weeks. The ladder is 1m/5m/15m/30m/1h/2h/4h/
  6h/12h then day/week/month — the narrowest width keeping the window at or
  under ~44 columns. Without a frame, `auto` falls back to `day`.
  **Wide windows: fold in SQL with `{{ bucket }}` (0.21.0).** The wall is ~44
  columns however long the window is, but a query emitting one row per event
  ships every one of them to draw them. `GROUP BY {{ bucket "col" }}, <row>` with
  `{{ worstStatus }}` folds to one row per CELL instead, on the axis's own grid —
  so the result is identical, seeding, labels and thinning included, and the fold
  is **exact** rather than approximate (§6.2). Measured on a real 30-day estate,
  an activity wall went from 414,792 rows to 7,002.
  **`auto` may not be combined with `format:` (0.19.0)** — the two contradict:
  auto resolves a width that CHANGES with the window and the labels follow it,
  while `format:` pins one layout for every width. Pinning `format: "01-02"`
  under auto is what produced an axis of a dozen identical dates. Declare an
  explicit unit if you need to name a layout. Rejected at register, loudly.
- **Duplicate cells keep the WORST status (0.15.0)** — severity order
  `fail/error/critical/high` > `warn/warning/elevated` > *unknown* >
  `inconclusive/info` > `pass/ok/healthy`. This replaces the previously
  *unspecified* last-write-wins: a query MAY now simply emit one graded row
  per source point (e.g. per hourly KPI round) and let the widget fold them —
  the wall exists to surface the bad hour, and a later ok must not paint over
  it. Pre-aggregating in SQL remains valid.
- **Tick thinning prefers natural boundaries (0.19.0).** Past ~16 columns the
  axis shows roughly 12 labels, and WHICH 12 is chosen by rank, not by
  arithmetic: day boundaries first (they are the only labels that say which day
  a column belongs to), then the resolution's round instants (top of the hour
  for sub-hour buckets, midnight for hour-and-up, Monday / the 1st for day
  buckets), then a positional fill for whatever gaps remain. A uniform minimum
  spacing applies to all three, so labels never collide — ranking decides which
  survive, spacing decides how many. Cell tooltips keep the exact bucket.
- Rendering notes: the heatmap is a server-rendered grid and refreshes wholesale
  on window change (it is exempt from the chart-canvas morph preservation that
  protects uPlot widgets).

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

### 8.5 Timeline (`row` / `start` / `end` / `value` / `key`, `card`, `expand`)

A `kind: timeline` projects **one row → one bar**: lanes down the left (one per
distinct `row` value), a continuous time axis across, and a true bar per result
row spanning `start`→`end`, colored by `value`. It expresses what a bucket
heatmap structurally cannot — **duration, overlap and cadence**.

The widget is **system-blind**: it knows lanes, bars, statuses and a window.
Mapping a system's raw statuses onto the canonical vocabulary is the **query's**
job, exactly as for the heatmap — which is what lets the same widget serve ADF
runs today and Databricks jobs, webMethods flows or Kafka consumer lag next.

> **Window the query with `{{ spanFilter "start" "end" }}`, not
> `{{ timeFilter "start" }}`** (§6.2, v0.20.0). A bar is an interval: filtering on
> the start alone drops every row that began before the window, so zooming inside
> a long run — or past the start of an in-flight one — empties a panel that should
> be showing exactly that run. This applies to the lane-selection CTE too.

```yaml
- id: run-timeline
  kind: timeline
  span: 12
  row: run_name             # lane axis (categorical — time is the bar track, not the lane axis)
  start: start_time         # bar start (timestamp column)
  end: end_time             # bar end; NULL → in-flight (bar runs to now, status color kept)
  value: status             # canonical status — map raw values in SQL
  key: external_run_id      # per-bar identity; the {{ key }} the card query receives
  count: runs               # optional — bars this row already stands for (see "folding in SQL")
  lane_order: time          # optional — appearance (default) | time; orders the SURVIVING lanes
  has_children: launched    # optional — per-lane chevron: only lanes whose rows say yes expand
  frame: window
  limit: 200                # optional lane cap; overflow reported in the tile foot
  drilldown: { target: adf.pipeline, params: { pipeline: "{row}" } }
  card:                     # optional — solution-declared hover detail
    query: |
      SELECT status, start_time, end_time, duration_ms, trigger_name
      FROM runs WHERE external_run_id = {{ key }}
  expand:                   # optional — a FULL nested axis declaration
    row: activity_name
    start: activity_start
    end: activity_end
    value: status
    key: activity_run_id
    query: |
      SELECT … FROM activities a JOIN runs r USING (run_id)
      WHERE r.run_name = {{ row }} AND {{ spanFilter "activity_start" "activity_end" }}
    # expand: may nest again — an ExecutePipeline activity opens its child's runs.
    # {{ row }} binds the lane being expanded; {{ ancestor N }} the lanes above
    # it, so a deeper level can restate its parent's scope. See "Nesting" below.
  source: { store: ops, query: "SELECT … WHERE {{ spanFilter \"start_time\" \"end_time\" }}" }
```

**Canonical status vocabulary.** `ok` / `warn` / `fail` / `running` / `queued` /
`cancelled`. Map in SQL — `CASE status WHEN 'Succeeded' THEN 'ok' WHEN 'InProgress'
THEN 'running' … END` — the heatmap precedent. Unrecognised values still render
(with the neutral color and their own legend entry) rather than being hidden: the
widget shows what the query gave it. Severity for "worst of a merged segment" is
`fail > warn > unknown > running/info > ok/queued/cancelled`.

**An absent status is `unknown`, and `unknown` outranks `ok` (0.21.1).** NULL,
empty and whitespace all mean the same thing — the row exists, and nothing graded
it — and they fold as `unknown`: above `ok`, `queued`, `cancelled` and `running`,
below `warn` and `fail`. So a merged segment holding eleven `ok` runs and one
ungraded one reads `unknown` rather than `ok`, and one holding a `fail` still
reads `fail`. That follows the rule the whole severity order exists for: an
unknown must be **visible, and must never be able to mask a failure**.

If you want an ungraded row to read as something specific, grade it in SQL — a
`CASE` with a total `ELSE` is the contract (`ELSE 'warn'` is what the ADF
timeline uses, precisely so a status Azure adds later stays visible). Leaving
NULLs to the widget is not a way of saying "ignore these".

- **`row` is categorical by construction.** Declaring `type: time` on it is a
  validation error — the continuous time dimension is the bar track.
- **`end` is optional; `key` is required exactly when you declare a `card:`.**
  A NULL / unparseable end means **in-flight**: the bar extends to now (never
  past the window, never into the future) and keeps its status color. Omitting
  the `end:` column entirely renders every row in-flight (the point-event
  degenerate case). `key` is what `{{ key }}` binds, so a card without one has
  nothing to look up — and because a **sub-lane** bar opens the same card a
  top-level bar does, a card-bearing timeline needs `key` **at every `expand:`
  level too**. The validator enforces all of this at register; without the
  check the failure is silent (sub-lane bars fall back to the plain hover title
  and the declared card is simply never seen).
- **Reruns share a lane.** Two runs of the same entity, disjoint in time, sit
  side by side on one lane. A row whose `start` cannot be parsed is dropped —
  inventing a start would draw a run that never happened.
- **The window is the picker window**, not the data extent: gaps stay visible.
  Bars straddling an edge are clamped; bars wholly outside are not rendered.
- **Bar width is the DURATION (0.23.0).** A bar's width is the true span of what
  it stands for, at every zoom, and the "a very short run must stay visible"
  floor is applied in CSS pixels at render — so it is a floor of a couple of
  pixels whatever the screen, and the widths above it still order by duration.
  (It was previously floored server-side as a percentage of an assumed track
  width, which made the floor grow with the viewport and, at a day-wide window,
  rendered every duration under a few minutes at one identical width. "Which
  lane is the long pole" was unanswerable at exactly the zoom that asks it.) A
  zero-length mark — a skipped step — is honestly zero and still hittable.
- **Density budget.** Bar geometry is computed as a percentage of the window,
  and the projection merges same-lane runs that **start** within a few pixels of
  each other — adjacent or **overlapping** — into one segment, colored by the
  **worst** status and titled `"N runs · worst: fail"`. Because the rule bounds
  segment *starts*, the node count per lane is capped whatever the run durations
  do: a lane of long, heavily-overlapping runs (the concurrent-rerun shape) is
  bounded exactly like a lane of short sequential ones. This happens
  **server-side**, so a dense window never serialises thousands of nodes. A
  merged segment spans from its earliest start to its latest end, carries no
  `key` (N runs have no single identity) and therefore no card query. A very
  short run is floored at a visible minimum width rather than vanishing.
- **Folding the density budget into SQL (0.21.0).** The merged marks are bounded;
  the rows behind them are not. `GROUP BY {{ bucket "start_time" }}, <lane>` folds
  a lane's runs onto the widget's own density grid before they leave the engine —
  measured, a 30-day run history went from 155,735 rows to 17,239 while drawing
  4,773 of the 5,028 marks it drew before. Unlike the heatmap's, this fold is
  **approximate**: the widget merges greedily and a grid can only approximate
  that, so `{{ bucket }}` deliberately resolves to a grid well under the merge
  threshold and leaves the widget to finish the job. Three things your `GROUP BY`
  must preserve, because the widget cannot recover them:
  - **`count:`** — how many bars the row stands for. Without it a segment holding
    twelve runs hovers as "1 run". Counts SUM through the widget's own merge, and
    a mark standing for more than one drops its `key` and its card exactly as a
    widget-merged segment does. Absent / NULL / `< 1` reads as **1**, so a query
    that does not fold is unchanged.
  - **In-flight.** `MAX(end)` ignores NULLs, so a bucket holding a finished run
    and a running one comes back finished and the bar stops dead mid-flight. Emit
    NULL for the group when any member is unfinished.
  - **Identity.** Emit the `key` only when the group holds exactly one row; a
    group of several has no single identity, and saying so with a NULL is what
    keeps the hover card honest.
- **Vertical budget.** Lanes have a minimum height and the widget body a maximum;
  many lanes scroll (the time axis stays visible) rather than shrinking lanes to
  fit. `limit` caps the lane count and any drop is reported, never silent.
- **The lane cap belongs to the WIDGET, not to your SQL (0.23.0).** `limit`
  truncates the lanes the query returned and reports the rest as `+N more
  lanes` in the tile foot. So **do not also cap lanes in the query**: a
  `LIMIT 40` under a `limit: 40` means the widget receives exactly 40, computes
  no overflow, and the foot marker never renders — the truncation is real and
  silent, which is precisely what the marker exists to prevent. Rank in SQL,
  truncate in the widget:

  ```sql
  -- ranks, does NOT cut: the widget's limit: is the cut
  top AS (SELECT lane, COUNT(*) FILTER (WHERE status='fail') AS fails,
                 COUNT(*) AS n FROM src GROUP BY lane)
  …
  ORDER BY lane_fails DESC, lane_n DESC, lane, bar_start
  ```

  The cost is real and worth sizing: every lane in the window reaches the
  platform. It is usually small, because the lanes a ranking drops are by
  construction the sparse ones — measured on a live ADF estate at a 30-day
  window, a run wall went 4,124 → 4,564 folded rows (+11%) for 74 lanes, and an
  estate-wide step wall 4,119 → 13,662 (3.3×) for 321. Fold with `{{ bucket }}`
  (above) and that is the shape of the bill; without the fold it is the raw row
  count, and the trade may not be worth it.
- **`lane_order:` — which lanes survive, and how they READ, are two questions
  (0.23.0).** The cut is always the query's **row order**, so an `ORDER BY` that
  ranks is what keeps the lanes worth keeping. `lane_order` then lays the
  SURVIVORS out:

  | value | lane order |
  |---|---|
  | `appearance` (default) | the query's row order — ranking and layout are the same thing |
  | `time` | earliest bar first, so the wall reads as a **waterfall** |

  It **never changes which lanes survive**. That separation is the point: a step
  timeline wants to keep the failing and busy steps *and* read in execution
  order, and one `ORDER BY` cannot say both — cut chronologically and the
  failures quietly drop out of the overview; lay out by rank and the steps stop
  reading as a sequence. An estate wall that wants its lanes read in the order
  it wants them kept needs no `lane_order` at all.
- **`card:`** is a declared query, not a registered handler — a solution stays
  pure YAML. Its FIRST row renders (a query returning several rows for one key
  is an authoring bug the card will not paper over); without a `card:` a bar
  still carries a plain hover title. The card renderer is **generic**, because
  it cannot know which columns a solution will select:
  - **The card's heading is the bar's `key` VALUE** — the run id, not a name
    column. If you want a human-readable heading, select the key column as
    something readable, or read the name off a labelled row in the body. (When
    a bar has no key at all, the heading falls back to the status.)
  - Columns are classified by **SUFFIX**, not by exact name, so a solution's own
    naming lands in the right slot: `status` / `*_status` → a status pill;
    `start` / `start_time` / `*_start` / `*_start_time` → the span line's left
    end; the matching `end` forms → its right end; `duration_ms` /
    `*_duration_ms` → a formatted duration. Everything unrecognised falls
    through as a labelled key→value row, in result-column order.
  - **Where two columns match the same role, the LAST one in result-column
    order wins** (for the start/end roles, the last one that parses as a
    timestamp; a non-parsing candidate degrades to a plain labelled row rather
    than being swallowed). A query selecting several time columns should
    therefore put the pair it wants on the span line last.
  - Duration is **derived from the span** when no duration column is present, so
    a card never shows a start and an end while leaving the reader to subtract.
- **`expand:`** is a full axis in its own right — `row` / `start` / `value`
  required, `end` optional, `key` governed by the same rule as the parent's
  (required iff the widget declares a `card:`), `count` / `has_children`
  optional — plus its own `query`, bound to the opened lane via `{{ row }}` and
  to everything above it via `{{ ancestor N }}`. Sub-lanes ARE lanes: same bar
  maths, same window, same density budget, same card mechanics. Nesting is
  bounded (5 levels).

**Nesting: an expand level receives its WHOLE ancestry (0.22.0).** `{{ row }}` is
the lane being expanded — the immediate parent — and `{{ ancestor N }}` is any
lane above it, indexed from the **outermost**. A level at depth *d* has *d*
ancestors, so its valid indices are `0 .. d-1`, and `{{ ancestor (d-1) }}` is
`{{ row }}` itself:

```yaml
expand:                       # level 1 — lanes are steps of the opened pipeline
  row: activity_name
  query: |
    … WHERE r.run_name = {{ row }}
  expand:                     # level 2 — lanes are the runs that step launched
    row: run_name
    query: |
      …
      WHERE r.run_name      = {{ ancestor 0 }}   -- the pipeline, two levels up
        AND a.activity_name = {{ row }}          -- the step directly above
```

Reaching past your own ancestry (`{{ ancestor 1 }}` at depth 1, or any
`{{ ancestor }}` outside an `expand:`) is an error, not an empty literal — but
**a MACRO error, which is not on the register path** (§9). It is caught when you
build your solution, by the declare-time lint that renders every query in the
spec; the platform does not reject the registration. Ship one anyway and the
dashboard registers, the tile renders, and the sub-lane fails at the click — so
run that lint in your own suite, the way an in-tree solution does. Contrast
`has_children` below, which the schema validator rejects at register.

**This changes what a second level can mean** — before 0.22.0 a level received
one lane key and nothing above it, and the rule below was a hard constraint
rather than a caution:

- **A level's scope must equal its parent lane's.** If level 1's lanes are step
  names and step names are only unique *within* a pipeline, a level-2 query
  keyed on the step name alone matches steps of that name in *every* pipeline.
  It returns rows, aligned on the right time axis, and is silently wrong. The
  chain is how you restate the scope you are hanging under; use it whenever your
  lane key is not unique on its own.
- **Page scope also counts as identity.** A dashboard already scoped to one
  entity by a variable (`{{ filter "run_name" "pipeline" }}` on a per-pipeline
  page) gives the sub-query the same context, and both together are ordinary.
- **When neither is enough, don't ship the level.** Prefer no chevron to a
  chevron over rows that belong to something else.

**`has_children:` — a chevron per LANE, not per level (0.22.0).** By default a
level's `expand:` gives *every* lane at that level a chevron, including lanes
whose sub-query returns nothing; opening one of those shows an inline note.
Declare `has_children:` (a column name) on the level and the widget offers the
chevron only where the column says yes:

```yaml
row: activity_name
has_children: launched      # a boolean/count column on THIS level's query
expand: { … }
```

- The value is read per ROW and **OR'd across the lane** — one row saying yes is
  enough — so it survives a `{{ bucket }}` fold (`BOOL_OR(...)` in the group).
- **NULL / absent / `false` / `0` reads as NO.** That is deliberate: a lane the
  query said nothing about has said nothing worth drawing a chevron for. It is
  also the honest answer where the underlying edge is missing rather than
  negative — imported history that never carried it, for instance.
- Absent → every lane at the level is expandable, exactly as before, so no
  existing document changes.
- Declaring it on a level with no `expand:` is a **register-time error**.

Without it, "this step launched nothing" and "this step's history predates the
edge" both render as a chevron opening an empty note — on a wholly imported
estate that can be every lane on the wall.

Sub-query failures stay local: a card or expand query that errors renders a muted
inline note in the hover or lane list, never a failed tile (§9).

**Interactions** (platform-provided, nothing to declare): dragging horizontally
across a lane zooms the dashboard's time range to the dragged span (the same
per-tab absolute range the picker and calendar write, so every widget follows);
hovering a bar opens its card; the card offers **Drill down** (the declared
`drilldown`, current window) and **Drill down & zoom** (the same navigation with
the range narrowed to the bar's span, padded); a merged segment offers **Zoom to
segment**.

---

## 9. Validation & errors

A document is validated at load/register time (`infra/dashboard/validate.go`,
panic-at-register) and the template is linted at render time
(`dsl_template.go`). The split below notes which check runs where, and it is a
real distinction rather than bookkeeping: **only the SCHEMA checks can stop a
registration.** Everything about a query's MACROS — an unknown macro, a bad
column argument, an `{{ ancestor }}` past its level, a `{{ bucket }}` on a
widget with no fold — is caught by a declare-time lint over the spec's queries
(`ValidateSpecQueries`), which a solution's own test suite runs. That lint is
deliberately not an admission gate (S-2176: a refused spec takes the whole
dashboard, or the whole announced batch, off the estate, and one broken tile
beside nine working ones is strictly better). So a partner who skips it ships a
document that registers cleanly and fails at the gesture.

The checks:

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

7. **Timeline shape** *(load)* — a `kind: timeline` declares `row` (categorical
   — `type: time` on the lane axis is rejected), `start` and `value`; a `card:`
   declares both a `query` and a `key:`; an `expand:` is a full axis (`row` +
   `start` + `value` + `query`) at every nesting level, bounded at 5. Column
   *names* are checked for presence, not against the query's result set — a
   query is opaque to the validator, so a misspelt column degrades to an empty
   lane at render, as for every other kind. `has_children:` is rejected on a
   level with no `expand:` (0.22.0), and `lane_order:` against its closed
   vocabulary `appearance | time` (0.23.0) — a near-miss must not fall through
   as the default, since the field exists precisely because a page's layout
   deliberately is not its `ORDER BY`.
8. **Expand ancestry** *(declare)* — an `expand:` level naming
   `{{ ancestor N }}` past its own depth is an error, caught when the author
   builds their solution: each level is rendered against a chain of exactly its
   own depth, so the macro's range check *is* the rule (0.22.0). Like every
   other macro check this is a declare-time lint over the spec's queries, not an
   admission gate on the announce path.

Errors render as an inline error tile inside the cell; one failed widget never
kills the dashboard (per editor doc §11). A timeline **sub-query** (card /
expand) failure is narrower still: it renders a muted inline note in the hover
card or the sub-lane slot, leaving the tile itself intact.

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

- **`{{ bucket }}` flavor coverage** — shipped for the DuckDB family
  (`time_bucket` for a width, `date_trunc` for a calendar unit). Postgres
  (`date_bin`) needs a decision on the minimum-version baseline. (This question
  outlived `timeGroup`, the never-implemented macro `bucket` replaced in 0.21.0.)
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
