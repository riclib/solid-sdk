# Teaching-as-a-tool: the SDK ships its own builder + upgrader

- **Status:** Idea (vision-only, no ticket) — 2026-06-28
- **Where:** `solid-sdk` (the dependency every fork shares)
- **Origin:** the LVRTC fork-of-kit build surfaced the gap; Ricardo's framing.

## The problem it solves

The original Solid pitch had two pillars (kit README §3, §4):

- **§3 "the repo IS the SDK"** — every slice carries a `CLAUDE.md`, so the
  partner's Claude already knows how to extend it.
- **§4 "fork without the fork tax"** — every platform change ships *its own
  migration as a skill*, and Claude applies it on upgrade.

Both were quietly riding a **fork → upstream** relationship: you'd pull upstream
and get the improved `CLAUDE.md`s and the migration skills. But we settled that
the real model is **clone-and-own, not git-fork-track-upstream** (the module
rename alone kills `git merge`; capability rides the SDK *dependency*, not git
history — see the LVRTC build notes). That decision orphans both pillars:

- In-repo teaching (`CLAUDE.md`s describing *how to build a solid solution*)
  **snapshots at clone time and rots** — nothing refreshes it.
- Migrations have **no delivery path** — there's no upstream to merge them from.

## The idea

**Move the generic teaching + migrations out of static repo files and into a
versioned binary shipped by `solid-sdk`, driven by a skill.** Teaching becomes a
*queryable tool*, not a file that rots — exactly the `icon-search` pattern
(*"NEVER grep the icon source; use the CLI"*).

Because the binary is **versioned with the SDK** — the capability vector we
already chose — `go get solid-sdk@0.3.0` *is* the teaching update. It can't drift,
because it isn't a file someone forgot to refresh; it's the dependency.

