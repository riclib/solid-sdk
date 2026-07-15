<!--
  ┌──────────────────────────────────────────────────────────────────────┐
  │  Store-Backed Catalogs — Declared-Only Exposure Contract              │
  ├──────────────────────────────────────────────────────────────────────┤
  │  Contract version : 1.0.0                                             │
  │  Status           : SHIPPED (S-1681 kind · S-1704 announce ·          │
  │                     S-1708 declared-only exposure)                    │
  │  Surface          : external — solution authors declare               │
  │                     store_backing + schemas in a catalog Body;        │
  │                     the platform serves EXACTLY that surface.         │
  │  Last updated     : 2026-07-13                                        │
  │  Owner ticket     : S-1708                                            │
  └──────────────────────────────────────────────────────────────────────┘
-->

# Store-Backed Catalogs — Declared-Only Exposure

A **store-backed catalog** is served by RO-attaching the solution's live
SQLite file and exposing namespaced views — no lake, no projection, no
snapshot. The solution's daemon writes the file (WAL mode, passive
checkpoints after every write burst); the platform reads it.

## The doctrine

**The solution is responsible for informing the platform of the richness of
its data.** It provides a catalog with rich table/column descriptions of what
it wants to expose to the agent and dashboards, and keeps that inventory
fresh and coherent. The platform validates, is resistant to solution
failures, and **never exposes more than the catalog declares**.

## The served surface (S-1708)

The catalog Body's declared schema table list **is** the served surface. For
each declared table, in order of precedence:

1. **A `store_backing.views` entry whose `table` matches the declared name**
   serves it via that view's SQL. The SQL references the attached file
   through the `{store}` token and may read **any** physical table —
   a view *mints* its served name (`process_status` over a physical `runs`
   table is the recommended shape). `views[].table` must itself be a
   declared schema table name — validated at announce, whole-catalog skip
   on violation.
2. **No matching view** = pass-through of the **same-named** physical table.
3. **Declared but physically missing** (pass-through source absent, or view
   SQL that fails to bind) = *awaiting-table*: the table is simply absent
   from the session — an ordinary "table does not exist" on query, never a
   session failure. It appears as soon as the daemon creates it.
4. **An undeclared physical table is never served.** The solution's disk
   layout is not the exposure policy.

Internal tables (`sqlite_*`, `_`-prefixed) are never pass-through-served,
even if declared — exposing one requires an explicit view.

## The peek doctrine

The declared surface is what the agent is **led to** — grounding describes
only declared tables — but it is **not an ACL**. The RO-attach alias is
reachable by arbitrary `catalog_query` SQL, so an agent (or operator) *can*
peek at undeclared physical tables; peeks are observable in tool-call logs,
not prevented (there is no practical in-process DuckDB ACL). Treat the
served file as a **published file**: a solution with genuinely private state
ships it in a separate, non-served database file.

## Producer checklist

- Every table you want served appears in the catalog `schemas:` with its
  **served** columns/types (what the view emits, not the raw storage shape —
  pin this with a test against your real store schema).
- Every `store_backing.views[].table` names a declared schema table.
- Store path: `<data_dir>/store/<file>` under the declared scope root
  (`scope: solution` or `workspace`); `file` is a bare filename — the file
  is the tenant boundary.
- WAL mode + `wal_checkpoint(PASSIVE)` after every write burst — the
  platform's reader sees only the checkpointed main file.
- Nothing private in the served file.
