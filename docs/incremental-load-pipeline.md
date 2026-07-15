<!--
  ┌──────────────────────────────────────────────────────────────────────┐
  │  Incremental Load Pipeline — Solution Data Contract                   │
  ├──────────────────────────────────────────────────────────────────────┤
  │  Contract version : 0.2.0                                              │
  │  Status           : DRAFT — README-driven; implementation follows      │
  │  Stability         : unstable; laws are firm, mechanics may move       │
  │  Surface           : external — solution authors declare fetch scope,  │
  │                      source_glob, decode SQL, and retention classes    │
  │                      against the guarantees specified here; operators  │
  │                      schedule the three runnables this doc sequences.  │
  │  Last updated      : 2026-07-04                                        │
  │  Owner ticket      : S-1610 (this doc) / S-1611 (fetch) / S-1612       │
  │                      (load) / S-1608 (legacy mode) / S-1613 (§10)      │
  │  Implements        : partially shipped — full-listing fetch (S-1585),  │
  │                      open-bucket load (S-1554), ladder (S-1565..69);   │
  │                      the incremental laws below are the target state.  │
  │  Supersedes        : the legacy Ingestion Pipeline planner             │
  │                      (domains/ingestion/runnable.go, deleted at M9)    │
  └──────────────────────────────────────────────────────────────────────┘
-->

# Incremental Load Pipeline — Solution Data Contract

**Contract version 0.2.0 · Draft**

This document specifies the **safe end-to-end incremental load pipeline** for
solution data: how bytes move from a customer's source to workspace-queryable
tables, what each stage may assume about the previous one, and the laws that
make the whole chain incremental *without ever silently double-counting or
silently dropping data*. It is the contract the implementation is built
against (README-driven), and the reference for anyone authoring a solution or
operating the pipeline.

```
source (Azure Blob / test_local)
   │  solution-fetch        — incremental listing, resumable copy      (§4)
   ▼
inbox/<container>/…          — verbatim blobs, the fetch's output
   │  solution-load          — consumed-manifest diff → touched buckets (§5)
   ▼
lake/<catalog>/<dataset>/bucket=<key>/   — decoded, partitioned parquet
   │  solution-projection    — ladder reconcile / snapshot rebuild      (§6)
   ▼
blocks/<catalog>/…  (or ws/<ws>/<catalog>.duckdb)
   │  mount-time views       — per-workspace label-scope predicates (free)
   ▼
catalog: workspace           — agent tools + dashboards
```

The fourth arrow costs nothing per workspace by construction (§7.6.1 fan-out
law in `docs/design/comply-port-out-incremental-solution-data.md`): workspaces
are view predicates over shared blocks. Incrementality is therefore a
three-stage problem — fetch, load, reconcile — and this contract binds those
three.

> **Relation to the design doc.** The architecture (why containers, why
> retention classes, why the ladder) is
> `docs/design/comply-port-out-incremental-solution-data.md`. This document
> pins the *incremental behaviour contract* of the pipeline that design
> describes: §5.4 (hourly idempotent partition-replace) and §4.9 (the
> load-request seam) are composed here into stage-by-stage guarantees.
> Provenance for the clever parts is the legacy Ingestion Pipeline
> (`domains/ingestion/runnable.go` — horizon scanning, adaptive prefix
> listing, cumulative-file re-download), which this contract transplants and
> hardens before M9 deletes it.

## 1. Versioning & stability

Pre-1.0: the **laws (§2) are firm** — code that violates one is a bug, not a
version bump. Mechanics (state-file schemas, exact paths, knob names) may
change in minor versions while the doc is DRAFT. State files carry a
`version` field; a reader encountering an unknown version treats the file as
absent (§2 law 6 makes that safe).

## 2. The six laws

Every stage obeys all six. They are the whole safety argument; each stage
section below just instantiates them.

1. **The filesystem is the state.** A stage derives *what is new* by diffing
   its input directory against its own recorded consumption state (a sidecar
   state file, §3). No window/cursor is passed between stages in-band —
   `jobs.Runnable` has no per-execution payload (the S-1562 precedent), and
   deriving-from-disk keeps every stage independently re-runnable.

