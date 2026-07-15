<!--
  ┌──────────────────────────────────────────────────────────────────────┐
  │  Workflow Definitions — Solution YAML Contract                         │
  ├──────────────────────────────────────────────────────────────────────┤
  │  Contract version : 1.0.1                                              │
  │  Status           : SHIPPED — implemented and enforced at boot         │
  │  Stability         : stable; the schema is the enforced subset (§5)    │
  │  Surface           : external — authored by solution authors (humans   │
  │                      and the LLM); third-party solutions write against │
  │                      this.                                             │
  │  Last updated      : 2026-06-28                                        │
  │  Owner ticket      : S-1349 (implementation) / S-1350 (this doc)       │
  │  Implements        : domains/workflow/yaml.go                          │
  │                      (RegisterSolutionWorkflowsFromYAML)               │
  │  Supersedes        : nothing — code registration (workflow.            │
  │                      RegisterDefault) remains valid as rung 1          │
  └──────────────────────────────────────────────────────────────────────┘
-->

# Workflow Definitions — Solution YAML Contract

**Contract version 1.0.1 · Shipped**

A **workflow definition** is the named, presentable unit a workspace `Schedule`
points at: a triggered, anchored conversation that runs ONE named skill over a
resolved time period (`domains/workflow`, `docs/design/pure-skill-workflows.md`).
This contract specifies how a **solution** publishes its definitions as embedded
YAML files — the authoring ladder's **rung 2**, following the
dashboards-from-YAML precedent (`docs/ideas/adaptive-workflows/primitives.md`
§The authoring ladder).

Unlike the dashboard DSL (still pre-1.0), this contract starts at 1.0.0 because
it is deliberately tiny: the schema is **exactly the subset the engine enforces
today** (§5), and growth happens by MINOR bumps as interpreters for new blocks
actually ship.

> **Delivery (announce wire).** This document specifies the def YAML itself. As of
> `solid-sdk` v0.3.0 the same YAML also travels as the `Body` of a
> `WorkflowArtifact` leaf when a solution **announces** over NATS KV — the
> announced rung beyond rung 2, consumed by `app/solutionbus` and parsed by the
> error-returning `workflow.ParseWorkflowYAML` (the announce sibling of the
> panicking in-tree loader; see §3). The announce / manifest contract itself is
> canonical in `solid-sdk/contract`; its author-facing home is this `docs/` folder
> (moved from the platform repo, S-1743). In-tree `RegisterDefault` (rung 1)
> and `RegisterSolutionWorkflowsFromYAML` (rung 2) stay live paths; the schema
> here is identical across all of them.

---

## 1. What a definition is — and is not

A definition **declares**; it never executes:

- It names a **skill** — the skill carries all the judgment and methodology.
  The platform's `Fire` primitive resolves id → skill at trigger time.
- Its `activation` block declares the cadence the publishing solution
  *intends* — **it does not trigger anything**. The workspace `Schedule` is
  the only trigger. Activation is opt-in by construction: a solution update
  must never silently start a cron agent. A workspace realizes the declared
  cadence by authoring a Schedule that references the def.
- Its `goal` block is **opaque to the platform**: carried verbatim on the
  registered definition, interpreted only by the owning solution's own code
  (*the vertical is the interpreter* — see §6).

There are no `stages:`, `gates:`, or `on:` event blocks. Those belong to the
adaptive-workflows grammar (`docs/ideas/adaptive-workflows/primitives.md`) and
will enter this contract only when their interpreters exist (§5).

---

## 2. File location, embedding, registration

Definitions live in the solution's repo directory and ship inside the binary:

```
solutions/<name>/
├── register.go          # init() registers the solution + its defs
└── workflows/
    ├── weekly-check.yaml
    ├── investigation.yaml
    └── pursuit.yaml
```

```go
// solutions/<name>/register.go
//go:embed workflows/*.yaml
var workflowFS embed.FS

func init() {
    workflow.RegisterSolutionWorkflowsFromYAML(workflowFS, "<name>", "workflows/*.yaml")
    // …
}
```

Each loaded def is registered into the default workflow registry with
`Source: "solution:<name>"`. `fs.Glob` returns sorted paths, so registration
order is deterministic.

**One mechanism per solution.** A solution registers its defs either from YAML
(this contract) or in Go via `workflow.RegisterDefault` (rung 1) — never both.

### 2.1 Boot validation — panic semantics

A solution-published definition **is code**, and code errors fail loud at boot:
`RegisterSolutionWorkflowsFromYAML` **panics** on any invalid input. The full
refusal list, as implemented:

