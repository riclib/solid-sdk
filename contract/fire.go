package contract

// The fire wire — the INVERSE of the runnable wire. The runnable wire is
// platform→solution (the platform triggers a long-running job the solution
// serves); the fire wire is solution→platform (a solution asks the platform to
// run a workflow/skill in a workspace, seeded with a parameter). It is the first
// solution→platform REQUEST primitive: an announced solution that consumes an
// external event (a portal intake message, a webhook, an inbound queue) and needs
// the platform to RUN something with its agent runtime + workspace data + audit —
// work the solution itself cannot do (it holds no LLM, no workspace access, no
// audit trail).
//
// Transport posture mirrors the runnable trigger: DURABLE + at-least-once over a
// JetStream work-queue (solid.fire.run.>), so a fire published while the platform
// is momentarily down waits in the queue and survives a restart. The terminal
// FireResult rides a durable result stream keyed by RunKey (solid.fire.result.>)
// so the disposition (accepted / rejected, plus the created conversation id) can
// be read after the fact — there is no live reply subject across the durable
// queue.
//
// Identity is NOT on the wire. A fire is "the workspace intaking a signal to
// process" — the same act as a scheduled run — so the platform assigns the
// run-as principal (its existing scheduled-run identity) after validating
// Workspace against the calling partner account's installed set. The anonymous
// origin contributes DATA (Question), never AUTHORITY. See
// docs/platform-services-design.md in the lvrtc fork for the full design.

// FireRequest is the trigger a solution publishes to ask the platform to run a
// workflow in a workspace, seeded with a free-text turn. Published durably to
// solid.fire.run.<solution>.<workflow>; a partner account is granted publish on
// solid.fire.run.<solution>.> (the subject shape IS the authz boundary, like the
// tool + runnable wires).
type FireRequest struct {
	// Solution is the announcing solution's name — the work-queue subject's first
	// variable token (the authz scope a partner account is granted publish on).
	Solution string `json:"solution"`

	// Workspace is the target workspace slug. The platform VALIDATES it against
	// the calling account's installed+approved set — a solution cannot fire into a
	// workspace it isn't installed in. It is never the run-as identity; the
	// platform assigns that.
	Workspace string `json:"workspace"`

	// Workflow is the announced workflow slug the platform resolves to a skill
	// (through the workspace's bound-solution overlay) and fires. It is the
	// work-queue subject's trailing token.
	Workflow string `json:"workflow"`

	// Question is the synthetic user turn that seeds the agent loop — the raised
	// payload (e.g. the portal-reported company + issue) so the run reacts to THIS
	// event, not a generic window sweep. Empty falls back to the platform's
	// default seed.
	Question string `json:"question,omitempty"`

	// RunKey is the deterministic idempotency key (e.g. the portal-minted ref). It
	// rides as the JetStream Nats-Msg-Id (so a double-publish collapses inside the
	// stream's dedup window) AND becomes the run's source_run_id (so a redelivered
	// fire resolves to the same run and the platform handler skips it). It also
	// keys the FireResult subject. Because it is a subject token, keep it free of
	// '.', ' ', '*', '>' (a minted ref like REF-260618-071530-02 is safe).
	RunKey string `json:"run_key"`
}

// Fire status values — the terminal disposition of a fire request, published on
// the durable result stream. Both are DATA outcomes the platform acks; a
// transient transport/handler failure is NOT a status — it leaves the trigger
// unacked for redelivery (the platform handler returns a Go error instead).
const (
	// FireStatusAccepted — the workflow run was created/launched (the durable
	// conversation exists; ConvID is set).
	FireStatusAccepted = "accepted"

	// FireStatusRejected — the fire was permanently declined as DATA: an unknown
	// workspace/workflow, a workspace the solution isn't installed in, or an
	// idempotent already-exists skip. Acked, never redelivered.
	FireStatusRejected = "rejected"
)

// FireDescriptor is a CAPABILITY declaration carried in the manifest header
// (SolutionManifest.Fires) — NOT an artifact body. It announces that this
// solution fires a given workflow over the fire wire (transport.PublishFire),
// serving two purposes at approve-before-live time:
//
//   - AUTHZ: when a solution declares any Fire, the platform grants the partner
//     account publish on solid.fire.run.<solution>.> — the fire grant becomes an
//     operator-approved capability, not out-of-band NATS config.
//   - VISIBILITY: the operator sees what inbound events the solution reacts to and
//     which workflow each fires, as part of the approval decision.
//
// It is the IMPERATIVE leg's declaration: the solution's own process owns the
// consumer and decides — in Go — when to fire. The DECLARATIVE leg (the platform
// owning the consumer via a seed-mapped EventTrigger that needs no solution
// process) is the 0.3.0 direction this v0 flag precedes. See
// docs/platform-services-design.md in the lvrtc fork.
type FireDescriptor struct {
	// Workflow is the announced workflow slug this solution fires (matches a
	// WorkflowArtifact.ID the solution also ships). Resolved platform-side to a
	// skill at fire time. It is the trailing token of the granted publish subject
	// (solid.fire.run.<solution>.<workflow>).
	Workflow string `json:"workflow"`

	// DisplayName is the operator-facing label.
	DisplayName string `json:"display_name,omitempty"`

	// Description is the approval context: what inbound event triggers this fire
	// and why (e.g. "a public-portal intake report → correlate against ongoing
	// situations").
	Description string `json:"description,omitempty"`

	// Source is an optional, informational tag for the event origin the solution
	// consumes (e.g. "portal.intake") — operator context, not an authz or routing
	// field in v0 (the solution owns its own consumer).
	Source string `json:"source,omitempty"`
}

// FireResult is the terminal disposition of a fire, delivered DURABLY (a
// JetStream result stream keyed by RunKey) so a caller can confirm acceptance and
// link the created run after the fact. v0 is mostly fire-and-forget — the fired
// skill publishes its own finding — so this is observability + a seam for a
// future synchronous caller.
type FireResult struct {
	// RunKey echoes FireRequest.RunKey (also encoded in the subject).
	RunKey string `json:"run_key"`

	// Status is FireStatusAccepted or FireStatusRejected.
	Status string `json:"status"`

	// ConvID is the created conversation id (a run-viewer link) when accepted.
	ConvID string `json:"conv_id,omitempty"`

	// Message is a human-readable disposition summary.
	Message string `json:"message,omitempty"`

	// Error is the reason when rejected. Empty when accepted.
	Error string `json:"error,omitempty"`
}
