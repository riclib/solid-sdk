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

## Versions

`nats.go` and `nats-server/v2` are pinned to match v4's go.mod (`v1.48.0` /
`v2.12.2`). When v4 bumps them, bump here in lockstep — a skew breaks the
eventual shared build.

## Testing

```bash
go test ./...   # embedded JetStream NATS server, no external dependency
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
- `~/repo/solid-partner` (the fork) — will import the same to announce + serve
  its solutions over the bus, replacing today's in-process registration.
