# Solid SDK — Architecture & Wire Contract

> Internals reference for the SDK kernel. **Building a solution?** Start with the
> builder's guide in [`README.md`](./README.md) — this document is for working on
> the platform itself or integrating at the wire level.

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
  manifest.go   SolutionManifest (index), ArtifactRef, ToolDescriptor, SkillArtifact, PromptArtifact, WorkflowArtifact, DashboardArtifact, Solution (assembled)
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
<name>.skill.<skillID>       one skill per leaf
<name>.prompt.<promptID>     one prompt per leaf
<name>.workflow.<slug>       one workflow per leaf    (Body = workflow definition YAML)
<name>.dashboard.<pageID>    one dashboard per leaf   (Body = dashboard DSL YAML)
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

## Your quack connection (the connect op + the reconnect contract)

A solution bound to a workspace gets a quack connection to that workspace's
engine — full SQL, server-side execution, statement-logged (the platform's
`docs/sdk/solution-stores.md` is the normative doc). You never configure an
engine address or token; you ask the platform over the store-proxy wire
(S-1721 platform side, S-1728 this mirror):

| | |
|---|---|
| Subject | `solid.store.call.<solution>.connect` — the store-call family; the S-1706 per-account publish prefix covers it, no extra grant |
| Request | `{"solution":"<name>","workspace":"<slug>","op":"connect"}` — NO `store`/`statement`/`args`; smuggling any is the uniform denial |
| Grant | the workspace **binding IS the grant** (the workspace draws your solution); there is no store hop |
| Reply (granted) | `{"uri":"quack:localhost:<port>","token":"<per-boot-token>","tls":false,"duration_ms":N}` |
| Reply (denied) | `{"error":"not granted","code":"not_granted"}` — byte-identical for unknown workspace / not bound / malformed (no existence leak) |
| Reply (engine down) | `{"error":"workspace engine unavailable","code":"exec_failed"}` |
| Transport failure | Go error at the caller — policy (`code`) vs outage (error), same split as `StoreCall` |

```go
res, err := transport.QuackConnect(ctx, nc, "revassure", "lmt")
// err != nil            → transport outage (platform down); retry/backoff
// res.Code != ""        → policy denial or engine failure; read res.Code
// otherwise             → connect a quack client to res.URI with res.Token
```

**The reconnect contract.** `token` is minted **per engine boot** and engines
are disposable by design — an LRU eviction, a compaction, a platform restart,
or a crash retires the engine, and the next boot mints a new token (usually on
a new port). Auth failures or connection refusals on an established handle are
the normal signal, not an incident. Treat `{uri, token}` as per-session state:
on ANY connection failure, re-run `transport.QuackConnect` and reconnect. Do
not persist handles across your own restarts, do not share them between
processes, do not backoff-forever (the re-handshake is cheap — the engine
re-boots in ~73ms on first touch), and **never log the token**. In-flight
statements on a retired engine fail; make your writes idempotent (upsert
shapes — `INSERT OR REPLACE`, `MERGE INTO`).

**Opening the connection — the `quack` package (the paved road).** The SDK
ships the engine client: pinned `duckdb-go` (lockstep with the platform's
go.mod) plus the quack + httpfs extensions, committed SHA-pinned
(`quack/extensions.lock`), embedded via `//go:embed`, and materialized at
first use — the runtime never touches the network. One call owns the whole
contract:

```go
conn, err := quack.Connect(ctx, nc, "revassure", "lmt")   // handshake inside
if err != nil { ... }                                     // outage OR denial — no Conn without a grant
defer conn.Close()

// statements ship server-side, marker-prefixed automatically:
err = conn.Exec(ctx, "INSERT INTO revassure.interestingevents SELECT ...")
rows, err := conn.Query(ctx, "SELECT ... FROM revassure.interestingevents")
```

`Conn` applies the reconnect contract for you: on a failed statement it
re-runs the handshake as a boot-identity probe — a NEW `{uri, token}` means
the engine was retired, so it reconnects and retries once, invisibly; the SAME
handle means the failure was the statement's own, returned as-is (no blind DML
retry, no error-string matching). It also drives `disable_ssl` from the
reply's `tls` field (loopback/plaintext today; when the platform flips `tls`
true the client speaks SSL with no wire change) and never logs or persists the
token. `transport.QuackConnect` stays public as the raw handshake for callers
composing their own client.

**Your statements are logged** (the peek doctrine: observable, not prevented).
Attribution is engine-grain; prefix every statement with
`contract.StatementMarker(<your-solution-id>)` — the `/* solid:solution=<id> */`
comment survives verbatim into the statement log and gives the operator
statement-level attribution.

## Next wires (each lands with a consumer)

1. **Skill loop (control plane first)** — the skill leaf already round-trips
   (`SkillArtifact`, `TestSkillWire_RoundTrip`). NEXT: v4 imports the SDK,
   consumes assembled `Solution.Skills`, and registers them so the agent loop
   sees them (the richer version materialises into the workspace gitstore — the
   seed step). A skill is pure content (no data plane), so this is the clean
   first real cross-repo integration. The **tool** execution path waits on the
   data plane (a solution reads via quack, publishes changes over NATS) — the
   tool *mechanism* is already proven with a stub; `revassure_query` is a data
   tool, blocked on quack.

   **v0.4.0 (S-1587, contract landed; v4 harness consumer pending):**
   `SkillArtifact` gained `Queries []SkillQuery` (named queries the v4 harness
   runs once at skill activation against the workspace data session, results
   injected into the skill's context block — design:
   `v4` repo `docs/design/skill-named-queries.md`), `Active *bool` (nil =
   active; an explicit `false` lets a skill ship disabled/opt-in — the S-1564
   deep-audit finding), and `Parameters []SkillParameter` (RESERVED for the
   S-1590 general skill-parameter enhancement — defined on the wire now, no v1
   consumer reads it, so the wire needs only one version bump for both). All
   three are additive/`omitempty`; a v0.3.0 producer's announce still parses
   unchanged. Round-trip: `TestSkillWire_QueriesActiveParameters_RoundTrip`,
   `TestSkillWire_ActiveNil_MeansActive`. **The v0.4.0 tag is not yet cut** —
   consumers pin the commit until Ricardo cuts it.
2. **Declarative artifact leaves** — the prompt, workflow and dashboard leaves
   now round-trip (`PromptArtifact` / `WorkflowArtifact` / `DashboardArtifact`,
   `TestDeclarativeArtifacts_RoundTrip`): each is pure control-plane content
   (`Body` carries the prompt text / workflow definition YAML / dashboard DSL
   YAML), assembled back onto `Solution.Prompts/Workflows/Dashboards`. NEXT:
   a v4 consumer parses each Body with announce-time validation (a bad partner
   artifact greys out the solution, never panics the platform).
3. **The answer/notary spine** — committed answer → JetStream (durable,
   sequenced), the seam Bulletproof's hash-chain reads from.
4. **Bundled-infra extraction** — move v4's dashboard-DSL / markdown / charts /
   quack-shaping here as shared packages (the second half).
