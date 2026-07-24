# Solid SDK — build solutions on Solid

> Bring your data, teach an AI analyst your domain, ship it — with Claude as your
> co-builder. It then works your data every day, shows its reasoning, and your
> people stay in control.

The Solid SDK and its companion starter, **solid-kit**, are how your team builds
**solutions** — the vertical capabilities your analysts and engineers run on top
of the Solid platform. This is the builder's guide. (Platform internals and the
wire protocol live in [`ARCHITECTURE.md`](./ARCHITECTURE.md).)

**Where to read:**

| You are | Start with |
|---|---|
| A solution builder | this README, then [the contracts](#the-contracts) in [`docs/`](./docs/) |
| A coding agent (Claude) | [`CLAUDE.md`](./CLAUDE.md) — the invariants and working guide |
| On the platform / wire level | [`ARCHITECTURE.md`](./ARCHITECTURE.md) — packages, wires, quack |

## A day building a solution

1. **Install the Solid skill.** In Claude Desktop, install **Solid** from the
   plugin marketplace. It carries the `solidsdk` toolkit and the build-a-solution
   know-how — nothing else to set up.
2. **Ask Claude to scaffold.** *"Start a new solution called churn-watch."* You get
   a complete, runnable solution repo (a solid-kit project), open in code mode,
   with your co-builder already fluent in its conventions.
3. **Give it example data, and talk.** Drop in a sample export — call records,
   billing rows, tickets — and describe what you want to watch. Claude writes the
   **data loaders**, the **data model**, and the **skills** (the analyst's
   methodology) with you, against your real shape.
4. **Run it.** One command connects your solution to Solid. It's **live** — your
   data lands, the dashboards fill, the agent can work it.
5. **Test and iterate.** Watch the agent reason over your data in *glass*, correct
   it, sharpen the skill, re-run — until it does the job your best analyst would.

No screen-by-screen app build, no integration project. A person who knows the
domain, plus Claude, plus a sample of the data — that's the team.

## What you're building

A **solution** is a domain capability. For a telecom operator:

- **Sales Integrity** — catch mis-sold or mis-billed contracts across telephony, CRM, billing
- **Service Desk** — triage tickets, investigate, draft resolutions
- **Churn Watch** — surface at-risk accounts from usage and care signals
- **Network Quality · Revenue Assurance · Fraud** — turn raw signals into findings someone can act on

## What's inside a solution

| Part | What it is | Who writes it |
|---|---|---|
| **Skills** | the methodology — how an analyst reasons, in plain markdown | your domain expert, with Claude |
| **Data model** | the shape of your data and what each column means | inferred from your sample, with Claude |
| **Loaders** | how raw data is read, decoded, and kept fresh | Claude, against your source |
| **Dashboards** | what to watch — charts over your data, in YAML | declared |
| **Workflows** | when to act — a scheduled check that runs a skill | declared |
| **Tools / actions** | optional — let the agent *do* things, not just read | optional |

Underneath, the platform does the heavy lifting — **connect → discover → keep.**
Point it at a source (a blob store, a database, a file drop); it samples and learns
the shape; it keeps the data current incrementally as new data arrives. Your
solution supplies the *decoder* — what the events mean — never a data engine.

## The mental model: you teach, you don't wire

Two load-bearing ideas:

- **The skill is the product.** A skill is a markdown document — your expert's
  playbook: what to look at, how to judge it, what counts as a finding, how to
  write it up. You're not programming a workflow engine; you're onboarding an
  analyst who is tireless, consistent, and fully auditable.
- **You bring your data; Solid keeps it.** You decode your domain once; the
  platform handles landing it, keeping it fresh, and scoping it. The agent only
  ever sees what the asking user is allowed to see.

That's why a *business* team — not only a dev team — can build solutions: the hard,
valuable part is the methodology and the data meaning, written in language your
best people already speak.

## What you get for free

Building on Solid means the hard platform concerns are already handled — your team
spends its time on the domain, not the plumbing:

- **EU AI Act compliant by design.** Solid logs, traces, and records every decision
  the agent makes at the level the Act expects. It will even **draft a full
  compliance report for your solution** and guide you in keeping it conformant as
  it evolves — so "is our AI auditable?" is answered before the auditor asks.
- **Credentials, monitoring, cost — built in.** Credential management, performance
  monitoring, and cost control with per-workspace / per-solution attribution come
  with the platform. Nothing extra to stand up.
- **A full admin console.** Workspaces, users and access, approvals, and your
  solution's whole lifecycle are managed in Solid's admin interface out of the box.
- **Your solution is native code — not a DAG diagram.** No boxes-and-arrows
  low-code canvas you outgrow in month three. It's real, version-controlled code
  your team owns, builds with Claude, and tests like software — while every
  platform service above comes for free underneath it.

This is not theoretical. An internal team at a global healthcare company had built
an incident-annotation tool — matching new incidents to past resolutions — in Python
and Streamlit. It worked, but their security review blocked it: gigabytes of
dependencies, no audit trail, weak credential handling. We helped them keep the part
that mattered — the business logic — and shed the rest onto the platform. The port
itself was almost mundane: drop the existing app into a subfolder, ask Claude *"how
would we re-engineer this to run smoothly and compliantly on Solid?"*, and iterate
for about two hours. It came back **~10× smaller**, in Go, with no UI to maintain
and no Python dependency sprawl — and audit logging, credential management, and
scoped access came for free. Leaner, faster, compliant, and running happily in
production.

## No ceiling: the Go escape

Declarative artifacts — skills, dashboards, data models — carry most of a solution.
When you need more, there is no wall, because a solution is a **real Go program**,
not configuration fed to an interpreter. Drop to code and you have the whole
language and its entire ecosystem: custom integrations, your own algorithms, a
bespoke connector, even **a standalone public web app with its own interface and
its own AI** that feeds your solution.

One of our service-desk solutions does exactly that: a **public self-service
portal** a customer chats with — its own web front end, its own model — that hands
the conversation to the solution over the bus; the solution then correlates the
report against history and raises a finding for an agent to work. A boxes-and-arrows tool can't reach outside its own canvas. A Solid solution
can do anything Go can — and *still* gets every platform service above
(compliance, data, admin, access, cost) for free underneath it.

**And Claude writes the part that's yours — with you.** Need data out of SAP,
Oracle Apps, or any system without an off-the-shelf connector? Claude writes the
**integration bit** with you — the piece specific to your system — leveraging Go's
vast ecosystem, and a high-performance loader takes shape around it. Solid supplies
everything else a production loader needs: scheduling, incremental keep (new data
only, deduped), logging, and load management. You bring the slice that's unique to
you; the platform is the rest.

For a state institution in Portugal, that's how Claude understood an entire SAP
PowerDesigner data model — tables, columns, datatypes, foreign keys — and built a
full incremental loader for it, straight out of its raw repository database (over
**60 million rows** in the relationship graph alone), in pure Go, with no
PowerDesigner install and no export files. **About four hours, end to end.** The
long tail of *"but our data lives in X"* stops being a blocker.

Most days you'll never need the escape. It's the reason you'll never hit a ceiling.

## solid-sdk + solid-kit

- **solid-kit** — the starter. `solidsdk new solution` scaffolds it into a
  complete, runnable solution you fill in.
- **solid-sdk** — the toolkit that scaffolds, validates, and upgrades it, and
  ships its own conventions so Claude already knows how to extend a solution. The
  rule is *ask the tool, don't guess* — the version of the SDK your repo depends on
  is the version of the guidance you get, so it never goes stale.
- **The Solid skill** — the toolkit packaged as a Claude Desktop skill, installed
  from the plugin marketplace. It carries the CLI and the conventions, so a builder
  goes from "install" to "scaffolding a solution" without setting anything up.

```bash
solidsdk new solution churn-watch   # scaffold a solid-kit solution repo
solidsdk validate                   # lint the whole solution against the platform contract
solidsdk migrate                    # upgrade to a new Solid version — Claude applies the changes
```

## Solutions compose

What your team deploys on Solid isn't a solution in isolation — it's a
**workspace**, and a workspace can draw on **many solutions at once**. Because
every solution runs on the same governed fabric and answers to the same agent —
one that only ever sees what the asking user is allowed to see — they combine
instead of colliding.

So Churn Watch, Sales Integrity, and the Service Desk aren't three installs a
department juggles; they're capabilities one analyst can reason across in a
single investigation. The value isn't additive, it's **combinatorial**: each
solution you add is worth more because of the ones already there. Apps from
different vendors stay silos forever — solutions on Solid get more useful
together. That's the platform answer to *"why build all of these in one place"*:
you're not buying N tools, you're growing a system whose parts multiply each
other, in your environment.

## How a solution ships and runs

A finished solution **announces itself** to the platform: it appears in the admin
console, an operator reviews and approves it, and it goes live. Nothing a solution
ships reaches a user until an operator says yes.

A solution runs **only on a licensed Solid platform.** Build it for your own teams
(internal use) or, as a partner, package it for your customers — the runtime is
Solid either way, and the access model is the same: the agent is a lens on the
user, never above them.

## Your builds shape the roadmap

This is how we build the platform's roadmap: from what builders actually had to
build, and the feedback you send back. When a solution needs something the platform
doesn't have yet, that need is what prioritizes the next capability — and when it
lands, a skill migrates your solution onto it, so what you built bespoke becomes
something you get for free. It's how the data catalog gained its *business-name*
field, and how incremental data loading is becoming a platform feature — both driven
by real engagements.

## The contracts

The declarative surfaces a solution writes against are specified in
[`docs/`](./docs/) — versioned, status-tagged, and the same documents the
toolkit validates against:

| Contract | Governs | Status |
|---|---|---|
| [`solution-stores.md`](./docs/solution-stores.md) | how your solution's data is declared, stored, served, and governed | DRAFT v0.2 |
| [`store-backed-catalogs.md`](./docs/store-backed-catalogs.md) | declared-only exposure — the platform serves exactly the surface you declare | SHIPPED 1.0.0 |
| [`workflow-defs.md`](./docs/workflow-defs.md) | the workflow YAML your solution ships (when to act) | SHIPPED 1.0.1 |
| [`dashboard-dsl.md`](./docs/dashboard-dsl.md) | dashboard queries + widgets, in YAML (what to watch) | DRAFT 0.14.1 |
| [`incremental-load-pipeline.md`](./docs/incremental-load-pipeline.md) | fetch → decode → keep for your source data | DRAFT 0.2.0 |
| [`tenant-artifact.md`](./docs/tenant-artifact.md) | declaring a lake tenant (streams, projections, views, ingest, retention) from a solution | DRAFT 0.1.0 |

## Versions & support

- The SDK is versioned; once you've built against it, upgrades are **additive**,
  and `solidsdk migrate` carries your solution forward when the platform moves.
- The contracts your solution declares against (data model, dashboards, workflows)
  ship as both human docs and machine-readable schemas the toolkit validates.

---

*Building the platform itself, or integrating at the wire level? See
[`ARCHITECTURE.md`](./ARCHITECTURE.md) for the announce protocol, the contract
types, and the transport layer.*
