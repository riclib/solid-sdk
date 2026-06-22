# solid-sdk

The **kernel** of the Solid partner model: the wire contract a partner solution
and the Solid platform exchange over NATS / JetStream / KV, plus thin transport
helpers. Pure data types + nats.go glue — **zero dependency on the v4 platform.**

Both sides depend on this module (the dependency inversion): the platform (v4)
imports it to *watch* announced solutions and *route* tool calls; a partner
solution imports it to *announce* its manifest and *serve* its tools.

> **Status:** Phase 1 — the first wire. The `revassure_query` tool round-trips
> end-to-end over loopback NATS (`transport` round-trip test). This is the
> strangler proof: the same announce + serve + call the cross-process split will
> use, today in one process.

## Why this exists

In the partner-model architecture, partner and platform talk mostly over the bus
(NATS request-reply for tools, JetStream for the committed-answer/notary spine,
KV for capability announcement, quack for data). When the boundary is a bus, the
SDK is *mostly types + helpers* — which is exactly what this module is. Until the
boundary is a bus it can't be: the in-process registration API (func pointers,
live services) isn't a wire contract. So this repo grows **one real wire at a
time, each with a live consumer**, never a speculative type catalog.

See `docs/ideas/solution-layer-and-partner-model.md` (v4 repo), the "solution
contract" and "SDK has two halves" sections.

## Packages

```
contract/   pure wire types — no behavior, no deps beyond stdlib
  manifest.go   SolutionManifest (index), ArtifactRef, ToolDescriptor, Solution (assembled)
  envelope.go   ScopedIdentity, ToolCallRequest, ToolCallResult   (agent-as-lens tool call)
  subjects.go   key-tree + subject helpers   (subject shape = the authz boundary)

transport/  thin nats.go helpers over the contract types
  announce.go   EnsureSolutionsBucket, PublishSolution, WatchSolutions   (KV tree)
  serve.go      ServeTool   (partner side: request-reply responder)
  call.go       CallTool    (platform side: request-reply caller)
  roundtrip_test.go   embedded-NATS proof: full wire + KV-tree size guards
```

## Announce is a KV TREE, not one blob (the 1 MB rule)

NATS's default `max_payload` is 1 MB and a KV value is one stream message, so a
single-blob manifest carrying skill bodies + dashboard YAML would risk going
oversize. Instead a solution publishes a **tree**:

```
<name>.manifest              small: core meta + version + revision + artifact index
<name>.tool.<toolName>       one ToolDescriptor per leaf
<name>.skill.<skillID>       one skill per leaf       (lands with the skill wire)
<name>.dashboard.<pageID>    one dashboard per leaf   (lands with the dashboard wire)
```

The platform watches `*.manifest`; on a change it reads the referenced leaves
and assembles the solution. Two rules keep it consistent: **commit-last**
(`PublishSolution` writes leaves first, manifest last — so a watcher that sees
the manifest finds every leaf present) and **every change re-publishes** (the
manifest revision bumps on any edit, so a content-only leaf change is still
observed). No single value is ever the whole solution.

**The per-leaf cap is a tripwire, not a quota.** The real size governor sits
*downstream* of the wire and is far below 1 MB for every kind:

| Kind | Real limit | Natural size |
|---|---|---|
| skill / prompt | the LLM context window | tens of KB (a 1 MB skill is a context-breaker — broken, not big) |
| dashboard | declarative surface | tens of KB YAML+SQL. No raw-HTML passthrough (that's the real footgun — raw HTML is how base64 data-URIs / XSS / arbitrary embeds ride in, as Grafana's HTML panel lets you do). Images, when supported, come by object-store reference — the sanctioned path precisely because there's no HTML hatch to inline them through. |
| workflow | — | ~100 B per step |
| tool | the LLM tool schema | a few KB |

So `MaxArtifactSize` (~900 KB) exists to catch a *malformed* artifact early
(a context-breaking body, an inlined-blob dashboard) — `PublishSolution` rejects
it as something to fix, not to offload. Genuine binary blobs (documents /
attachments) are a separate, future artifact kind over the NATS object store,
never an escape valve for these declarative leaves.

## The two halves of the eventual SDK (and why only one is here)

Per the partner-model doc, the SDK splits along opposite economics:

- **Bus contract (this repo, greenfield).** New wire types + NATS/KV/quack
  helpers. No v4 code to mirror, so it's safe to author fresh here. This is what
  Phase 1 builds.
- **Bundled infra (later, an *extraction*).** markdown, charts/SVG, the
  dashboard-DSL parser+validator, quack table-shaping. These already exist in
  v4's `infra/` and **must be the same module both sides run** (else the partner
  validates v1 while the platform renders v2). So that half is moved *out of v4
  into here*, not rewritten — gated on actually needing it. Not in this repo yet.

## Design invariants (already encoded)

- **Agent-as-lens on every call.** `ScopedIdentity{identity, workspace, role,
  interactive}` rides on every `ToolCallRequest`. A solution gates against the
  envelope, never ambient state — there is none across the bus.
- **Subject shape is the sandbox.** `solid.tool.<solution>.<tool>`; a partner
  account is granted `solid.tool.<solution>.>`. Addressing and authz are one.
- **Tool failure is data; transport failure is an error.** A declined/failed
  tool returns `ToolCallResult.Error`; only a no-responder/timeout returns a Go
  error from `CallTool`.
- **Request-reply is live, not durable.** Tool calls are tied to the running
  agent loop (no JetStream on the input) — a lost turn is re-asked, never
  replayed. (The committed *answer* → JetStream is a separate, later wire.)

## Use

```go
// partner (solution) side
kv, _ := transport.EnsureSolutionsBucket(ctx, js)
_ = transport.PublishSolution(ctx, kv, transport.SolutionPublish{   // leaves + manifest, commit-last
    Name:  "revassure",
    Tools: []contract.ToolDescriptor{{Name: "revassure_query", /* ... */}},
})
sub, _ := transport.ServeTool(nc, "revassure", "revassure_query",
    func(ctx context.Context, id contract.ScopedIdentity, args json.RawMessage) contract.ToolCallResult {
        // enforce id gates, run the query, return Output + AccessCounts
    })

// platform side
_ = transport.WatchSolutions(ctx, kv, onPut, onDelete)        // onPut gets the ASSEMBLED contract.Solution
res, err := transport.CallTool(ctx, nc, "revassure", "revassure_query", req)  // route an agent tool call
```

```bash
go test ./...   # embedded-NATS round-trip, no external server needed
```

## Next wires (each lands with a consumer)

1. **Integrate the real revassure_query** — fork serves its actual executor over
   `ServeTool`; v4 grows a KV-watch consumer + a bridge translating
   `ToolCallResult` ↔ the platform's in-process `tools.Result`. First real
   cross-repo + v4-imports-sdk step.
2. **Bound declarative artifacts** — dashboard + workflow bindings in the
   manifest, with announce-time validation (a bad partner artifact greys out the
   solution, never panics the platform).
3. **The answer/notary spine** — committed answer → JetStream (durable,
   sequenced), the seam Bulletproof's hash-chain reads from.
4. **Bundled-infra extraction** — move v4's dashboard-DSL / markdown / charts /
   quack-shaping here as shared packages (the second half).