2. **The bucket is the idempotency unit — replace, never append.** (Design
   §5.4.) A stage's incremental output is a **set of touched buckets**, not a
   single time window: late-arriving and cumulative files touch old buckets,
   and correctness is per-bucket re-materialization. (The legacy no-retention
   dataset shape is the degenerate case with no buckets — §5.5.)

3. **Evidence decides completeness, never the clock.** A bucket is
   re-processed whenever its *observed inputs changed*, regardless of whether
   the wall clock says the bucket is "closed." Azure's cumulative `PT1H.json`
   files grow for minutes past each hour boundary — structural late data —
   and the only safe trigger for the final re-process is the observed size
   change, not an assumption that hour H was done at H:59.

4. **Failure holds the horizon.** A failed listing window, a failed download,
   or a failed bucket build keeps that window/file/bucket in next run's work
   set. Progress state advances only past *cleanly completed* work. (The
   legacy planner violated this: `listFiles` warned-and-continued past a
   failed prefix listing, and the horizon then advanced over the gap forever
   — `domains/ingestion/runnable.go:600`. That flaw is explicitly not
   transplanted.)

5. **All writes are atomic.** Temp name + `os.Rename`, everywhere: fetched
   blobs (`.partial`, `mirror.go`), lake parquet (duckdbcopy per-flush files
   + bucket-dir swap, §5.4), ladder blocks and manifest (S-1565), and every
   state file this contract adds. No reader — including a stage's own next
   run — can ever observe a torn artifact.

6. **Corrupt or missing state degrades to full, loudly.** A missing,
   unparsable, or unknown-version state file means "I know nothing": the
   stage falls back to its full (non-incremental) behaviour — full listing,
   full rebuild — logs a `Warn`, and rebuilds the state file on clean
   completion. Degradation may be slow; it is never wrong. Silent
   double-count and silent gap are the two unforgivable failures.

## 3. State layout

```
<dataDir>/solutions/<sol>/
├── inbox/<container>/…                  DATA — fetched blobs, verbatim
├── lake/<catalog>/<dataset>/bucket=…/   DATA — decoded partitioned parquet
├── blocks/<catalog>/                    DATA — ladder blocks + manifest.json
├── ws/<ws>/<catalog>.duckdb             DATA — per-workspace snapshots
└── state/                               STATE — consumption bookkeeping
    ├── fetch-<sourceID>.json            fetch horizon        (§4, S-1611)
    └── load-<catalog>.json              consumed manifest    (§5, S-1612)
```

**Data directories contain only data.** State never lives inside `inbox/` or
`lake/`: the load enumerates inbox subdirectories as containers
(`containerDatasetLoads`) and the projection globs the lake recursively — a
state file inside either would be enumerated as data. `state/` is a sibling,
invisible to both. (The ladder's `manifest.json` predates this rule and stays
where it is — `blocks/` is never glob-enumerated.)

State files are JSON, written temp-file + rename (law 5), each carrying
`{"version": 1, …}` (§1).

## 4. Stage: `solution-fetch` (source → inbox) — S-1611

Ships today (S-1585) as: full listing (`**/*`) every run → filter (empty /
`Until` / container allow-list / caps) → resumable copy (skip when a
destination exists with matching size; `.partial` + rename). Correct, but the
listing is O(entire account) on every tick. This contract adds the horizon.

### 4.1 Fetch state

```json
{
  "version": 1,
  "horizon": "2026-07-04T06:00:00Z",
  "failing": ["container-x/resourceId=…/PT1H.json"],
  "updated_at": "2026-07-04T08:04:11Z"
}
```

- **`horizon`** — the hour boundary up to which fetching is known
  *clean-complete*: every listing window at or before it succeeded and every
  in-scope blob listed in it was landed (or deliberately skipped by scope
  rules). Zero/absent = never completed a clean run.
- **`failing`** — blob paths that failed download on the last run (law 4
  bookkeeping + operator visibility; retried every run; logged `Warn` each
  run they persist).

### 4.2 Listing

