# SDK ships the engines — store/duck primitives, pinned DuckDB+SQLite, solid as first consumer

**Status:** CAPTURED 2026-07-12, deliberately NOT ticketed. **Sequencing decision:
the CART solution is built first as a private repo (plain SQLite, no SDK
dependency) to discover what the API really wants to be; the SDK extraction
happens from that proven shape, not from this sketch.** This doc preserves the
decisions and research so the extraction starts warm.

Origin: the v4 duck-tier retirement (2026-07-12, v4 PRs #836–#840) proved the
storage-class model empirically — SQLite serves live state, parquet lake is
evidence, DuckDB is a query engine over immutable files. CART
(`v4:docs/poc/kpi-platform/cart-process-status-feed-design.md`) is the second
consumer of the store-backed pattern, which by the promote-on-second-instance
rule makes this the right moment to design the promotion.

## The thesis

Ship the storage substrate — the CGO SQLite + DuckDB drivers, pinned, plus the
primitives that encode the engine laws — inside the SDK, with **solid itself as
the first consumer** (v4's streamstore/solutiondata adopt the same primitives).

Two reasons, in the founder's words:

1. **Partners inherit the integration work.** A year of hard-won substance:
   CGO toolchain + fts5 tag decisions made once; committed **air-gapped DuckDB
   extension loading** (never a network INSTALL — load-bearing for sovereign
   deployments); WAL + busy_timeout + one-writer discipline;
   checkpoint-per-burst (DuckDB's sqlite scanner never reads the WAL tail);
   `.tmp` → CHECKPOINT → atomic rename for every native duck file;
   `MaxOpenConns(1)` on writers; the `-shm` permissions trap; COPY ≫ Go loops.
   Every bullet is an evening a partner doesn't lose.
2. **The primitives lead partners into building solid-like solutions.** They
   are opinions with a shape: one writer + many readers, live state in SQLite,
   immutable derived files, canonical paths, checkpoint freshness. A solution
   built on them is mountable/checkable/dashboard-able/agent-queryable by solid
   on day one — structurally first-party. The API simply doesn't offer the
   designs we can't compose with (e.g. "append to DuckDB live", which fails
   other readers: a RW holder blocks even READ_ONLY attaches — verified).

## Primitive sketch (to be corrected by the CART build)

```go
// solidsdk/store — live state (SQLite)
st, _ := store.OpenSolution(dataDir, "cart", "cart.sqlite")   // canonical path, dir+perms, WAL, busy_timeout
st, _  = store.OpenWorkspace(dataDir, sol, ws, file)          // file-per-tenant scope
st.Burst(ctx, func(tx *sql.Tx) error { ... })                 // txn + wal_checkpoint(PASSIVE) on exit

// solidsdk/duck — immutable derived files + reads
duck.BuildImmutable(path, func(db *sql.DB) error { ... })     // .tmp → CHECKPOINT → atomic rename; STORAGE_VERSION 'v1.0.0'
duck.AttachSQLiteRO(db, path, alias)                          // the read side of the engine law
```

Cut at the **primitives, never at streamstore** — the conversations fold and
bit vocabulary are v4 domain logic and must keep refactoring freedom; the SDK
gets only the engine laws as code. `solidsdk validate` gains rules for the
declaration side (`store_backing` scope/file bare-filename, table naming — no
`_`-prefix/`sqlite_*`, unique per solution).

## Compatibility policy (decided 2026-07-12)

- **SDK major version IS the compatibility gate.** No separate storage epoch:
  a storage-format break ⇒ SDK major bump. API-only breaks also burn a major
  and also gate — acceptable, since solutions recompile against the SDK anyway
  and breaking releases ship migration skills.
- **Enforcement:** version handshake at solution announce; an incompatible
  solution goes **greyed-out with a stated reason** in the UI (reuse
  `FilterAvailable`'s grey-out), never a mount error at runtime.
- **Conservative engine upgrades:** engine bumps are SDK releases, never
  incidental `go get -u`; a format-breaking upgrade needs a named reason
  (feature/fix/CVE — "staying current" is not one); **reader leads, writers
  follow** (solid deployments upgrade before partner solutions rebuild).
- **Pin the write format in code:** `duck.BuildImmutable` writes
  `STORAGE_VERSION 'v1.0.0'` explicitly — decouples engine version from file
  compatibility for the whole 1.x line.

## DuckDB storage-format research (2026-07-12, cited)

Bottom line: **the major-gate policy costs ~0 forced majors/year.**

- Internal storage version bumps ~once per minor (64=v1.0/v1.1 → 65=v1.2 →
  66=v1.3 → 67=v1.4 → 68=v1.5), **but v1.0–v1.5 all write the v1.0.0 format
  (64) by default** — newer formats are opt-in (`STORAGE_VERSION 'latest'`) or
  feature-forced (at-rest encryption ⇒ 67; new types/compression per the 0.10
  forward-compat exclusions).
- **Backward read (new reader, old file): guaranteed since v1.0** ("DuckDB
  files created with DuckDB 1.0.0 will be compatible with future DuckDB
  versions"). **Forward read: best-effort only, explicitly not guaranteed**
  (fails with `Serialization Error`). Pre-1.0 broke nearly every release; that
  era ended at v0.10 (2024-02) by design.
- Extensions (incl. `sqlite_scanner`, which IS solid's store-mount read path)
  are ABI-keyed to the exact DuckDB version — every engine bump needs matching
  extension binaries; the committed air-gapped extension set already handles
  this per-version and becomes something partners inherit.
- WAL travels with the format: never hand an un-checkpointed native file to an
  older reader (the `.tmp`+CHECKPOINT+rename discipline already satisfies this).
- Sources: duckdb.org/docs/current/internals/storage; the 0.10.0, 1.0.0, 1.2.0
  (STORAGE_VERSION), 1.4.0 (encryption) announcement posts;
  duckdb.org/2025/11/19/encryption-in-duckdb; ATTACH docs;
  motherduckdb/sqlite_scanner.

## Obligations accepted with the thesis

1. **The paved road must be the easy road** — `store.OpenSolution` must be
   fewer lines than doing it wrong, from the first commit, or partners route
   around it and the opinions evaporate.
2. **We own the substrate's version story** — release notes state engine
   versions; a partner's storage bug is our support ticket.
3. Publishing `OpenSolution/OpenWorkspace` makes the canonical path layout
   (`data/solutions/<sol>/store/...`) a **published contract** — the forcing
   function for v4's conversations file relocation (storage-classes doc §6),
   so solid isn't the one exception to the layout its own SDK advertises.

## Extraction checklist (when CART's private repo has settled the shape)

1. `solidsdk/store` + `solidsdk/duck` with pinned engines + air-gapped
   extensions (weigh module split so skill-only forks don't compile DuckDB).
2. v4 adopts the primitives in streamstore/solutiondata — solid as first
   consumer (mechanical, strangler-style).
3. v4 announce path: `store_backing` parse in `toCatalog` (today it maps only
   dialect/grounding/labels/retention — a fork CANNOT declare a store-backed
   catalog over the bus yet), the SDK-major compat handshake → greyed-with-
   reason, and one boot log line per store-backed catalog
   (mounted/awaiting-source/REJECTED+reason — the resolver's silent skip is
   hostile to strangers).
4. `docs/sdk/solution-data-stores.md` — reduced to the storage-class decision
   tree ("is your store the only copy of something non-derivable?" — if yes,
   you owe an evidence path like conversations' lander/check/restore; if no,
   re-fetch is your DR, like CART), the engine law stated once, the
   compat/upgrade policy, and CART as the worked example.

## Related

- `teaching-as-a-tool.md`, `distribution-and-licensing.md` — the SDK-as-product
  siblings; this doc adds the runtime-substrate tier beneath them.
- v4: `docs/design/conversation-storage-classes.md` (the law),
  `docs/design/conversations-duck-tier-retirement.md` (the execution),
  `docs/poc/kpi-platform/cart-process-status-feed-design.md` (the first
  partner-shaped consumer).