| Condition | Failure |
|---|---|
| empty solution name / bad glob / glob matches nothing | panic |
| file read error / YAML parse error | panic |
| **unknown field** (strict decode — see §5) | panic |
| missing `id`, `skill`, or `display_name` | panic |
| `activation` present but missing `schedule` or `window` | panic |
| `activation.schedule` not a parseable cron expression | panic |
| `activation.window` not a known period preset | panic |
| duplicate id (across **all** registered defs, any source) | panic |

There is no partial load: one bad def stops the binary from starting.

---

## 3. Field reference

The schema, exactly as `domains/workflow/yaml.go` implements it:

| Field | Required | Type | Meaning |
|---|---|---|---|
| `id` | **yes** | string | Registry slug, globally unique. The handle a workspace `Schedule` references and `Fire` resolves. Convention: `<solution>-<verb>` (e.g. `salesintegrity-pursuit`). |
| `display_name` | **yes** | string | Human name shown by pickers. |
| `description` | no | string | One-to-two sentences for pickers/cards. |
| `icon` | no | string | riclib/icon name. |
| `skill` | **yes** | string | The skill id the workflow runs — the single behavior pointer. |
| `activation` | no | block | Declared cadence (§3.1). **Declares, does not trigger** — the workspace `Schedule` is the trigger. |
| `goal` | no | map | **Opaque** goal block (§6). Carried verbatim; the platform never reads inside it. |

`source` is **not** an authored field — the loader stamps
`Source: "solution:<name>"` on every def it registers.

> **Registered shape vs authored schema (S-1457).** The seven fields above are the
> full *authored* surface. The registered `WorkflowDefinition`
> (`domains/workflow/types.go`) additionally carries a stamped `Provenance`
> (alongside `Source`) and is a `gitstore.Entity`, because announced defs persist
> and reconcile like skills/prompts. Neither is author-authored — they are
> stamped by the materialise path, not the YAML. The strict-decode honesty rule
> (§5) still holds for the authored subset: a YAML carrying any field outside this
> table refuses to load.

### 3.1 The `activation` block

```yaml
activation: { schedule: "0 9 * * MON", window: last_week }
```

