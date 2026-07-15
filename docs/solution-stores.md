# Solution stores — the SDK contract

**Status:** DRAFT v0.2 — written 2026-07-14 out of the quackdb round-2 design
session (S-1715); v0.2 amendments 2026-07-14 from the S-1722 validation
(Mode 2 `maintained: solution` is BUILT and validated end to end on the
salesintegrity rebuild — see the Mode 2 section's implementation notes). This
is the solution-author-facing contract: how your solution's data is declared,
stored, served, and governed on a Solid estate. The platform implementation
is `infra/quackdb` (see its README); the design record is
`docs/design/quackdb-workspace-store.md`.

## The one-paragraph model

Every workspace is served by one DuckDB engine. Your solution never touches a
database file and never runs DDL — **the catalog declaration is the contract**:
you declare tables, the platform mints and serves them, and your data appears
in the workspace query surface as `<solution>.<table>`, joinable with
everything else the workspace can see. There are two maintenance modes, one
knob apart: the platform can run your data through its full pipeline
(landed, sealed to the immutable lake, projected, retention-managed,
GDPR-erasable — `maintained: platform`), or it can mint you a store that you
own the contents of (`maintained: solution`).

## Laws you cannot opt out of

1. **No DDL.** Schema comes from your declaration; the platform runs the DDL
   at creation and runs migrations at solution upgrade. This is doctrine, not
   a mechanical block (your connection could physically run DDL): DDL outside
   the declaration voids the migration and erasure contracts, risks wedging a
   serving engine shared with the customer's whole workspace, and is on you.
   Declare your tables; fill them however you like.
2. **No files.** You never open a platform database file. Your access is a
   quack connection per bound workspace (below); bulk loads ride
   `solution-load`.
3. **Declared-only exposure.** The declared table list IS the served surface.
   Undeclared physical tables are not served; a `StoreView` may mint a served
   table from a transform.
4. **One writing solution per store.** The store you declare is yours to
   write; concurrent foreign writers do not exist by construction.
5. **Write only your own tables.** Platform-maintained tables are written by
   the platform's funnel exclusively; the daily gen/count parity check
   detects foreign rows. Your connection is statement-logged (the peek
   doctrine: observable, not prevented).

## Your connection

A solution bound to a workspace gets a quack connection to that workspace's
engine — full SQL, server-side execution, statement-logged. This is the
performance contract: cross-catalog compute runs at engine speed, in place.
The canonical pattern is the **derived catalog**:

```sql
-- declared table revassure.interestingevents, filled at engine speed:
INSERT INTO revassure.interestingevents
SELECT * FROM solid.events WHERE <interesting criteria>;
```

One statement, no data over the wire, joining platform-managed catalogs with
your own. The binding is the grant: you reach engines of workspaces you are
bound to, nothing else. Estates may enable an optional hardened grade
(sandboxed solution access: OS-read-only lake, restricted SQL) — write your
solution to the doctrine above and the hardened grade never affects you.

### Getting your connection — the handshake (S-1721)

You never configure an engine address or token. You ask the platform, over
the same NATS trust boundary as the store proxy (S-1712):

- **Subject:** `solid.store.call.<solution>.connect` — the op segment is
  `connect`; the solution segment is your announced name and must match the
  payload (the same identity discipline as every store-proxy op; the S-1706
  per-account publish prefix `solid.store.call.<solution>.>` covers it).
- **Request** (JSON, core NATS request-reply):

  ```json
  {"solution": "revassure", "workspace": "lmt", "op": "connect"}
  ```

  No `store`, no `statement`, no `args` — a connect that smuggles any of
  them is denied. The **binding is the grant**: the named workspace must
  draw your solution; there is no store grant to ask for, and workspace
  engines never appear in a workspace's store-grant list.
- **Reply** on success:

  ```json
  {"uri": "quack:localhost:9601", "token": "<per-boot-token>", "tls": false,
   "duration_ms": 74}
  ```

  Connect with the quack client (`disable_ssl=>true` while `tls` is false).
  On denial/failure the reply carries `error` + `code` exactly like every
  store-proxy op: `not_granted` (unknown workspace, workspace doesn't draw
  you, malformed connect — one uniform message, no existence leak) or
  `exec_failed` (`"workspace engine unavailable"` — the engine could not
  boot; the platform log has the real reason). Transport failure (no
  responder, NATS timeout) is a Go error at your caller, distinguishing
  outage from policy.

### The reconnect contract

Engine tokens are minted **per engine boot** and engines are disposable by
design: an LRU eviction, a compaction, a platform restart, or a crash retires
the engine, and the next boot mints a NEW token (usually on a new port).
Your cached `{uri, token}` then stops working — auth failures or connection
refusals on an established handle are the normal signal, not an incident.

The contract: **treat `{uri, token}` as per-session state; on any connection
failure, re-run the connect handshake and retry.** Do not persist handles
across your own restarts, do not share them between processes, and do not
implement backoff-forever — the re-handshake is cheap (the engine re-boots in
~73ms on first touch). In-flight statements on a retired engine fail; your
writes are yours to make idempotent (upsert shapes — `INSERT OR REPLACE`,
`MERGE INTO` — are verified on the pinned engine).

### Your statements are logged

The peek doctrine, mechanically: every statement your connection runs is
recorded on the serving engine (statement text, timestamp, engine connection
id) and shipped to the estate's operational log, alongside the connect-grant
audit line (which solution asked to connect to which workspace, when). The
platform's operator can read both, joined. Two honest caveats, stated so you
never design around a fiction:

- Attribution is engine-grain (= workspace-grain): the engine does not know
  which caller a wire connection belongs to. **Prefix your statements with
  `/* solid:solution=<your-id> */`** — the comment survives verbatim into
  the log line and gives the operator statement-level attribution. The fork
  SDK client does this for you.
- The log is observability, not enforcement — consistent with your grade.
  The hardened grade adds enforcement; write to the doctrine and neither
  concerns you.

### Off-box estates — the TLS gap (honest limits, 2026-07-14)

The pinned quack extension **cannot serve TLS**: `quack_serve` accepts only
`token`/`allow_other_hostname`/`disable_ssl` (no cert/key), and its HTTP
server is compiled without SSL. The platform therefore **refuses to bind
engines on anything but loopback** — it will not serve tokens and tenant
data in cleartext off-box. What this means for you:

- Your solution daemon connects **same-box** (loopback) today. Fork daemons
  on a different box need the extension to grow server-side TLS first; when
  it does, the handshake reply's `tls` field flips true and your client
  connects with `disable_ssl=>false` — the wire shape does not change.
- The NATS handshake itself already crosses boxes fine (NATS carries its own
  auth/TLS); it is only the quack data plane that is same-box for now.

### Known hole, known on purpose

`allowed_directories` has no read-only flag, so a connection can in
principle `COPY TO` into lake paths its workspace is granted. Sealed lake
artifacts are signed — tampering is detectable, not prevented. Closing this
(OS-read-only lake, sidecar) is the optional hardened grade, deliberately
later. Doctrine for you: the lake is read-only; writing anywhere but your
declared tables is a contract breach, and it is logged.

## Naming

The served surface is **`<solution>.<table>`** — flat per solution. A
solution may declare many catalogs; they all compose into the one solution
namespace, and the catalog id never appears in a served name (it is a
packaging unit, not an addressing unit).

- **Table names must be unique across all of a solution's catalogs.** A
  collision fails the mount loudly, naming both catalogs — rename at author
  time.
- **Identifiers are sanitised:** any character outside `[A-Za-z0-9_]`
  becomes `_`, so solution id `databricks-compliance` is queried as
  `databricks_compliance.<table>`. Prefer ids that are already valid SQL
  identifiers.
- **Double underscore is reserved.** `<ns>__...` aliases are the platform's
  private mount names; do not declare table or catalog ids containing `__`.
- **Reserved words:** `workspace` and `all` are reserved catalog tokens; the
  `workspace` label key is the reserved projection-identity label.

## Mode 1 — `maintained: platform` (the pipeline)

Declare a catalog with a `storage:` block and the platform does everything:

```yaml
# catalogs/transcripts.yaml
id: transcripts
dialect: duckdb
storage:
  maintained: platform
  landing: wire            # wire | jetstream | bulk
  sign: true               # sign sealed lake artifacts
  keep: 24 months          # omit → keep everything (the default)
  expire: file             # file (month-drop, ≤1 month lag) | exact (daily boundary rewrite)
labels:
  - workspace              # the projection identity key
```

What happens to a row you write:

```
append (wire/jetstream/bulk)
  → landing            stamped with a generation number (gen) — the arrival clock
  → projection         your row appears in every granted workspace surface, at
                       ingest cadence (not seal cadence), with gen as lineage
  → daily seal         distributed by EVENT timestamp into lake parquet,
                       partitioned by (label-key, day) — the immutable record
  → monthly compaction day files → one month file per label partition,
                       ZSTD-recoded, re-signed
```

**The two clocks.** `gen` is *when we learned it* (projection cursor,
lineage, audit); the event `ts` is *when it happened* (lake partitioning,
retention, erasure targeting). Late-arriving data is not a special case:
high gen, old ts — workspaces see it immediately, the lake files it under
the day it happened.

What you get for free: rebuildability (every serving store is a disposable
projection of the record), retention enforcement (`keep`/`expire`, generated
policy text), GDPR erasure (the platform's erasure loop reaches your tables
because it knows your schema and your gen lineage), and a divergence check
(gen/count parity between landing, lake, and every projection).

**Serving latency classes** (measured, B1 2026-07-14): interactive
point-reads want a native projection (~2–4ms); scan/dashboard reads can be
served lake-window-only (~76ms point, ~250ms windowed aggregates over 55M
rows at floor config). Declare `projection: none` for scan-only catalogs and
skip the storage cost.

## Mode 2 — `maintained: solution` (your store, our file)

For operational state that is yours to manage — run ledgers, working sets,
staging you want queryable in the workspace surface:

```yaml
# catalogs/runs.yaml
id: runs
storage:
  maintained: solution
  scope: workspace         # workspace | estate (estate: declared, not yet served — see below)
  version: 1
  migration: migrate-runs-v2   # your job, required on any version bump
schemas:                   # the declared schema IS the DDL source —
  - name: main             #   the platform mints exactly these tables
    tables:
      - name: runs
        columns:
          - {name: run_id, type: VARCHAR}
          - {name: started_at, type: TIMESTAMP}
```

The platform mints the file from your declared schema and serves it in the
engine (`revassure.runs`); you write it directly over your workspace
connection. Everything else is yours:

- **No pipeline.** No gen, no seal, no lake copy, no parity check, no
  compaction schedule. The file is kill-9-safe (engine checkpoint
  discipline) but if it is lost, the platform owes you nothing — it is not
  in the record.
- **Lifecycle:** created at install per scope; dropped at uninstall.
  Visible (path, size, owning solution) in the estate maintenance screen.
- **GDPR:** declare an `erasure:` rule (SQL the platform runs per erasure
  request) or your store is recorded in every erasure audit artifact as
  *out-of-scope-by-declaration* — visible, never silent. If you store
  personal data, declare the rule.

### Implementation notes (S-1722, honest state as of 2026-07-14)

Validated end to end on the salesintegrity demo rebuild (13 declared tables,
60k rows loaded over the connection, serve parity against the previous
hand-built store, statement-log attribution, uninstall drop). Amendments the
build forced on the v0.1 draft:

- **Your tables are REAL writable tables, minted exactly from your
  declaration.** Because `INSERT INTO <solution>.<table>` must bind, the
  minted store file is mounted AS your solution namespace in the engine —
  not as views. Consequence: **at most ONE `maintained: solution` catalog
  per solution** (the namespace is one database). The general "many catalogs
  compose into one namespace" law still holds for your snapshot/pipeline
  catalogs; the solution store is the namespace they'd compose into, and
  more than one solution-store catalog is refused loudly at mount.
- **Declared identifiers are refused, never rewritten**: table/column names
  outside `[A-Za-z0-9_]`, leading digits, and the reserved `__` fail the
  mint naming both. Declared column types must be DuckDB type syntax.
  Columns mint without constraints (declared nullability is descriptive).
- **`scope: workspace` is built; `scope: estate` is declared-only** — a live
  estate-scoped file would be RW-mounted by several workspace engines at
  once (one-owner-per-live-file), so its serving shape is deliberately
  unresolved and the platform refuses it loudly at install rather than mint
  a contested file.
- **Install is idempotent, never a re-mint**: an existing store file is left
  untouched on re-announce/reinstall. A schema change is a `version:` bump —
  the migration road. The version-bump ORCHESTRATION (mint new → `old.` /
  `new.` mounts → your `migration:` job → swap → retire) is a documented
  platform seam not yet wired (the swap mechanic exists; nothing exercises a
  bump yet). A bump without `migration:` is already refused at declaration
  validation.
- **Re-runnable loads are your job**: DELETE-your-own-rows-first (plain DML)
  is the validated idempotent-loader shape.
- On-disk (operator-facing): the minted file is
  `data/solutions/<solution>/ws/<ws>/<catalog>.store.duckdb`, dropped by
  solution uninstall.
- **Second consumer validated (S-1723, 2026-07-14):** dq's `dq_rule_results`
  rebuilt on this contract (declared catalog + regeneration as DML over the
  connection), killing the last pre-doctrine side-door write into a platform
  store. Two honest notes from that build: (1) an IN-TREE solution declares
  its store the same way an announced one does — a `SeedSolutionCatalogs`
  call from its `OnRegister` hook, no parallel mechanism; (2) the
  derived-catalog `INSERT ... SELECT` runs at engine speed only when the
  source surface is MOUNTED in the workspace engine — platform per-catalog
  artifacts like the profiles store are not, so dq STAGES its rows through
  the connection (fine at rule-history volume). A fork solution deriving
  from its own declared catalogs (e.g. solidmon's runs store over its event
  tables) gets the true server-side pattern.

### Schema migration (version bump)

You ship the migration; the platform orchestrates:

1. You bump `version:` and ship a migration job.
2. Platform mints the NEW file from the new declaration. The serving alias
   stays on the old file. Pause your own writes — you are the sole writer
   and the one migrating; coordination is yours.
3. Your job runs with both mounts: `old.<table>` and `new.<table>`. Copy
   what you want to keep (`INSERT INTO new.runs SELECT … FROM old.runs`).
   Pure DML — the no-DDL law holds.
4. On success the platform swaps the canonical alias to the new file and
   retires the old one for a grace window. On failure: nothing happened —
   old stays canonical, the half-filled file is dropped, retry re-mints.

A version bump with no `migration:` ref is refused loudly at upgrade.

## Reading

One engine, one surface. `catalog_query` / dashboards / skills query the
workspace engine; your tables join freely with conversations, other
solutions' declared catalogs, and lake windows the workspace is granted.
Search is `LIKE` — no FTS. Cross-table SQL executes server-side
(`quack_query` ships text; there is no client-side ATTACH in your path).

## Bulk data

Unchanged: bulk reference feeds ride `solution-load` into the platform lake
(sibling: [`incremental-load-pipeline.md`](./incremental-load-pipeline.md)). The connection is for operational
writes and derived-catalog compute, not for pushing 100 GB through a protocol.

## What there is no API for

- Engines of workspaces you are not bound to; other estates; the generation
  table, seal manifests, or JetStream subjects.
- DDL (doctrine — law 1) and writes to platform-maintained tables
  (parity-checked, statement-logged).
- Writing another solution's store (one writing solution per store).
- Escaping the record: promoting your store's contents into a
  platform-maintained catalog happens through the landing road, gen-stamped
  — there is no side door.