- **No usable state (first run, corrupt, unknown version):** full listing —
  byte-identical to today's behaviour (law 6). The resumable copy engine
  makes a full listing cheap in downloads (size-match skips), just not in
  list calls.
- **With a horizon:** generate path-template prefixes covering
  `[horizon, now]` and list only those. Adaptive granularity, transplanted
  from the legacy planner: **day** prefixes when the gap is ≤ 48 h, **month**
  prefixes otherwise. Requires the source to declare a `path_template`
  (the `y=YYYY/m=MM/d=DD/h=HH` convention — the field `domains/source` gains
  per design §9.1, formerly on `domains/dataset`). A source with no
  `path_template` always full-lists (correct, degraded).
  **Config-home note (v0.2, §10):** the template's DECLARATION home moves to
  the solution's per-variant load declaration — layout is product knowledge
  that crosses customers; `FetchConfig.PathTemplate` is deprecated in favor
  of it (S-1614). The runtime behaviour specified here is unchanged.
- **The template must be LITERAL — glob metacharacters are rejected
  loudly.** The connectors' pattern matchers require a literal prefix, so a
  glob-headed window pattern silently matches *nothing*: every narrowed run
  would list zero blobs and the fetch would stall forever while looking
  healthy (verified against the comply fixture's multi-workspace resourceId
  layout). A layout whose date segments sit under varying path components
  (several `WORKSPACES/<ws>` per container) cannot use prefix narrowing in
  v1 — leave `path_template` empty (full listing, correct-but-degraded)
  until multi-template / per-stream sequence-cursor narrowing lands (§9).
- The listing window **always includes the horizon hour itself** — its
  cumulative files may still have grown (law 3). Since prefixes are
  day-granular, this inclusion costs zero extra list calls.

### 4.3 Download

Unchanged from S-1585: oldest-first deterministic order, container
allow-list, `Until`/`MaxBlobs`/`MaxBytes` as the bounded-backfill knobs,
0-byte markers skipped, `.partial` + rename, **size-mismatch re-download**
(a grown cumulative blob replaces its inbox copy atomically — this is the
mechanism law 3 rides on).

### 4.4 Horizon advancement (law 4)

**The horizon is data-relative, never wall-clock.** It advances to the
newest *path-date hour observed among the files this run landed* (minus
grace) — not to `now`. This is the legacy `scanHorizon` insight kept intact:
the data decides how far we've gotten. A wall-clock horizon would silently
jump past anything whose path dates lag the clock — a bounded backfill
(`Until` in the past), a quiet source, a paused producer, or a fixture
replayed with an advancing ingestion limit — and those windows would never
be listed again. Data-relative, all of these compose for free: the horizon
sits wherever the data actually reached, and the next run lists forward
from there.

On run completion:

- **Clean run** (every prefix listing succeeded, zero download failures):
  `horizon := maxObservedHour − grace`, truncated to the hour, state written
  atomically; never moved backwards. `maxObservedHour` is the newest
  path-date hour in the run's final work set (a capped run's set is its
  oldest-first prefix, so the horizon lands exactly at the cap — the next
  run continues from there). A run that observes nothing leaves the horizon
  unchanged. `grace` (default **2 h**) keeps the last observed hours inside
  the next listing window as belt-and-suspenders for slow blob
  finalization; with day-granular prefixes it costs nothing.
- **Any listing failure or any download failure:** horizon **unchanged**
  (the whole window re-lists next run; already-landed blobs are size-match
  skips, so the retry is cheap). Failed blob paths recorded in `failing`.
  The run itself completes with a warning (partial), so a scheduled job
  keeps its cadence — the horizon is what enforces the retry, not the job
  status.

**Residual risk accepted for v1:** a blob that fails download persistently
while its neighbours succeed can reach the load incomplete (the load only
sees what landed). The `failing` list + per-run `Warn` is the operator
signal; automated quarantine/alerting is out of scope here.

## 5. Stage: `solution-load` (inbox → lake) — S-1612, S-1608

