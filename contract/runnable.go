package contract

// The runnable wire — a solution publishes a long-running runnable the platform
// triggers from its one scheduler. It is the durable, scheduled-work analog of
// the tool wire (envelope.go): a tool is a sub-second interactive call routed
// over request-reply; a runnable is a long / unattended / must-survive-a-
// disconnect job triggered over a JetStream work-queue, with progress streamed
// back on a per-run core-NATS subject and a terminal result delivered durably.
//
// Declarative half: RunnableDescriptor is the manifest leaf (peer of
// ToolDescriptor) — the platform reads it and registers a remote-proxy
// jobs.Runnable per announced runnable, so a job step can't tell local from
// remote (mirror of how an announced tool becomes a normal callable tool).
//
// Trigger / progress / result halves: RunnableRunRequest is the trigger,
// RunnableProgress is the streamed telemetry, RunnableResult is the terminal
// outcome — see transport.RunRunnable / transport.ServeRunnables.

// RunnableDescriptor is the leaf payload for an ArtifactRunnable — the
// declarative announcement of a long-running runnable the solution serves over
// the JetStream work-queue. It is the durable-work peer of ToolDescriptor: it
// carries the identity (Type) the platform triggers, plus human-facing metadata
// and the config options a job step picks from (mirror of jobs.Runnable's
// DisplayName + ListConfigs).
type RunnableDescriptor struct {
	// Type is the runnable's stable identifier — the trailing subject token the
	// platform triggers on (solid.runnable.run.<solution>.<type>). It mirrors
	// the in-tree jobs.Runnable.Type().
	Type string `json:"type"`

	// DisplayName is the operator-facing label (jobs.Runnable.DisplayName()).
	DisplayName string `json:"display_name"`

	// Description is a one-line summary the platform renders in the job-step
	// runnable picker.
	Description string `json:"description"`

	// ConfigOptions is the set of named configs this runnable can be triggered
	// with — the wire form of jobs.Runnable.ListConfigs(). The job step's config
	// picker is populated from these; the chosen one's ID rides in
	// RunnableRunRequest.ConfigID.
	ConfigOptions []ConfigOption `json:"config_options,omitempty"`
}

// ConfigOption is one selectable configuration for a runnable — the (id, name,
// description) triple a config picker renders. ID is what the trigger carries.
type ConfigOption struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

// RunnableRunRequest is the trigger the platform publishes to the JetStream
// work-queue (solid.runnable.run.<solution>.<type>) to fire one run. It is
// durable + at-least-once: it waits in the queue if the solution is momentarily
// down and survives a platform restart.
//
// Identity is the agent-as-lens / run-as-a-human envelope (ScopedIdentity,
// envelope.go): every scheduled run is attributable to a named human, scoped to
// a workspace — never an ambient full-access run. RunID correlates the trigger
// with its progress subject and terminal result.
type RunnableRunRequest struct {
	// Solution is the announcing solution's name — the work-queue subject's
	// first variable token.
	Solution string `json:"solution"`

	// Type is the runnable's Type (RunnableDescriptor.Type) — the work-queue
	// subject's trailing token, so a partner account permission scoped to
	// solid.runnable.run.<solution>.> covers all of its runnables.
	Type string `json:"type"`

	// ConfigID names which of the runnable's ConfigOptions to run with (chosen
	// in the job step's config picker). Empty for a runnable with no configs.
	ConfigID string `json:"config_id,omitempty"`

	// Identity is the run-as-a-human scope (run-as user). Never empty for a
	// scheduled run — the run is attributable.
	Identity ScopedIdentity `json:"identity"`

	// RunID correlates this trigger with its progress subject
	// (solid.runnable.progress.<runid>) and its terminal result. The platform
	// (caller) mints it; the solution echoes it on every progress + the result.
	RunID string `json:"run_id"`
}

// Run status values — the terminal outcome of a runnable run, mirroring the
// in-tree jobs.Status string consts (lowercase, stored verbatim). Only the
// terminal set is on the wire: a run ends completed, failed, or skipped.
// "running" is implied by progress events, not a terminal RunnableResult.Status.
const (
	// RunStatusCompleted — the runnable finished its work successfully.
	RunStatusCompleted = "completed"
	// RunStatusFailed — the runnable errored; RunnableResult.Error carries why.
	RunStatusFailed = "failed"
	// RunStatusSkipped — the runnable decided there was nothing to do (a no-op
	// run, e.g. no new data to load). A gate, not a failure.
	RunStatusSkipped = "skipped"
)

// RunnableProgress is one streamed telemetry event for a run, published on the
// core-NATS per-run subject (solid.runnable.progress.<runid>). It is EPHEMERAL
// "watch it run" telemetry — a dropped line is cosmetic — the mirror of
// jobs.Progress. The terminal outcome is RunnableResult, delivered durably, not
// the last progress event.
type RunnableProgress struct {
	// RunID echoes RunnableRunRequest.RunID (also encoded in the subject; carried
	// in the body so a consumer needn't parse the subject).
	RunID string `json:"run_id"`

	// Status is a free-form progress phase (e.g. "running", "copying", a step
	// label) — NOT one of the terminal RunStatus* consts. Optional.
	Status string `json:"status,omitempty"`

	// Message is the human-readable progress line the platform relays into the
	// local job's progress channel.
	Message string `json:"message"`
}

// RunnableResult is the terminal outcome of a run, delivered DURABLY (a
// JetStream result stream keyed by RunID — see transport.ServeRunnables /
// RunRunnable) so a platform restart can still read it. It mirrors the terminal
// half of jobs.Progress.
//
// Status is one of the RunStatus* consts. Error carries a handler-level failure
// message as DATA (the runnable ran on the solution but failed) — a transport
// failure surfaces as a Go error from RunRunnable instead, never here.
type RunnableResult struct {
	// RunID echoes RunnableRunRequest.RunID.
	RunID string `json:"run_id"`

	// Status is the terminal outcome: RunStatusCompleted / RunStatusFailed /
	// RunStatusSkipped.
	Status string `json:"status"`

	// Message is the human-readable terminal summary.
	Message string `json:"message,omitempty"`

	// Error is a non-empty handler-level error message when Status is
	// RunStatusFailed. Empty otherwise.
	Error string `json:"error,omitempty"`
}
