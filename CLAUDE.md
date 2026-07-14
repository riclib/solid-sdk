# solid-sdk — agent guide

The kernel of the Solid partner model: wire-contract types + thin NATS/KV
helpers. Read `README.md` for the why; this is the working guide.

## Invariants — do not break these

- **Zero v4 dependency.** This module must never `import "v4/..."`. Both v4 and
  the partner fork depend on *this* module, not the reverse. If you find yourself
  wanting a v4 type here, that's the signal to define the wire type here and have
  v4 adopt it — not to import v4.
- **`contract/` is pure data.** No behavior, no deps beyond stdlib (`encoding/json`,
  `fmt`). Every field is JSON-tagged and must marshal identically on both sides.
  Behavior/transport goes in `transport/`.
- **Grow one real wire at a time, each with a consumer.** Don't pre-write a type
  catalog for wires nothing serves/calls yet. A type with no consumer is
  speculative and will be wrong. The round-trip test is the bar: a new wire ships
  with an end-to-end test proving announce/serve/call (or publish/consume).
- **Additive-only once a partner forks.** Signatures and JSON shapes freeze the
  day the first partner builds against them. Add fields (optional, `omitempty`),
  never repurpose or remove. This is the reconciliation/versioning contract.
- **Never expose a primitive that escapes scoping.** No raw `*sql.DB`, no file
  paths, no unscoped connections — only the scoped envelope + typed
  request/result. The DPO promise lives in this rule.
- **Announce is a KV TREE, never one blob.** NATS payload cap is 1 MB and a KV
  value is one message. The manifest is a small index (`<name>.manifest`); every
  artifact is its own leaf (`<name>.<kind>.<id>`). Do NOT fold skill bodies /
  dashboard YAML into the manifest — they'd blow the cap. `PublishSolution` is
  commit-last (leaves first, manifest last) and re-publishes on every change
  (revision bump) so `*.manifest` watchers stay correct. The per-leaf cap
  (`MaxArtifactSize`) is a TRIPWIRE for a malformed artifact, not a quota: the
  real size limit is downstream (a skill/prompt is bounded by the LLM context,
  far under 1 MB; a dashboard is tens of KB unless someone inlines a base64
  image; a workflow is ~100 B/step). Oversize = a broken artifact to fix, NOT a
  blob to route to the object store — the object store is a separate future
  *document* kind, never an escape valve for these declarative leaves.

## Logging — every solution logs via `solid-sdk/log` (S-1462)

Logging is **baseline**: every solution does it, so it lives IN the core SDK
module as a normal package (`solid-sdk/log`), NOT a separate Go module. The
pretty-printer deps (`dusted-go/logging`, `phsym/console-slog`) landing on the
core module is fine and intended — and the package is CGO-free.

**Logs do NOT travel the bus.** The NATS log handler was removed: shipping logs
over NATS needed a drop-on-full volume guard precisely *because* a message bus is
the wrong transport for a log firehose. Logs go to the solution's local sink
(stdout → journald/file); the bus carries only control-plane events (announce /
trigger / result / heartbeat), each low-rate with a real consumer. If logs are
ever wanted centrally, that's a Loki-style ingestion concern reading the disk
sink — not a bespoke wire. The `Field*` keys below still earn their place: they
make a partner record correlate with a platform record wherever both are read.

- **Use `log.Pkg(domain, pkg) *slog.Logger`** once per package at startup and
  store the result (`var log = sdklog.Pkg("partner", "announce")`). `New(cfg)`
  builds the app/nats/http loggers from `Config`; `DefaultConfig()` is the
  starting point. The platform (v4) consumes the SAME package via its
  `infra/logging` thin delegator (type aliases + func wrappers) — identical
  dev/prod behavior on both sides.
- **Field-key constants are a frozen, shared vocabulary** (additive only). Use
  them so a partner record correlates with a platform record on identical keys:
  `FieldSolution = "solution"`, `FieldRevision = "revision"`,
  `FieldWorkspace = "workspace"`, `FieldCorrID = "corr_id"`. The solution
  template wires these on.
`nats.go` and `nats-server/v2` are pinned to match v4's go.mod (`v1.48.0` /
`v2.12.2`). When v4 bumps them, bump here in lockstep — a skew breaks the
eventual shared build.

## Quack — the SDK ships the engine client (S-1728)

**One module, CGO in — decided 2026-07-14.** The SDK is basically useless
without quack, so the engine client is a normal package (`solid-sdk/quack`),
not a separate Go module: most partners start from solid-kit, which carries
the build tooling, and submodule complexity buys nothing. (This supersedes the
earlier "separate modules for CGO cost" doctrine;
`docs/ideas/sdk-ships-the-engines.md` remains the wider store/duck-primitives
future.)

- **Pins are a wire contract, lockstep with the platform.**
  `duckdb-go/v2` (go.mod) and the quack/httpfs extension SHAs
  (`quack/extensions.lock`) are copied from the platform repo's
  `infra/duckdb/extensions/extensions.lock` — the client and the serving
  engine must speak the same quack protocol and extensions are ABI-keyed to
  the exact DuckDB version. When the platform bumps DuckDB, bump both here in
  the same change, never incidentally. Same rule as the NATS pin above.
- **Air-gapped by construction.** Extension binaries are NOT committed;
  `scripts/duckdb-fetch.sh` stages them at BUILD time (SHA-verified against
  the lock), `//go:embed` bakes them into the consuming binary, and the
  runtime materializes them to disk on first connect. There is deliberately
  NO network INSTALL fallback — a failure to stage is a build error, never a
  runtime download.
- **`quack.Conn` is the paved road**: handshake (never a configured
  address/token), reconnect contract (re-handshake as a boot-identity probe:
  retry once only when the platform hands back a NEW {uri, token}; a failure
  on an unchanged handle is the statement's own — no blind DML retry, no
  error-string matching), statement marker on every statement, `disable_ssl`
  driven by the reply's `tls`. The token is never logged and never persisted.

## Testing

```bash
scripts/duckdb-fetch.sh   # once per checkout/arch: stage the pinned quack+httpfs extensions
go test ./...             # embedded JetStream NATS server + a real in-process quack engine
```

The round-trip test (`transport/roundtrip_test.go`) starts an in-process
JetStream server (mirroring v4's embedded pattern) and exercises the full wire.
New transport helpers extend it (or add a sibling) with the same shape.

## Adding a wire

1. Define the wire type(s) in `contract/` (pure data, JSON-tagged, documented).
2. Add the transport helper(s) in `transport/` taking standard nats.go handles
   (`*nats.Conn`, `jetstream.JetStream`/`KeyValue`) — never open connections
   here; compose with the caller's.
3. Add an embedded-NATS round-trip test proving it end-to-end.
4. Note the consumer in README's "Next wires" — every wire names who calls it.

## Relationship to the other repos

- `~/repo/v4` (platform) — will import `contract` + `transport` to watch
  announced solutions and route tool calls. The inversion lands here first (v4
  depends on the SDK for new wire code), before any shared-infra extraction.
- `~/repo/solid-kit` (the fork) — will import the same to announce + serve
  its solutions over the bus, replacing today's in-process registration.