Ships today as: retention path rebuilds **only the open bucket** every run —
but derives it by re-decoding the **entire inbox glob** and filtering
`WHERE bucket = <open>` (scan amplification: hourly cost grows with inbox
history); legacy path is `LoadModeFull` (clear + re-COPY everything) with
`LoadModeIncremental` a stub. This contract replaces both inner loops with a
consumed-manifest diff. The S-1570 collision guard and S-1568 per-dataset
error aggregation run exactly as today, before/around everything below.

### 5.1 Consumed manifest

Per (solution, catalog), at `state/load-<catalog>.json`:

```json
{
  "version": 1,
  "files": {
    "insights-logs-jobs/resourceId=…/y=2026/m=07/d=04/h=06/PT1H.json": {
      "size": 184320,
      "dataset": "insights-logs-jobs",
      "buckets": ["2026-07-04T06"]
    }
  }
}
```

Keys are inbox-relative paths of every file the load has consumed. `size` is
the size at consumption time. `buckets` records which lake bucket keys the
file's rows contributed to — learned at consumption time from the file's
*data*, over *just that run's changed files* (only new data is ever scanned
to learn its buckets). For the legacy (no-retention) shape, `buckets` is
empty and unused (§5.5).

**Decode-once staging (S-1615).** For a decode-at-ingest dataset the learning
scan and the rebuild COPY would each decode the same changed bytes — a
measured 4.4× steady-tick penalty (§5.6). The engine therefore decodes each
changed file exactly ONCE into a transient per-file staging parquet carrying
a `__src` attribution column; learning becomes a columnar
`SELECT DISTINCT __src, bucket` over the staging (milliseconds), and the
targeted replace (§5.3) reads the staged rows back instead of re-decoding the
delta. This is an internal re-implementation of the same contract: same
manifest, same touched-bucket math, same lake cells — a spillover row still
lands in its true event-time bucket because the bucket column is computed
from the staged row's data. Plain-parquet datasets skip staging (their learn
is already a single list-shaped read).

### 5.2 Diff → touched buckets

Each run, per dataset (same enumeration as today — containers in scope of
`source_glob`, or projection-derived for legacy):

- **new** — on disk, not in manifest;
- **changed** — on disk, size differs from manifest (the re-fetched grown
  `PT1H` case);
- **absent** — in manifest, not on disk. **No action** (one `Debug` line).
  The inbox is transport; the lake is the retained truth — a consumed blob
  may be pruned upstream without its decoded rows vanishing from the lake.
  *The steady-state incremental load only ever adds or updates evidence;
  removing data from the lake is exclusively the backfill's job* (an
  operator who shrank the inbox and wants the lake to match runs
  `solution-load-backfill`). Manifest entries for absent files are retained —
  they record what the lake contains.

Touched buckets = union of `buckets` over (new ∪ changed), where the bucket
sets are computed fresh this run (changed files: fresh ∪ previously recorded
— a shrunk time-range must still touch the buckets it used to feed).
**Nothing changed → the run is a no-op** — one `log.Info`, zero DuckDB work.
(Better than today, which rebuilds the open bucket unconditionally.)

Note this is law 3 operating: a touched bucket may be "closed" by the clock —
it is rebuilt anyway, because the evidence (a file changed) says so.

### 5.3 Targeted partition replace (retention path)

For each touched bucket `B` of dataset `D`:

1. **Contributing files** = every manifest file of `D` whose `buckets`
   contains `B`, plus this run's new/changed files feeding `B`. If any
   contributing file is absent from disk (pruned upstream), the bucket
   **cannot be faithfully re-materialized** — skip it with a `Warn`
   (existing partition kept as-is; rescue = backfill). In practice files
   and buckets are both hour-scoped, so an old bucket is only ever touched
   by its own files and this guard fires only on a genuinely inconsistent
   inbox.
2. Compose the same decoded, label-stamped, bucket-stamped SELECT as today
   (`partitionedSelectSQL` over `fromSourceSQL`), with two narrowings: the
   `{source}` file set is the **contributing-file list** (not the whole
   container glob), and the filter is `WHERE bucket = 'B'`.
3. COPY into a temp dir; swap it in as `lake/<catalog>/<D>/bucket=B/`
   (remove-old + rename — law 5; the reader of the lake is the reconcile
   step, which runs after the load in the same job, §6).
