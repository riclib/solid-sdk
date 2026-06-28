# Distribution & licensing: license the runtime, liberate the on-ramp

- **Status:** Idea (vision-only, no ticket yet) — 2026-06-28
- **Where:** `solid-sdk` + the website (download/docs surface) + the `solid` runtime license
- **Origin:** Ricardo, riffing out from [teaching-as-a-tool](./teaching-as-a-tool.md)
  — started as "GitHub isn't great for sharing a few repos with partners," landed
  on a cleaner model once the audience was named.
- **Timing:** the ~**2026-07-12** window (≈ two weeks out), *after* the comply/dq
  external-solution port. The port is the dogfood that proves the on-ramp before
  external eyes hit a download page.

## The problem it started as

The SDK is on (private) GitHub. Sharing it with external partners means either
outside-collaborator access per repo (GitHub-account-coupled, `GOPRIVATE` + git
auth friction, exposes full repo+history) or a public repo (frictionless `go
get`, but source is world-readable — fights a proprietary license). Neither maps
to "share *these* repos with *these* parties under *these* terms."

## The reframe: it's not one decision, it's audience × layer

**Audience is three tiers** — and both downstream tiers get the **same**
`solid-kit` + SDK; only the *rights* differ:

| Tier | Gets | Rights | Value capture |
|---|---|---|---|
| **Us** | everything | — | — |
| **Partners (ISVs)** | kit + SDK | build **and redistribute** solutions as their own product | their solution is **inert without `solid` installed** → the runtime is the hook |
| **Customers** | kit + SDK | build + extend **internally only**, no redistribution | also need a licensed `solid` to run anything |

The common thread across both downstream tiers — *"only works with solid
installed"* — **is the entire licensing engine.**

## The consequence: license the runtime, liberate the on-ramp

Because nothing built on the SDK or kit *runs* without a licensed `solid`:

1. **The SDK and kit don't need to be gated.** They can be a public download +
   `scaffold` + docs. The moat is the **runtime + platform IP**, not the contract
   types or the scaffolding. → the GitHub-sharing headache dissolves; you stop
   scoping *repo access* and start scoping *redistribution rights* in the EULA.
2. **Partner vs customer is a contract clause** (redistribution yes/no), **not a
   different artifact, repo, or access mechanism.** One distribution, two EULAs.
3. **Self-hosting (Gitea/Forgejo) becomes a nice-to-have** (sovereignty narrative
   — Riga base, LVRTC/Latvia), not a *need* for access control.

The clean statement: **SDK + kit = frictionless on-ramp; `solid` (the runtime) =
the licensed, value-capturing artifact.**

### The one open question this hinges on
**Which parts of `solid` itself are open vs commercially licensed?** The
"open agent / closed notary" split lives exactly here — it decides whether the
runtime license covers the whole platform or just the closed pieces. Resolve this
before the EULA text, because it defines what "licensed solid" even means.

## The download surface (the visible deliverable, ~07-12)

One page, three artifacts that reinforce each other:

- **The `solidsdk` binary** — the [teaching-as-a-tool](./teaching-as-a-tool.md)
  CLI: `scaffold` (a partner/customer's *first* move — `solidsdk new solution`,
  not "read a stale CLAUDE.md and copy a sibling"), `validate`, `migrate`, etc.
- **Human-readable docs** — the convention guide + the `docs/sdk/` contracts
  (dashboard DSL, workflow defs, and eventually the announce/manifest contract).
- **Machine-readable docs** — the *same* contracts as schemas (JSON Schema for the
  dashboard/workflow YAML, the manifest shape). This is the **same artifact** as
  the `validate` surface: a partner's tooling, CI, and Claude all consume one
  source of truth instead of prose that rots. Human page + machine schemas + the
  binary = one coherent download surface.

## A cheap hedge worth doing regardless of hosting

Put the SDK behind a **vanity import path** — `go.solidreason.com/solid-sdk`
(a one-line `<meta name="go-import">` on a static page) instead of
`github.com/riclib/solid-sdk`. Then the host can move (GitHub → Gitea → wherever)
**without partners ever changing an import.** Decouple the import path from the
host *before* the first partner pins the GitHub path.

## Decisions leaning in
1. **License the runtime, not the SDK/kit.** Value capture at `solid` install;
   SDK + kit are the open on-ramp.
2. **One distribution, two EULAs** (partner = redistribute; customer = internal).
3. **Machine-readable contracts are first-class**, published next to the human
   docs and identical to the `validate` surface.

## Non-goals / caveats
- **Not now.** The ~07-12 window, after comply/dq port — that port tells us where
  the on-ramp actually bites before we expose it.
- **License *text* + partner contract terms need a lawyer.** This doc is the
  mechanics and the model, not the legal instrument.
- **SDK source-availability** (public permissive vs source-available like
  BSL/PolyForm) is a separate sub-decision — lower stakes once the runtime is the
  moat, but still a choice. Defer until the runtime-vs-open split (above) is set.

## Related
- [teaching-as-a-tool](./teaching-as-a-tool.md) — the `solidsdk` binary this
  distributes; `scaffold`/`validate`/`migrate` are the on-ramp's verbs.
- v4 `docs/sdk/` (dashboard-dsl, workflow-defs) — the human contracts whose
  machine-readable twins ride the download surface.