### The clean split it creates
- **Generic platform/SDK teaching + migrations → the tool** (version-locked,
  can't rot, travels with the bump).
- **Solution-specific docs → stay in-repo** (e.g. `internal/<sol>/CLAUDE.md`
  documents *that* solution — the builder owns it; correct to keep local).

## Shape (strawman, à la icon-search)

A binary at `github.com/riclib/solid-sdk/cmd/<name>`, ideally invoked as
`go run …@<the version in the repo's go.mod>` so it's always the teaching for the
SDK the repo actually depends on. It `//go:embed`s the convention docs + a
migration set keyed by SDK version. Subcommands, roughly:

- `new solution <name>` — scaffold a solution package. (This is literally the
  by-hand work the LVRTC port did, reading conduct as a template — the proof the
  tool is real: it would have generated the `internal/servicedesk/` skeleton.)
- `migrate` — walk the repo from its current SDK conventions to the bumped
  version. The "fork without the fork tax" promise, delivered by the tool instead
  of `git merge`.
- `sync` / `teach` — (re)write the generic convention docs / install the
  skill-pack into the repo.
- `doctor` — lint the fork against current conventions.

And a **skill** ("building-solid-solutions"): the agent-facing half — *"to add,
extend, or upgrade a solution, use the `solid-sdk` tool; don't guess conventions
or copy a stale sibling."* One-for-one with the icon-search skill's contract.

### Distribution: the skill IS the install (Ricardo, 2026-06-29)
The skill **packages the CLI binary** and ships via the **Claude Desktop plugin
marketplace**. A builder installs "Solid" from the marketplace and is immediately
going — no separate CLI install, no PATH, no clone-first dance. Onboarding
collapses to: *install the skill → "start a new solution" → scaffold → build*.
This is the zero-setup front door the [builder README](../../README.md) opens
with, and it makes the marketplace the distribution channel for the on-ramp (the
runtime stays the licensed artifact — see [distribution-and-licensing](./distribution-and-licensing.md)).

## Command surface (strawman)

The fuller subcommand surface (Ricardo, 2026-06-28). It splits along the line the
idea already draws — **deterministic, embedded, version-locked** (pure Go the
agent *calls*, like icon-search) vs **Claude-driven** (the tool ships the logic +
a skill; the agent executes):

| Command | Kind | Notes |
|---|---|---|
| `solidsdk icon search` / `icon validate` | deterministic | `search` is the proven, already-shipped pattern; `validate` checks an icon name resolves before it reaches a build |
| `solidsdk skill scaffold` / `skill validate` | deterministic | `new solution` at skill granularity + a focused `doctor` |
| `solidsdk dashboard validate` / `workflow validate` | deterministic | the **executable form of the `docs/sdk/` contracts** — see below |
| `solidsdk dashboard smoke` | deterministic | `validate` + data: run every widget query against the solution's fixture dataset (`generate.sql`), catching what static validation can't — DECIMAL-in-chart scans, missing/renamed columns, empty-result degradation — see below |
| `solidsdk install skills` | deterministic | install/refresh the skill-pack into the repo (the `sync`/`teach` write) |
| `solidsdk claudemd update` | deterministic | (re)write the generic convention `CLAUDE.md`s from the version-locked teaching |
| `solidsdk migrate` | **Claude-driven** | walk the repo from its current SDK conventions to the bumped version — "fork without the fork tax", delivered by the tool, not `git merge` |

Only `migrate` (and any future `explain`) needs an LLM; everything else is offline
and deterministic. That's the icon-search contract generalized: the binary is the
authority, the skill just says *use it, don't guess*.

### `validate` is the contract, executable — the anti-rot answer

The `validate` family is the **executable twin of the `docs/sdk/` contracts**
(`dashboard-dsl.md`, `workflow-defs.md`). Those are prose, and prose rots: a
correctness pass on both (v4 S-1524, 2026-06-28) found the dashboard doc still
claiming "nothing implemented yet" long after the substrate shipped, a breaking
heatmap-axes change under-documented, dead worked-example paths, and a workflow
"goal seam" helper that never existed. A `solidsdk dashboard validate <file>` /
`workflow validate <file>` **cannot** drift the same way — it *is* the contract.

`dashboard smoke` is `validate`'s data-plane twin, and it restores a capability
the port-out lost: v4's in-tree revassure once had a `verify_test.go` that built
the fixture DuckDB and ran every dashboard widget query against it — the test
that caught the DECIMAL-in-chart bug. The fork has no equivalent (confirmed in
the S-1618 doc sweep, 2026-07-05): today no fork test executes widget queries
against data, so a query that validates but can't scan its result types ships
silently. As a CLI verb (`smoke <dashboard.yaml> --fixtures <generate.sql>`, or
repo-wide), every fork inherits the check at once and it slots into partner CI —
another case of the binary being the authority instead of a per-fork test
someone has to remember to copy.

And the validators **already half-exist**: v4's `infra/dashboard/validate.go` and
`domains/workflow/yaml.go` (`KnownFields(true)` strict decode) do this work today,
in-tree. Lifting them into `solid-sdk` makes the check runnable by third parties
and in CI, and reframes the open "where do the SDK docs live" question: the docs
become the **human-readable companion** to a machine-checkable surface, and
`claudemd update` keeps even that fresh. (This is the durable answer to the
`docs/sdk/` completeness gap — a parent announce/manifest doc that can rot vs a
`validate` command that can't.)

## Decisions leaning in
1. **Home = `solid-sdk`, not `solid-kit`.** Forks depend on the SDK, not the kit;
   capability must ride the SDK bump. The kit is just the initial clone.
2. **Teaching embedded in the binary** (offline, deterministic, version-locked)
   rather than fetched — like icon-search.

## Sequencing / non-goals
- **Not a demo blocker.** The LVRTC Monday demo needs none of this. This is the
  *durability* investment that makes clone-and-own sustainable.
- Natural 0.3.0-era work, alongside the `transport` consumer→fire-skill seam —
  and that seam would make a nice **first migration the tool ever ships** (dogfood).
- Vision-only for now: captured, not built. Place the stone when the upgrade pain
  is real, not before.