4. Untouched buckets: not read, not written, not deleted.

After **all** touched buckets of all datasets land (or fail — per-dataset
aggregation as today), write the manifest **once**, atomically, reflecting
only the successfully loaded datasets' files (law 4: a failed dataset's
entries stay stale, so its diff re-derives next run).

**Crash safety:** a crash after some bucket swaps but before the manifest
write leaves disk-newer-than-manifest — next run re-derives a superset of the
same touched set and re-replaces those buckets. Replace is idempotent; no
double-count is possible (law 2).

### 5.4 Backfill and degradation

- `solution-load-backfill` (the rescue/initial path) is unchanged in
  behaviour — clear everything, rebuild every bucket from the whole inbox —
  and now additionally **rewrites the consumed manifest** from its full scan,
  so a backfill leaves the incremental path primed.
- Missing/corrupt/unknown-version manifest on the steady-state runnable:
  `Warn` + behave as backfill for that run (full rebuild, manifest
  regenerated) — law 6. Never attempt to "guess" increments without state.

### 5.5 Legacy datasets (no retention) — `LoadModeIncremental`, S-1608

The degenerate case of the same manifest, for the DQ / conduct / controls /
salesintegrity shape (no `retention:` block; buckets don't exist):

- **new** files → COPY *only those files* and append the output parquet
  beside the existing lake files (duckdbcopy's `{uuid}` names are
  append-native); record them in the manifest.
- **changed** files → the append model can't subtract; degrade that dataset
  to a full rebuild this run (clear dir + full COPY + reset its manifest
  entries), with a `Warn`. Legacy inboxes are append-only in practice; this
  is the safe fallback, not the expected path.
- **absent** files → no action (same doctrine as §5.2: the lake keeps their
  rows; removal is Full mode's / backfill's job).
- Unchanged inbox → no-op (the S-1608 acceptance bar: re-run never
  double-counts, because the projection sums every parquet in the dir).
- `LoadModeFull` stays byte-identical (clear + COPY all, manifest rewritten).

Mode selection: `NewIncrementalLoadRunnable` — a third registered runnable
(`solution-load-incremental`), the `NewBackfillLoadRunnable` precedent (zero
jobs-domain changes). Once §4.9 load-requests exist they become the trigger;
the runnable split is the v1 seam. *(Open: whether `solution-load` itself
should default to incremental once proven — see §9.)*

## 6. Stage: `solution-projection` (lake → ladder/snapshots)

**Machinery unchanged** — the ladder (S-1565..69) is already incremental and
evidence-driven where it matters:

- The lowest rung rebuilds from the lake each reconcile with a row-count
  idempotency check — an untouched lake is a discard-and-no-op; a lake whose
  open-window bucket was replaced (§5.3) produces a changed count and the
  growing block re-materializes.
- Higher rungs move only on lower-rung closure; sealing/pruning per S-1567.

What this contract adds is an **ordering requirement and one honesty rule**:

1. **Steady-state job = fetch → load → project, in that order, as steps of
   one job** (the existing jobs pipeline pattern, `ExitOn: failure`). This
   ordering is what closes the cumulative-file window: the reconcile that
   seals a period always runs after a fetch+load that re-observed it (law 3).
   Each stage remains individually safe to run alone (all are idempotent);
   only *sealing freshness* depends on the ordering.
2. **Stale-sealed warning:** when the load replaces a touched bucket that
   lies **outside the ladder's lowest-rung open window** (possible only via
   genuinely late source data — never observed for databricks audit logs),
   the affected sealed/absorbed blocks do NOT auto-rebuild in v1. The load
   logs a `Warn` naming the bucket ("touched closed bucket … — ladder blocks
   covering it are stale; run solution-load-backfill + ladder rebuild to
   reconcile"). Automatic block invalidation is a noted follow-up (§9), not
   silently promised.

## 7. Failure modes → outcomes

| # | Failure | Outcome (law) |
|---|---------|---------------|
| 1 | Crash / interrupt mid-download | `.partial` never visible to load; resumed or re-downloaded next run (5) |
| 2 | Cumulative `PT1H` grows after fetch | Size mismatch next run → re-download → bucket touched → partition replaced (3) |
| 3 | Blob still growing when its hour "closes" | Grace window + fetch→load→project ordering re-observes it before sealing (3, §6.1) |
| 4 | Prefix listing fails | Horizon held; window re-listed next run; no silent gap (4) |
| 5 | Some downloads fail | Horizon held; paths in `failing`, retried + `Warn` each run (4) |
| 6 | Load crashes between bucket swaps and manifest write | Next run re-derives ⊇ touched set, re-replaces; replace is idempotent (1, 2) |
| 7 | Fetch state / load manifest corrupt or missing | Full listing / full rebuild + `Warn`; state regenerated (6) |
| 8 | Inbox file pruned upstream | No action — lake keeps its rows (inbox is transport, §5.2); a later touch of a bucket missing a contributor skips with `Warn` (§5.3) |
| 9 | Genuinely late data into a sealed period | Bucket replaced in lake; ladder `Warn` + manual backfill in v1 (§6.2) |
| 10 | One container broken (malformed JSON etc.) | Per-dataset error aggregation (S-1568): others load, run reports the failure, failed dataset's manifest entries held (4) |
| 11 | Concurrent runs of the same config | Out of scope: the jobs executor serializes per config; manual-beside-scheduled is operator error today (noted §9) |

### 5.6 Measured (2026-07-04, real fixture)

Verified against the 10.6 GB comply-fixture + solid-comply's decode SQL
(`docs/performance/2026-07-04-incremental-load-pipeline.md`): staged
incremental arrival converges to the backfill ground truth **cell-identical**
per (container, bucket); the steady tick drops **6.5 s → 0.28 s** on a 3 GB
inbox (and the old cost grows with history, the new one is O(diff)).

Decode-once staging (S-1615, same fixture,
`docs/performance/2026-07-04-decode-once-benchmark.md`): the 1949-file 5-day
delta tick drops **31.7 s → 10.0 s** and prime **19.8 s → 5.2 s**, still
cell-identical — the learning re-decode is gone; what remains is the one
unavoidable decode plus staging IO. Bulk catch-ups beyond tens of thousands
of files: backfill remains the right tool.

## 8. Acceptance tests (the contract's teeth)

Fetch (S-1611): horizon advances only on clean runs · failed listing window
re-listed next run · grown blob re-downloaded · missing/corrupt state = full
listing · prefix narrowing lists only `[horizon, now]` windows.

Load (S-1612): unchanged inbox → no-op (zero DuckDB work) · new file → only
its bucket(s) replaced, others byte-untouched · grown file → its buckets
replaced, no double-count · pruned file → lake untouched; a touch of a
bucket missing a contributor skips with `Warn` · corrupt manifest → full
rebuild, correct counts · backfill primes the manifest · collision guard +
per-dataset aggregation regressions stay green.

Legacy mode (S-1608): append accumulates (A then +B ⇒ A+B) · idempotent
re-run (A+B stays A+B) · changed file degrades to full rebuild with `Warn` ·
`LoadModeFull` byte-identical.

End-to-end: the §12 worked pipeline (fixture) run twice back-to-back produces
identical row counts everywhere; run across an hour boundary with a grown
PT1H produces exactly the final file's rows, once.

## 9. Open questions / follow-ups (noted, not promised)

- **Compaction** of accumulated small parquet in legacy-mode lakes (S-1608
  design call 3): out of scope; revisit when read amplification is measured.
- **Automatic ladder block invalidation** for late-touched sealed buckets
  (§6.2): design exists in outline (blocks record `CoversFrom/To`; a touched
  bucket maps to covering blocks); build only if late data is ever real.
- **Load-request trigger (§4.9)**: the JetStream load-request replaces
  cron-shaped scheduling; the runnables' internals are unchanged by it.
- **`solution-load` default mode**: once incremental is proven under the
  campaign harness, whether the steady-state runnable defaults to
  manifest-diff (likely yes — it strictly dominates) and full becomes the
  explicit rescue alongside backfill.
- **Run-lock** for manual-beside-scheduled executions of one config (§7 row
  11).
- **Inbox retention**: pruning consumed inbox blobs is safe by construction
  (§5.2 — absent files are no-ops; the lake keeps their rows), but *policy*
  (when to prune, what "old" means vs the retention classes) is undesigned.
  Never prune inside the fetch grace window (§4.4) — a pruned-then-relisted
  blob would re-download.
- **Stream-shaped sources** (shipper-produced block files, e.g. a
  promtail-like agent writing per-stream incremental blocks with
  monotonic-within-stream event times): correctness composes with zero
  contract changes — lake bucketing is event-time-derived (§5.1), so
  unaligned multi-bucket blocks are the normal case, and monotonicity keeps
  the touched set at the head. **The solution is responsible for good fit**:
  emit date-shaped paths (`y=/m=/d=/h=`) so §4.2 prefix narrowing applies
  verbatim, map streams to containers (modest cardinality) or a column
  (high), and bound stream lag operationally — per-stream monotonicity is
  NOT global bounded lateness, and a stream lagging past the ladder's open
  lowest-rung window hits the §6.2 stale-sealed path. Platform follow-ups
  this workload would promote: **per-stream sequence cursors** as a second
  §4.2 narrowing strategy (laws 1/4 are shape-agnostic; only the prefix
  generator is time-specific), and §6.2 auto-invalidation.
- **DuckDB-as-WAL head block** (Prometheus-like): a writable in-flight
  DuckDB on the solid side receiving data *between* bucket boundaries,
  sealed to immutable parquet/blocks only on bucket close — sub-bucket
  freshness without violating the immutability laws (readers would union
  the sealed ladder + the head block, mirroring the growing-rung pattern).
  A capability for push/stream ingest where the file-drop model's latency
  floor (one fetch cadence) is too high. Vision-only; the file pipeline
  above stays the substrate.

## 10. Load declaration — who declares what (v0.2)

*(Ricardo, 2026-07-04. Owner: S-1613; implementation: S-1614. The pipeline
mechanics above are unchanged by this section — it pins where their
configuration LIVES and how a solution claims its source's physics.)*

### 10.1 The division of knowledge

> **The solution declares once, per variant, everything it knows about how
> its product stores data — the what and how we transform it. The source
> says where, and proves access.**

The rule that decides where a field belongs: **knowledge that crosses
customers ships in the solution artifact; knowledge that is per-customer
stays on the source config.** Azure Databricks writes
`resourceId=/…/y=/m=/d=/h=/PT1H.json` because that is what Azure Monitor
diagnostic export does — true at every customer on that variant, so it is
solution knowledge (the flywheel law: schema and physics cross customers,
data never). The storage account name, the credential, and which containers
exist are the customer's — they stay on the source.

```
solution (announced artifact, per VARIANT — azure now, aws later):
  layout:    path template / date-segment convention / bucket-evidence claim
  scope:     source_glob (the belt, S-1568)
  transform: decode SQL (events_decode), schema, labels, retention classes

source (operator-owned, per customer):
  where:     account/endpoint + container scope (allow-list)
  access:    credential + TestConnection (the proof)
  window:    until-date / caps (the bounded-backfill knobs)
```

### 10.2 The declaration (schema sketch)

The load-stage projection is already the de-facto load declaration
(`stage: load`, `source_glob`, decode SQL — solution-load is its only
consumer). v0.2 extends it with a `layout` block and a variant key:

```yaml
- id: events-decode
  stage: load
  source_kind: azure_blob          # the VARIANT key
  source_glob: "resourceId=/SUBSCRIPTIONS/*/…/MICROSOFT.DATABRICKS/**/*.json"
  sql: |
    SELECT … FROM read_json('{source}', …)
  layout:
    # Optional. LITERAL head → enables §4.2 fetch narrowing. A layout whose
    # date segments sit under varying components (multi-workspace
    # resourceIds) omits it or declares a glob-headed one — narrowing then
    # degrades to full listing (Warn), evidence below still works.
    path_template: "…/y={{.Year}}/m={{.Month}}/d={{.Day}}/h={{.Hour}}"
    # data (default) = learn buckets from rows (§5.1 — today's behaviour).
    # path = derive from the path's date segments; see 10.4.
    bucket_evidence: path
```

The authoritative artifact schema lands in `solid-sdk/contract` alongside
the other announce kinds; this section is its author-facing home.

### 10.3 Variant selection — no new binding machinery

A solution ships one load-stage declaration per source kind. Selection is
automatic at both consumers:

- **Fetch**: the job config already pairs `(solution, source)`; the source's
  KIND picks the variant. No new operator knob.
- **Load**: each variant's `source_glob` matches only its own layout, so the
  existing per-container scope check routes every container to its variant's
  decoder (`loadStageProjection` goes from first-match to
  glob-matches-this-container). A mixed estate (azure + aws mid-migration)
  works for free rather than being forbidden.

A first-class solution↔source binding registry is deliberately NOT
introduced — job-config pairing suffices until the §4.9 load-request
trigger needs the pairing without a job config in hand.

### 10.4 `bucket_evidence` — trust-but-verify, never trust-and-forget

An enum naming the EVIDENCE SOURCE (not a learn/trust attitude — future
values: `sequence` for per-stream cursors, `declared` for reference tables):

- **`data`** (default): §5.1 learning — per-file attribution from the
  decoded rows. Always correct, linear in changed files. With decode-once
  staging (S-1615, §5.1) the attribution rides the single decode, so `data`
  runs at ~2× the path-evidence floor instead of the pre-S-1615 4.4× —
  which narrows `path`'s payoff for any dataset that decodes at ingest.
- **`path`**: the file's bucket keys derive from its path date segments
  (read ANYWHERE in the path — a literal template is NOT required, unlike
  fetch narrowing; the two consumers of the layout have different
  strictness). Steady ticks skip learning entirely — with no decode stage
  at all, this is zero-touch placement.

  **The verify law:** `path` is a CLAIM, and claims are checked at the
  moments we already read everything — prime and backfill still run data
  learning and compare per file; any mismatch fails LOUDLY naming the file.
  A stray out-of-hour record is exactly the row the touched-set filter
  would otherwise drop silently (§5.3) — the one hazard `data` learning
  guards, so giving it up on steady ticks is only legal because every
  prime/backfill re-audits the claim. A file with no parsable date segments
  under `path` is a hard per-dataset error, never a silent skip.

The comply/databricks azure variant was EXPECTED to qualify for `path` by
construction (hour-scoped PT1H files, event time == path hour per Azure
Monitor's export contract) — and verify-at-prime DISPROVED that claim on
real data the day the engine shipped
(`docs/performance/2026-07-04-path-evidence-benchmark.md`): Azure Monitor
flush latency spills ~2% of records across the day boundary (a ±15–48 min
band, not a timezone bug), so **the variant stays on `data` evidence**. The
verify law worked exactly as designed — an optimistic claim was rejected
loudly at prime, before any steady tick could drop a row. With decode-once
(S-1615) `data` is fast enough that `path`'s remaining home is the
trusted-producer, no-decode case (e.g. an OTLP receiver that stamps the
path from the record's own timestamp — `docs/ideas/otlp-receive-endpoint.md`),
where the claim holds by construction and there is no decode to optimize.

### 10.5 What moves, what stays

- `FetchConfig.PathTemplate` (S-1611) is **removed** (S-1614, pre-prod
  no-compat-shims) — the declaration home moved to `layout.path_template`,
  resolved by `FetchRunnable`'s `LayoutResolver` seam keyed on the source
  KIND; the source keeps `containers`, `until_date`, and the caps (where +
  how much, never how shaped). Runtime plumbing (`FetchOpts`, horizon,
  narrowing, the literal-template law) is byte-unchanged. A glob-headed
  declared template degrades to full listing with a Warn (the runnable strips
  it; the engine's silent-stall rejection stays as defense-in-depth).
- `source_glob`, decode SQL, retention classes: already solution-shipped —
  unchanged, now named as parts of the load declaration.
- Out of scope here: binding registry (§10.3), sequence-cursor narrowing
  (§9), reference/SCD load kinds (the §11 strata of the design doc — a
  `load kind` field earns its place when the second kind actually ships).
