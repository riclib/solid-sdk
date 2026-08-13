<!--
  ┌──────────────────────────────────────────────────────────────────────┐
  │  Mechanic Definitions — Solution YAML Contract                         │
  ├──────────────────────────────────────────────────────────────────────┤
  │  Contract version : 0.1.0                                              │
  │  Status           : DRAFT — the grammar is enforced, the announce path  │
  │                     is Stage B+ (pre-1.0 minors may break)             │
  │  Stability         : unstable; the schema is the enforced subset (§12)  │
  │  Surface           : external — authored by solution authors (humans   │
  │                      and the LLM); third-party solutions write against │
  │                      this.                                             │
  │  Last updated      : 2026-08-14                                        │
  │  Owner ticket      : S-2212 (this doc) — grammar: S-1927 / S-1955 /    │
  │                      S-2008 / S-2205 / S-2208 / S-2209                 │
  │  Implements        : domains/workflow (ParseMechanicYAML) +            │
  │                      app/mechanic (the executor's own re-checks)       │
  │  Sibling           : ./workflow-defs.md — the v1 PURE-SKILL contract,  │
  │                      a different grammar with a different parser       │
  └──────────────────────────────────────────────────────────────────────┘
-->

# Mechanic Definitions — Solution YAML Contract

**Contract version 0.1.0 · Draft**

A **mechanic def** is a sequence of tracked steps over a **case**: one case per
subject row, opened when the lake projects that row, advanced tick by tick,
closed by a declared condition. Every step is recorded — what it read, what it
answered, which def version ruled — and **no agent is in the hot path**. That is
the whole difference from the sibling contract: [`workflow-defs.md`](./workflow-defs.md)
declares an anchored *conversation* that runs a skill; this declares *mechanism*.

> **Two grammars, two parsers, one leaf kind.** A mechanic def and a pure-skill
> def are not dialects of each other. They have separate parsers, separate
> stores, and share no keys beyond `id` and `description`. A pure-skill def
> carrying `steps:` refuses to load, and a mechanic def carrying `skill:`
> refuses to load. Which parser runs is decided by the announce leaf's `kind`
> (§2), never by sniffing the body.

---

## 1. What a mechanic def is — and is not

- It is **per subject row**. `activation.scope: case` — one case per row of the
  activating stream that passes the filter. There is no batch mode.
- It is **tracked**, not orchestrated. Steps run in declaration order; each
  writes a ledger row (ran, skipped, waiting, failed). A step that cannot run
  yet **parks** the case rather than spending an attempt.
- It has **no expression language.** Not CEL, not JS, not Go-template
  conditions. Every condition in the grammar is one of exactly five mechanical
  forms (§8), and that is a deliberate ceiling, not a gap waiting for a parser.
- It **declares what it reads** (§5). The platform builds the query surface
  *from* that declaration, so a stream the def did not declare has no name a
  query can spell.
- Its operational knobs live **outside** the def. Workers, enabled/disabled, and
  the values of declared `config:` knobs are separate documents (§9.1) — because
  the def's content hash is its version identity (§3.1), and turning a dial must
  not re-version the def.
- Its `goal:` block is **opaque to the platform**: carried verbatim, interpreted
  by nobody unless the owning solution interprets it (§13).

---

## 2. Delivery — the announce leaf

A mechanic def travels as the `Body` of a `WorkflowArtifact` leaf
(`<solution>.workflow.<id>` in the announce KV tree, see
[`../CLAUDE.md`](../CLAUDE.md) and `contract/workflow.go`):

```go
contract.WorkflowArtifact{
    ID:          "cdl-advisory.incident",
    Name:        "CDL advisory — per incident",
    Description: "Every incident that lands gets a case.",
    Source:      "cdl-advisory",
    Kind:        contract.WorkflowKindMechanic, // ← routes the body to this grammar
    Body:        string(defYAML),               // ← verbatim, opaque bytes
}
```

Three rules govern the leaf:

1. **`Kind` is the router.** `contract.WorkflowKindMechanic` selects this
   grammar; `contract.WorkflowKindSkill` (or an EMPTY `Kind`, the frozen
   pre-0.11.0 meaning) selects [`workflow-defs.md`](./workflow-defs.md). The
   platform never guesses from the body.
2. **The body's own `kind: mechanic` must agree** with the leaf's `Kind`. They
   are not redundant: the leaf field routes *before* parsing, the body key is
   what the parser requires. A mislabelled leaf therefore fails loudly at the
   parser rather than silently running the wrong grammar.
3. **The body is carried verbatim.** Nothing in the SDK parses and re-emits it:
   comments, key order, indentation, blank lines and the trailing newline all
   survive the wire byte for byte, because the content hash of those bytes is
   the def's version (§3.1). Re-serializing YAML would mint a phantom new
   version for an unchanged def.

The def id may contain dots (`cdl-advisory.incident`) — the leaf key absorbs
them.

---

## 3. The def header

```yaml
id: cdl-advisory.incident
kind: mechanic
display_name: CDL advisory — per incident
description: >
  Every incident that lands gets a case. The case rules the incident in or out
  of the team's domain, retrieves resolved precedents, and writes an advisory.
```

| Key | Required | Type | Meaning |
|---|---|---|---|
| `id` | **yes** | string | The def's identity, globally unique. No format rule — dots and dashes are legal. |
| `kind` | **yes** | string | Must be `mechanic`. A def that omits it does not load. |
| `display_name` | no | string | Human name. Parsed, never validated (unlike the v1 contract, where it is required). |
| `description` | no | string | Carried verbatim; documentation. |
| `activation` | **yes** | block | §5 — what opens a case, and what the def reads. |
| `steps` | **yes** | list | §6 — at least one; *a workflow that tracks nothing is not a workflow*. |
| `functions` | no | map | §7.1 — the prompt functions the function steps name. |
| `queries` | no | map | §7.2 — the declared SQL. |
| `notifications` | no | map | §7.4 — declared messages (a purpose, never a destination). |
| `deliveries` | no | map | §7.5 — declared outbound row shapes. |
| `goal` | no | map | §13 — **opaque**. |

### 3.1 Versioning — by content hash, not by a key

**There is no `version:` key**, and adding one would be a mistake the contract
deliberately avoids. A def's version is:

```
def_version = first 12 hex chars of SHA-256(the raw def body bytes)
```

computed with **no normalization** — comments and whitespace are part of the
identity. `contract.WorkflowArtifact.DefVersion()` computes exactly this, so a
def announced over the wire and the same bytes seeded in-tree carry the same
version.

Consequences worth internalising:

- **A case stamps the def version resolved by the sweep that OPENED it**, and
  keeps it for life. Later steps run against whatever the tracker holds. The
  stamp is a claim about the open, not about the whole case.
- Every invocation record carries `def_version` too, which is what makes an A/B
  between two def versions readable after the fact.
- Because the hash covers everything, **operational values must not live in the
  def** — that is why `config:` declares knobs here and the *values* live in a
  separate workspace document (§9.1).

---

## 4. Reserved keys — what will never load

These refuse **by name**, with an error explaining why, rather than being
silently ignored:

| Key | Where | Why it refuses |
|---|---|---|
| `stages:` `gates:` `skill:` | top level | They belong to the ADAPTIVE shape, which runs as an anchored conversation on the skill engine. A mechanic def has no agent in its hot path. |
| `skill:` | step | Adaptive work gets a goal, never a flowchart. |
| `runnable:` | step | The jobs executor is not wired into the tracker's executor registry yet. |
| `ready` inside `activation.on` | activation | Readiness moved to the steps (S-1927). |
| a third `retrieve.arms` entry, a second `fuse` | step | A grammar change, not a spelling one (§7.3). |
| `with:` beside `retrieve:` | step | `retrieve:` declares its own inputs; a second way to say it would be unchecked. |
| `with:` / `params:` / `config:` on an authority gate | step | An authority gate reads the ratification ledger, not data. |

Decoding is **strict** (`KnownFields`) everywhere, at every level: any key not
in this document fails the load. See §12.

---

## 5. `activation:` — what opens a case, and what the def may read

```yaml
activation:
  on: "projected(servicenow_inc.incidents)"
  reads: [servicenow_inc.incidents]
  scope: case
  filter:
    state: "100"
  subject:
    key: sys_id
    columns:
      sys_id:            {type: string, description: the incident's immutable record id}
      number:            {type: string, description: the human-facing INC number}
      short_description: {type: string, description: the one-line symptom}
      close_notes:       {type: string, description: "the engineer's close notes; absent until closure"}
```

| Key | Required | Rule |
|---|---|---|
| `on` | **yes** | Exactly one form: `projected(<tenant>)` or `projected(<tenant>.<stream>)`. It is a **frontier predicate over projected state** — the lake tick — not an event subscription. Nothing else parses. |
| `reads` | no | §5.1. Absent = the pre-declaration behaviour, unchanged. |
| `scope` | no | Only `case` (the default): one case per subject row. |
| `filter` | no | **Equality only**: `column: value`. No operators, no ranges, no OR. The column name is checked; the value is not validated at all. Ordered by column name so the generated SQL is deterministic. |
| `subject.key` | no | An identifier. When omitted the platform resolves it from the stream's derivation declaration, and errors at compile if it cannot. |
| `subject.columns` | **yes** | Non-empty. Names are identifiers; types are **scalars** (`string`, `int`, `float`, `bool`). This is what makes every `with: subject.…` binding checkable at load. |

### 5.1 `reads:` — the keystone rule

**The platform builds the query surface FROM this declaration.** One
gen-windowed view per declared stream. A stream the def did not declare has no
name a query can spell — so an undeclared read is not policed, it is
*unspellable*.

```yaml
reads:
  - servicenow_inc.incidents          # <tenant>.<stream>
  - kpis                              # bare <stream> ⇒ the activation's tenant
  - {stream: adf_ops.runs, wait: false}   # windowed, but not waited on
```

- The **trigger stream is implicit** and always in the set; naming it again is a
  no-op.
- The same declaration compiles into the **wait**, so wait and window cannot
  drift.
- `reads:` is **opt-in**. A def without it keeps the older surface
  (`{{.Schema}}` / `{{.LatestSchema}}`, unwindowed) exactly as before — there is
  no flag day. A def *with* it has **no `{{.Schema}}`**, deliberately, so
  reaching for the old surface fails at load.

Refusals:

| Condition | Why |
|---|---|
| `reads:` with the short activation form `projected(<tenant>)` | The trigger stream must be in the read vocabulary at LOAD time; a surface that is one set at load and another at run is not a closed vocabulary. |
| two entries sharing a bare stream name across tenants | The alias is the whole vocabulary `{{.Read.<alias>}}` gets; a collision would make the def's meaning depend on declaration order. |
| `wait: false` on the trigger stream | The trigger gen IS that stream's wait; there is nothing to relax. |

**Enforcement runs three times, and only the last one is authoritative.** (1) At
load, an undeclared stream can only be reached through `{{.Read.<undeclared>}}`
or `{{.Schema}}`, and both fail the template dry-run. (2) At load, a belt check
requires `ready: [fts]` from any query that touches the FTS index (§8.5). (3) At
run, the platform re-parses the **rendered** SQL with DuckDB's own parser and
requires every base table to be a declared read view — and refuses every table
function by class, so `read_parquet(…)` cannot smuggle an undeclared read past
it.

---

## 6. Steps

```yaml
steps:
  - id: precedents
    retrieve: {…}
    ready: [embeddings, fts]
    config: {…}
```

Every step declares **exactly one executor** — `function:`, `query:`,
`retrieve:`, `notify:`, `deliver:`, or a `gate:` block of its own. Zero
executors and two executors are both load errors.

| Key | Applies to | Meaning |
|---|---|---|
| `id` | all | **Required.** `[A-Za-z][A-Za-z0-9_]*`, unique. It is the ledger key and the name later steps bind to. |
| `enabled` | all | Default true. `false` = DORMANT: still parsed, bound and type-checked, never invoked (the tracker writes a `skipped` row). |
| `with` | all but authority gates | §7.6 — the bindings. |
| `params` | query, data gate | Fixed values for the query's declared params. |
| `config` | function, query, data gate | §9 — the tunable knobs. |
| `close_when` | function | §8.2. |
| `precheck` / `on_error` | function | §8.3 / §8.4. |
| `gate` | any (authority) / its own step (data) | §8.1. |
| `ready` | all | Derivation identifiers this step waits for. Shape-checked here; the *names* are checked against the stream's declared derivations at compile. An unmet readiness **parks** the case (no attempt spent). |
| `terminal` | all | Closed set: `scored`. The others — advised, ruled_out, expired, failed — are things that *happen* to a case; a def declaring one would be declaring an outcome rather than a step. |
| `promote` | query, deliver | `<tenant>.<stream>`, both plain identifiers. Lands the step's rows back into the lake. |

### 6.1 Step outputs — what a later step can bind

| Step kind | Outputs |
|---|---|
| `function` | `.output` — always a string |
| `query` (incl. a compiled `retrieve:`) | `.rows` (typed by the query's `returns:`), `.count` (int) |
| `deliver` | `.rows` (the declared columns **plus** `outcome`, `outcome_detail`), `.count` (int) |
| `notify` | `.outcome` (one of `delivered`, `no_route`, `denied`, `failed`), `.endpoints` |
| `gate` | **nothing.** A gate passes, waits, or expires. Binding one is a load error — what a closure gate lets a later step SEE is the subject's newer row: bind `subject.latest.<column>`. |

---

## 7. The five executors

### 7.1 `function:` — a prompt function with a closed output contract

```yaml
functions:
  domain_check:
    description: …
    tier: mini
    inputs:
      IncludedDomains:  {type: strings, description: the in-domain keywords}
      ShortDescription: {type: string,  description: the one-line symptom}
    output:
      kind: enum
      allowed: [IN_SCOPE, OUT_OF_SCOPE]
    template: |
      Decide whether this incident is in the team's domain.
      Symptom: {{.ShortDescription}}
      In-domain keywords: {{join .IncludedDomains ", "}}
      Answer with exactly one of: IN_SCOPE, OUT_OF_SCOPE
```

Input types (closed): `string`, `int`, `float`, `bool`, `strings`, `object`
(needs `fields:`), `rows` (needs `columns:`, each a scalar). Names are
`[A-Za-z_][A-Za-z0-9_]*` and are **case-sensitive template keys**.

Output contracts, both enforced at declare time:

| `kind` | Required | Rule |
|---|---|---|
| `enum` | `allowed:` | Non-empty; every token one word (whitespace refuses); unique case-insensitively. *An enum with no tokens accepts nothing.* |
| `text` | `max_chars:` | Required and positive — *an unbounded text contract is not a contract*. `min_chars:` optional, default 1. |

The template is Go `text/template` with `missingkey=error`, a curated FuncMap
(`join`, `truncate` — nothing else), and it is **dry-run twice at load** (a full
sample and a zero sample). Known and documented gap: a *value* conditional
(`{{if eq .X "y"}}`) is reached by neither sample.

### 7.2 `query:` — the raw-SQL escape hatch

When the declared shapes do not fit, write the SQL. It is a first-class part of
the grammar, not a back door — but it is a *declared* query: inputs, params and
returned columns are typed, and the type check happens at load.

```yaml
queries:
  incident_closed:
    description: Has this incident closed with a note?
    inputs:  {SubjectKey: {type: string, description: the case subject's row identity}}
    params:  {lookback_days: {type: int, description: how far back, default: 30}}
    returns:
      State:      {type: string}
      CloseNotes: {type: string}
      Gen:        {type: int}
    sql: |
      SELECT i.state                     AS "State",
             coalesce(i.close_notes, '') AS "CloseNotes",
             CAST(i."__gen" AS INTEGER)  AS "Gen"
      FROM {{.ReadSchemaLatest}}.{{.Table}} i
      WHERE i.{{.Key}} = :SubjectKey
        AND i."__gen" <= {{.LatestGen}}
        AND i.state IN ('800', '900')
      ORDER BY i."__gen" DESC
      LIMIT 1
```

| Key | Required | Rule |
|---|---|---|
| `inputs` | no | Bound as **SQL arguments**, so types must be scalar. |
| `params` | no | Names are identifiers, types scalar; a `default:` is coerced through the same path a literal takes. A param with no default must be supplied by every step using it (as `params:` or as a `config:` knob). |
| `returns` | **yes** | Non-empty; scalar or `timestamp`. *The returned columns are what a later step's row-list input is type-checked against.* |
| `embed_input` | no | Names a declared **string** input — the text the platform embeds to produce `{{.QueryVector}}`. |
| `sql` | **yes** | See below. |

**Placeholders.** Every `:name` becomes a positional argument, in order. `::` (a
DuckDB cast) is left alone. A `:name` that is neither a declared input nor a
declared param refuses.

**The template surface is CLOSED.** The SQL is a template with
`missingkey=error`, dry-run at load against a fixed surface — anything outside
it is a load error, not a runtime surprise:

| Always available | |
|---|---|
| `{{.Table}}` `{{.Key}}` `{{.Gen}}` `{{.LatestGen}}` | the subject table, its key column, the case's **pinned** frontier gen, and the frontier **as of this tick** (the "second read") |
| `{{.TextTable}}` `{{.EmbeddingsTable}}` `{{.FTSIndex}}` | the retrieval surfaces |
| `{{.QueryVector}}` `{{.EmbedModel}}` `{{.Dims}}` | the embedding of `embed_input` and its model |

Plus **exactly one** of these two disjoint sets:

| The def declares… | …and gets |
|---|---|
| no `reads:` | `{{.Schema}}` `{{.LatestSchema}}` |
| `reads:` | `{{.ReadSchema}}` `{{.ReadSchemaLatest}}` `{{.Read.<alias>}}` `{{.ReadLatest.<alias>}}` |

`{{.Read.<alias>}}` reaches **only** the aliases `reads:` declares — the sample
map carries exactly those, so a typo is a load error. Use `{{.ReadSchema}}` for
the case's pinned window and `{{.ReadSchemaLatest}}` for the second read at this
tick's frontier; a closure gate wants the latter, because *a pinned read
structurally cannot see an update.*

### 7.3 `retrieve:` — declared hybrid retrieval

`retrieve:` is not a sixth executor: it **compiles at load into a declared query**
named `<step id>_retrieve`, and that generated SQL then goes through the very
same validator as a hand-written `query:` — so a compiler bug is a def that
refuses to load.

```yaml
- id: precedents
  retrieve:
    text: subject.short_description     # the ONE declaration that feeds both arms
    arms: [fts, vector]
    fuse: rrf
    collapse: {near_dupe: 0.92}
    where:
      exclude_subject: true
      non_empty: [close_notes]
    returns:
      Number:     number
      Symptom:    short_description
      Resolution: close_notes
  ready: [embeddings, fts]
```

| Key | Required | Rule |
|---|---|---|
| `text` | **yes** | A `subject.<column>` reference. It IS the binding — which is why a `with:` block beside `retrieve:` refuses. |
| `arms` | **yes** | Non-empty, a subset of `[fts, vector]`, no duplicates. A third arm is a grammar change: write it by hand with `query:` until the platform grows it. |
| `fuse` | no | Only `rrf` (reciprocal rank). |
| `collapse` | no | `near_dupe:` required when present, and requires the `vector` arm — there is no vector space to measure it in otherwise. The threshold is the **def's** policy; the platform ships no default. |
| `where.exclude_subject` | no | Needs `activation.subject.key`, and that key must be a declared string subject column. |
| `where.non_empty` | no | Each entry must be a declared subject column. |
| `returns` | **yes** | `<output name>: <declared subject column>`. v1 returns SUBJECT COLUMNS of the retrieved rows and nothing else — a computed column is a `query:`. |

What the compiler emits, which you get without declaring it:

- inputs `QueryText` (always) and `SubjectKey` (when excluding the subject);
- params with defaults — `top_k` **5**, `rrf_k` **60**, `candidate_k` **50**,
  and `near_dupe` at your declared threshold when collapsing (all four tunable
  through `config:`, §9);
- returned columns: yours, **plus** `Occurrences` (with `collapse:`) and
  `Citation` (always). Naming either yourself refuses — renaming the citation is
  a change to its FORMAT, which is a grammar question;
- `embed_input: QueryText` when the vector arm is on;
- **inferred readiness**: `vector` → `embeddings`, `fts` → `fts`, unioned with
  whatever `ready:` you declared. It can add, never drop.

`Citation` is a fixed platform string, joined with ` · `: `bm25 rank <n|no
match>`, `cosine <x|no match>`, `fused <x>`, `seen <n>×`, `embedded by <model>`,
`corpus pinned at gen <n>`.

### 7.4 `notify:` — a purpose, never a destination

```yaml
notifications:
  advisory_card:
    labels: {purpose: advisory}
    inputs:
      Number:   {type: string, description: the human-facing INC number}
      Advisory: {type: string, description: the advisory the advise step wrote}
    subject: "Resolution advisory · {{.Number}}"
    target:  "{{.Number}}"
    body: |
      Resolution advisory for {{.Number}}.
      {{.Advisory}}
    card: |
      {"@type": "MessageCard", "text": {{ jsonstr .Advisory }}}
```

**A def declares a purpose; the workspace's routing object decides the
destination.** No URL and no endpoint id is expressible in this grammar at all.

| Key | Required | Rule |
|---|---|---|
| `labels` | **yes** | Non-empty. Keys are identifiers, values non-empty and **static** — a value containing `{{` refuses, because a template here would let a def widen its own reach at run time. A notification with no labels can only reach a catch-all rule, which is not routing, it is luck. |
| `subject` | **yes** | Template. Every door the routing may pick names the message somehow. |
| `body` | **yes** | Template. `card:` alone serves only the kinds that POST JSON — and the def does not choose the kind. |
| `card` | no | Template that must render **valid JSON**. |
| `target` | no | Template. |

FuncMap: `join`, `truncate`, `jsonstr` — nothing else. Templates are dry-run
**three times** at load: full sample, zero sample, and a *hostile* sample whose
strings contain quotes, a backslash, a newline and markup — with `json.Valid`
applied to the rendered `card:` each time. This is why every interpolated value
in a card must go through `jsonstr`: a bare `{{.Field}}` inside quotes renders
fine until the first value containing a double quote, and then it breaks a live
card.

`close_when:` on a notify step refuses: a notify step has a delivery *outcome*,
not a ruling about the subject, and **a case must not close because nobody was
listening.**

### 7.5 `deliver:` — declared outbound rows

```yaml
deliveries:
  kpi_platform:
    rows:
      kpi_id:    {type: int,       description: the KPI this row scores}
      anchor_ts: {type: timestamp, description: the period the score anchors on}
```
```yaml
  - id: deliver
    deliver: kpi_platform
    with: {rows: compute.rows}
    promote: cdhkpi.kpi_results
```

Column types are scalar or `timestamp`. A column may **not** be named `outcome`
or `outcome_detail` — the executor appends those, so the input may not claim
them. `params:`, `config:` and `close_when:` all refuse on a deliver step; a
per-row value belongs in the query that produced the rows.

### 7.6 `with:` — bindings, type-checked at load

Two forms, and only two:

```yaml
with:
  ShortDescription: subject.short_description   # the case's PINNED gen
  Actual:           subject.latest.close_notes  # the SECOND read, same column
  Precedents:       precedents.rows             # an EARLIER step's output
  IncludedDomains:
    const: [alteryx, tableau, databricks]       # a literal
```

- `subject.<column>` reads the case's pinned gen; `subject.latest.<column>` reads
  the latest gen — the same declared column, read a second time.
- `<step id>.<field>` may name only a step **above** this one. Steps run in
  declaration order, so a binding may only read upward.
- `{const: <value>}` is the whole literal form. `{konst: …}` refuses.

Everything is checked before anything runs: a binding whose declared type does
not match the source's refuses; a row-list input is checked **column by column,
name and type** against the producing query's `returns:` (extra returned columns
are fine, missing ones refuse); a supplied-but-undeclared binding refuses,
because it is almost always a typo for a declared one and accepting it would
leave the template rendering a hole; a required input with no binding refuses.
Literals go through the same coercion a runtime value gets.

---

## 8. Conditions — the five mechanical forms

**There is no expression language.** Every condition a def can express is one of
these, and that is the contract's most load-bearing negative fact.

### 8.1 `gate:` — a data condition or a human mandate

```yaml
  - id: await_closure
    gate: {on: incident_closed, expiry: 720h}
    with: {SubjectKey: subject.sys_id}
```
```yaml
  - id: worknotes
    notify: servicenow_worknotes
    gate: {authority: cdl-knowledge-owner, expiry: 168h}
```

- **`on:`** names a declared query. **The gate passes when the query returns at
  least one row.** The SELECT *is* the predicate — no operator, no comparison, no
  boolean combinator. A data gate is its own step (a gate condition riding
  another step would need two `with:` blocks), and because steps are sequential,
  a gate step gates everything after it.
- **`authority:`** names a mandate, and the condition is a ratification row on
  the case ledger. Absent = the gate is dark and its step is skipped (the
  workflow continues); proposed = the case parks; ratified = the step runs;
  refused = the step is skipped with the refusal recorded. It binds nothing.
- **`expiry:` is required for both**, a positive Go duration (`720h`, `168h`,
  `15m`). *A case waiting forever is invisible rather than terminal.* A gate
  still waiting past its expiry leaves the case `expired`.
- The two flavours are mutually exclusive: a case parked on both would have no
  single reason on its row.

### 8.2 `close_when:` — closing on a declared verdict

```yaml
close_when: {verdict: OUT_OF_SCOPE, deliver_first: scope_notice}
```

Function steps only. The function's output contract **must be `kind: enum`** and
the token must be one of its `allowed:` values — both checked at load. *A
verdict condition over free text would be a substring match, which is exactly
the inversion the enum contract exists to prevent*, and a condition that can
never fire is worse than no condition.

`deliver_first:` names a **later** `notify:` step that runs before the case
closes — because declining in silence is not declining. It must be declared
below this step, be a notify step, be enabled, and bind only steps at or above
the deciding step (a step it binds below would never have run for a case closed
here). There is deliberately **no `when:` key**: a `deliver_first` target is
conditional by construction.

### 8.3 `precheck:` — the mechanical pre-answer

```yaml
precheck:
  match: IncludedDomains
  in: [ShortDescription, Description]
  verdict: IN_SCOPE
  when_all_empty: [IncludedDomains, ExcludedDomains]
```

Function steps only. A keyword from the `match` list (a declared `type: strings`
input) appearing **verbatim, case-insensitively** in the concatenation of the
`in` inputs (declared `type: string`) resolves the step to `verdict` **without
calling the model**. `when_all_empty:` names inputs which, when all empty,
resolve the same way — the allow-all arm. A precheck that reads nothing answers
every case, which is a function nobody should have declared.

### 8.4 `on_error:` — the fallback token

A single declared enum token the function step resolves to when the model seam
errors. Function steps only.

**The shared rule for both `on_error:` and `precheck.verdict:`**: the token must
be one of the function's `allowed:` tokens, and it may **not** be the step's
`close_when:` verdict. A fallback that CLOSES the case would rule on the subject
without the model ever having ruled — *whatever answers mechanically must be the
answer that lets the case carry on.*

### 8.5 `ready:` — waiting for a derivation

`ready: [embeddings, fts]` parks the case until those derivations have caught
up. Any step whose query reads the FTS index **must** declare `ready: [fts]`:
the index is the one surface a declared read reaches *outside* the gen window,
so the run stamps `replay=partial` — and only a declared readiness puts the
index's own frontier on the row beside it. Without both, the record admits it is
not exactly replayable and does not say what it read.

### 8.6 `activation.filter:` — equality, and nothing else

`{state: "100"}` is the whole vocabulary. See §5.

---

## 9. `config:` — the declared knob surface

A knob is a value the def **declares** and the workspace **tunes**:

```yaml
  - id: precedents
    retrieve: {…}
    config:
      top_k:     {type: int,   default: 5,    min: 1,   max: 20}
      near_dupe: {type: float, default: 0.92, min: 0.5, max: 1.0, description: >
        cosine at or above which a lower-ranked candidate collapses into a
        higher-ranked one.}
```

| Key | Required | Rule |
|---|---|---|
| `type` | **yes** | Scalar only — a knob is one tunable value. |
| `default` | **yes** | The DEF's own answer when the workspace says nothing. A knob without one is a required value hidden a document away from the step that needs it. |
| `min` / `max` | no | `int` and `float` only — *a bound nothing checks is a promise to the person tuning it that nothing keeps.* |
| `description` | no | The whole reason a knob is not just a number. |

Where the resolved value lands: on a **query or data-gate** step it supplies a
declared `params:` entry (a SQL argument); on a **function** step it supplies a
declared `inputs:` entry by name — exactly where a `{const: …}` binding would
land, and it satisfies a required input. A value declared in both `with:` and
`config:`, or in both `params:` and `config:`, refuses: **one home per value.**
`params:` is a FIXED value the workspace may not touch; `config:` is a tunable
one it may.

`config:` refuses on `notify:` and `deliver:` steps. What a notification says is
its templates and its labels; where it goes is the workspace's routing object,
not a knob.

**Resolution order:** query-param default < step `config:` default < workspace
value.

### 9.1 The values live outside the def

The def declares the *surface*; a separate workspace-scoped document carries the
*values*:

```yaml
def_id: cdl-advisory.incident
values:
  precedents.near_dupe: 0.91
```

A flat `<step>.<knob>: value` map and nothing else. It **refuses, never skips**:
a key that is not `<step>.<knob>`, a step the def does not declare, a step that
declares no knobs, an undeclared knob, a value outside the declared bounds — each
is a loud failure. (The operating document — workers, enabled — is deliberately
the opposite: malformed means defaults. Do not make them consistent; the
asymmetry is the ruling.)

This split is the reason the def can be content-hashed (§3.1): tuning a
threshold changes the config document's version, not the def's, so
`(def version, config version)` reads as an A/B pair rather than a confound.

---

## 10. Worked example

The in-tree reference def — CDL advisory, one case per incident. Comments
stripped; the header and activation are §3 and §5 above.

```yaml
steps:
  - id: scope
    function: domain_check
    with:
      IncludedDomains:
        const: [alteryx, tableau, databricks, gallery, data platform, etl]
      ExcludedDomains:
        const: [network, vpn, printer, badge access]
      ShortDescription: subject.short_description
      Description: subject.description
    precheck:
      match: IncludedDomains
      in: [ShortDescription, Description]
      verdict: IN_SCOPE
      when_all_empty: [IncludedDomains, ExcludedDomains]
    on_error: IN_SCOPE
    close_when: {verdict: OUT_OF_SCOPE, deliver_first: scope_notice}

  - id: scope_notice
    notify: scope_limitation_notice
    with:
      Number: subject.number
      Team: subject.label_team
      ShortDescription: subject.short_description

  - id: skip
    enabled: false                     # declared, dormant, still type-checked
    function: should_skip
    with:
      Team: subject.label_team
      ShortDescription: subject.short_description
      Description: subject.description
    close_when: {verdict: AUTO_RESOLVE}

  - id: precedents
    retrieve:
      text: subject.short_description
      arms: [fts, vector]
      fuse: rrf
      collapse: {near_dupe: 0.92}
      where:
        exclude_subject: true
        non_empty: [close_notes]
      returns:
        Number: number
        Symptom: short_description
        Resolution: close_notes
    ready: [embeddings, fts]
    config:
      top_k:       {type: int,   default: 5,    min: 1,   max: 20}
      candidate_k: {type: int,   default: 50,   min: 5,   max: 500}
      rrf_k:       {type: int,   default: 60,   min: 1,   max: 1000}
      near_dupe:   {type: float, default: 0.92, min: 0.5, max: 1.0}

  - id: advise
    function: generate_resolution
    with:
      ShortDescription: subject.short_description
      Description: subject.description
      Precedents: precedents.rows        # row-list, column-checked against the retrieve

  - id: deliver
    notify: advisory_card
    with:
      Number: subject.number
      Team: subject.label_team
      Advisory: advise.output
      Precedents: precedents.count

  - id: worknotes
    notify: servicenow_worknotes
    with:
      Number: subject.number
      Advisory: advise.output
    gate: {authority: cdl-knowledge-owner, expiry: 168h}

  - id: await_closure
    gate: {on: incident_closed, expiry: 720h}
    with: {SubjectKey: subject.sys_id}

  - id: score
    function: resolution_validation
    terminal: scored
    with:
      Predicted: advise.output
      Actual: subject.latest.close_notes   # the second read — the gate let it appear
```

The shape to notice: `await_closure` parks the case for up to 30 days on a
declared query, and `score` then reads `subject.latest.close_notes` — a *pinned*
read structurally could not see the closure that the gate was waiting for.

The function consuming the retrieval declares the platform's added columns
alongside its own, which is what makes the row-list check meaningful:

```yaml
      Precedents:
        type: rows
        optional: true
        columns:
          Number:      {type: string}
          Occurrences: {type: int}      # platform-emitted (collapse)
          Symptom:     {type: string}
          Resolution:  {type: string}
          Citation:    {type: string}   # platform-emitted (always)
    output:
      kind: text
      max_chars: 1500
```

---

## 11. Versioning & stability

`MAJOR.MINOR.PATCH`, the same policy as the sibling contracts:

| Bump | Meaning | Examples |
|---|---|---|
| **MAJOR** | Breaking — an existing def may refuse to load. | Remove/rename a key; tighten validation that previously passed. |
| **MINOR** | Additive — old defs keep loading. | A new step key, executor or arm **whose interpreter ships in the same change**. |
| **PATCH** | No schema change. | Doc clarifications. |

Pre-1.0 (today), a MINOR may still break: the grammar is young and the announce
path has no platform consumer yet. Note that this is the version of the
*document*; a def's own version is its content hash (§3.1), and they are
unrelated numbers.

### 11.1 Changelog

| Version | Date | Change |
|---|---|---|
| 0.1.0 | 2026-08-14 | Initial contract (S-2212), born current: activation + the `reads:` keystone rule (S-1955), the five executors including `retrieve:` (S-2208) and the `query:` escape hatch, `config:` knobs (S-2209), `deliver:`/`promote:` (S-2008), the condition forms, content-hash versioning, and the `WorkflowArtifact` announce path. |

---

## 12. The honesty rule

**Config carries nothing the engine doesn't enforce.** Every decoder in this
grammar is strict (`KnownFields`), at every nesting level, including the ones
that re-decode a fragment (`{stream:, wait:}`, `{const: …}`, the `functions:`
block). A def carrying a key outside this document **refuses to load, by
design**, instead of being silently ignored.

The corollary is the reserved list in §4: keys that a reader might reasonably
expect — `stages:`, `gates:`, `skill:`, `runnable:` — refuse *by name*, with an
error saying why, rather than parsing into nothing. A YAML file that loads is a
YAML file whose every line does something.

Two more places the same rule shows up, worth knowing because they surprise
people:

- **The def body is authoritative over an edited stored copy.** The seed
  persists the shipped bytes and reverts a stored edit at the next boot. A
  stored def edit is a RUNNING change, not a durable one.
- **A prompt function's `description`, and every `description:` on an input,
  param, return or column, is documentation only.** It never reaches a model.
  (Descriptions on *lake* declarations are different — there they are product
  surface; see [`./lake-artifact.md`](./lake-artifact.md).)

---

## 13. The opaque `goal:` block

```yaml
goal:
  per: case
  terminal: advised_and_scored | ruled_out | expired
```

Decoded as `map[string]any` and carried verbatim. The platform never validates,
interprets, or enumerates its keys — the owning solution is the interpreter, if
anyone is. In particular the `terminal:` above is a *string*, unrelated to a
step's `terminal:` key, and nothing parses that disjunction.

---

## 14. Related documents

- [`./workflow-defs.md`](./workflow-defs.md) — the v1 pure-skill contract. A
  different grammar with a different parser; this document does not supersede it.
- [`./lake-artifact.md`](./lake-artifact.md) — declaring the lake a def
  activates on, including label-scoped binding and the exclusivity law that
  decides **which workspace's case** a landed row opens.
- [`./dashboard-dsl.md`](./dashboard-dsl.md) — the sibling YAML-from-solution
  contract whose versioning policy this follows.
- `domains/workflow/CLAUDE.md` (platform repo) — the parser, the stores, the
  gotchas. The grammar there is the source of truth; this document specifies the
  enforced subset.
