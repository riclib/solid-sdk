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