| Field | Type | Validation |
|---|---|---|
| `schedule` | cron string | robfig/cron: optional seconds field, standard 5 fields, `@descriptors` (mirrors `app/workflow`'s publisher parser). Evaluated in the workspace timezone by whatever Schedule realizes it. |
| `window` | preset key | Closed set — the period presets `app/workflow.ResolvePeriod` can produce: `yesterday`, `last_week`, `last_month`, `last_24h`, `last_7d`, `last_30d`. |

If the block is present, **both** fields are required. If a def has no
schedule-shaped trigger (e.g. it is fired per finding by another run), omit the
block entirely.

---

## 4. Versioning & stability

`MAJOR.MINOR.PATCH`, same policy as the dashboard DSL contract:

| Bump | Meaning | Examples |
|---|---|---|
| **MAJOR** | Breaking — an existing def may refuse to load. | Remove/rename a field; tighten validation that previously passed. |
| **MINOR** | Additive — old defs keep loading. | A new optional field **whose interpreter ships in the same change** (§5); a new window preset. |
| **PATCH** | No schema change. | Doc clarifications. |

There is no `version:` field in the YAML — with a strict decoder, an old
runtime rejects a newer def loudly at boot (unknown field), which is the
failure mode we want; a version header would add ceremony without adding
safety. If the schema ever forks incompatibly, MAJOR introduces one.

### 4.1 Changelog

| Version | Date | Change |
|---|---|---|
| 1.0.0 | 2026-06-10 | Initial contract (S-1349): `id` / `display_name` / `description` / `icon` / `skill` / `activation{schedule, window}` / opaque `goal`. Strict decode. |
| 1.0.1 | 2026-06-28 | PATCH (doc-only, no schema change): correctness pass (S-1524). §§1–5 verified accurate. §6 corrected — the goal seam is `pursuit.Service.SetStandingGoalSeed` + `EnsureStandingGoal` via `RegisterDeps.PursuitSvc` (the prior `mustStandingGoalFromDef` was fictional). §7 examples flagged illustrative/out-of-tree (no in-tree solution uses the rung-2 loader yet). Added the S-1457 registered-shape note (§3) and the announce-wire pointer. |

---

## 5. The honesty rule

**Config carries nothing the engine doesn't enforce.** The decoder is strict
(`yaml.Decoder.KnownFields(true)`), so a def carrying any field outside §3 —
including the grammar blocks `stages:`, `gates:`, `on:` — **refuses to load,
by design**, instead of being silently ignored.

This is mechanical honesty: a YAML file that loads is a YAML file whose every
line does something. The alternative — accepting aspirational blocks and
ignoring them — produces configs that lie about what the system does, which is
worse than configs that are small. Grammar blocks enter this contract one
MINOR bump at a time, each in the same change as its interpreter.

(Known consequence, accepted: event chaining — `on: run.completed` — is the
named activation-sequencing gap. Until it exists, sequenced workflows use
staggered cron times; the stagger is the honest mechanism.)

---

## 6. The opaque `goal` block

```yaml
goal:
  standing: "Elektrum sales are clean and durable"
  metric: sales_integrity_index
  target: 98
```

`goal` is decoded as `map[string]any` and carried **verbatim** on the
registered `WorkflowDefinition`. The platform loader never validates,
interprets, or even enumerates its keys — **the owning solution is the
interpreter**. A goal-using vertical casts the opaque map into its own typed
shape and seeds it through the pursuit service: it stashes the typed seed via
`solutions.RegisterDeps.PursuitSvc.SetStandingGoalSeed(pursuit.Goal{…})`
(`domains/pursuit/service.go`), and `EnsureStandingGoal(ctx, workspace)` then
**idempotently** seeds the standing root goal (G0) into the workspace's pursuit
ledger on first write. salesintegrity was the reference vertical for this; it now
lives out-of-tree, but the seam — opaque map → typed `pursuit.Goal` → seed via
`PursuitSvc` — is unchanged.

The keys above are therefore *salesintegrity's* vocabulary, not this
contract's. Another vertical may carry a completely different goal shape. This
is the skill-is-the-interpreter rule extended to config: the shared "goal
interpreter" the extraction discipline defers never exists as platform code
(`docs/design/pursuit-v1.md` §The placement rule).

---

## 7. Worked examples

The three defs below are the salesintegrity reference solution's. **They are
illustrative of the schema, not in-tree:** salesintegrity has moved out-of-tree,
and as of this writing **no in-tree solution uses the rung-2 YAML loader** — the
in-tree solutions (aiact, dq) register in Go via `workflow.RegisterDefault`
(rung 1). The shapes are still valid `RegisterSolutionWorkflowsFromYAML` input;
treat the paths as historical.

### 7.1 Scheduled reporter — `weekly-check.yaml`

The minimal shape: id + presentation + skill. It declares no `activation`;
its cadence is left entirely to the workspace Schedule that references it.

```yaml
id: salesintegrity-weekly-check
display_name: Weekly Sales Integrity Check
description: Reconcile the week's telephony, CRM and billing signals, publish material findings, and write a weekly digest.
icon: clipboard-list
skill: salesintegrity-weekly-check
```

### 7.2 Per-finding investigator — `investigation.yaml`

No `activation` at all: this def is fired per finding by the weekly check's
`publish_finding` flow, not by a cron schedule.

```yaml
id: salesintegrity-investigation
display_name: Investigate Sales Integrity Finding
description: "Investigate one finding: confirm it across systems, quote the contradicting records, and write a resolution."
icon: clipboard-list
skill: salesintegrity-investigation
```

### 7.3 Pursuit — `pursuit.yaml`

The full surface: declared activation (staggered after the weekly check — the
honest sequencing mechanism, §5) plus an opaque goal block the solution's own
seeding code interprets (§6).

```yaml
id: salesintegrity-pursuit
display_name: Sales Integrity Pursuit
description: Move the standing goal — record weekly progress, open and close sub-pursuits as findings cluster and targets hold, and propose mitigations for director ratification.
icon: gauge
skill: salesintegrity-pursuit
activation: { schedule: "0 9 * * MON", window: last_week }
goal: # OPAQUE to the platform loader — carried, not interpreted
  standing: "Elektrum sales are clean and durable"
  metric: sales_integrity_index
  target: 98
  target_unit: "index ≥"
  horizon_days: 90
  owner: "Customer Service Director"
```

---

## 8. Related documents

- `domains/workflow/CLAUDE.md` — the registry, who registers what, gotchas.
- `docs/design/pursuit-v1.md` — the decision record that introduced rung 2
  (§salesintegrity wiring) and the honesty rule.
- `docs/ideas/adaptive-workflows/primitives.md` — the five-primitive grammar
  and the full authoring ladder this contract is rung 2 of.
- `./dashboard-dsl.md` — the sibling contract whose YAML-from-solution
  + boot-validation precedent this follows.
