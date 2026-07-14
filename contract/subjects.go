package contract

import "fmt"

// SolutionsBucket is the JetStream KV bucket partner solutions announce into.
// Keys form a per-solution tree (see ManifestKey / ArtifactKey).
const SolutionsBucket = "solutions"

// ManifestKey is the KV key a solution's manifest (the index + core metadata)
// lands at: `<name>.manifest`.
func ManifestKey(solution string) string {
	return fmt.Sprintf("%s.manifest", solution)
}

// ArtifactKey is the KV key one artifact leaf lands at: `<name>.<kind>.<id>`.
func ArtifactKey(solution string, kind ArtifactKind, id string) string {
	return fmt.Sprintf("%s.%s.%s", solution, kind, id)
}

// ManifestWatchFilter is the KV key filter the platform watches to observe
// every solution's manifest (and only manifests, not leaf churn): `*.manifest`.
func ManifestWatchFilter() string { return "*.manifest" }

// SolutionSubtree is the KV key filter covering one solution's whole tree
// (manifest + all leaves): `<name>.>`. Used for purge/inspection, not the
// assembly watch.
func SolutionSubtree(solution string) string {
	return fmt.Sprintf("%s.>", solution)
}

// ToolSubject is the NATS request-reply subject a solution serves a tool on.
//
// Subject design IS the authz boundary: a partner's NATS account is granted
// per-subject permissions scoped to `solid.tool.<solution>.>`, so the subject
// shape is the partner sandbox, not just addressing. Keep the segments
// hierarchical so a single permission can cover all of a solution's tools.
func ToolSubject(solution, tool string) string {
	return fmt.Sprintf("solid.tool.%s.%s", solution, tool)
}

// ToolSubjectPrefix is the wildcard a partner account is granted to serve all
// of a solution's tools (`solid.tool.<solution>.>`).
func ToolSubjectPrefix(solution string) string {
	return fmt.Sprintf("solid.tool.%s.>", solution)
}

// RunnableRunSubject is the JetStream work-queue subject the platform publishes
// a runnable trigger to: `solid.runnable.run.<solution>.<type>`. It is durable +
// at-least-once — a trigger waits in the queue if the solution is momentarily
// down and survives a platform restart (the opposite posture from the live
// ToolSubject request-reply). Like the tool subject, the shape IS the authz
// boundary: a partner account is granted RunnableRunSubjectPrefix.
func RunnableRunSubject(solution, runnableType string) string {
	return fmt.Sprintf("solid.runnable.run.%s.%s", solution, runnableType)
}

// RunnableRunSubjectPrefix is the wildcard covering all of a solution's runnable
// triggers (`solid.runnable.run.<solution>.>`). The solution's work-queue
// consumer binds this filter to receive every runnable's trigger; a partner
// account is granted this one permission to serve all its runnables.
func RunnableRunSubjectPrefix(solution string) string {
	return fmt.Sprintf("solid.runnable.run.%s.>", solution)
}

// RunnableRunSubjectAll is the work-queue stream's capture filter across every
// solution (`solid.runnable.run.>`) — the subject set the shared work-queue
// stream binds (see transport.EnsureRunnableStream).
func RunnableRunSubjectAll() string { return "solid.runnable.run.>" }

// RunnableProgressSubject is the core-NATS per-run subject a runnable streams
// its ephemeral progress on: `solid.runnable.progress.<runid>`. Core NATS (not
// JetStream) — a dropped progress line is cosmetic "watch it run" telemetry; the
// durable outcome rides RunnableResult on the result stream, not here.
func RunnableProgressSubject(runID string) string {
	return fmt.Sprintf("solid.runnable.progress.%s", runID)
}

// RunnableResultSubject is the JetStream per-run subject the runnable's terminal
// RunnableResult is published to: `solid.runnable.result.<runid>`. It is DURABLE
// (a JetStream stream keyed by RunID) so a platform restart can still read the
// outcome — the caller (RunRunnable) consumes exactly this run's result.
func RunnableResultSubject(runID string) string {
	return fmt.Sprintf("solid.runnable.result.%s", runID)
}

// RunnableResultSubjectAll is the result stream's capture filter across every
// run (`solid.runnable.result.>`) — the subject set the durable result stream
// binds (see transport.EnsureRunnableStream).
func RunnableResultSubjectAll() string { return "solid.runnable.result.>" }

// FireRunSubject is the JetStream work-queue subject a solution publishes a fire
// request to: `solid.fire.run.<solution>.<workflow>`. It is the INVERSE direction
// of RunnableRunSubject (solution→platform, not platform→solution): a solution
// asks the platform to run one of its workflows in a workspace. Durable +
// at-least-once — a fire waits in the queue if the platform is momentarily down
// and survives a restart. Like the tool + runnable subjects, the shape IS the
// authz boundary: a partner account is granted FireRunSubjectPrefix.
func FireRunSubject(solution, workflow string) string {
	return fmt.Sprintf("solid.fire.run.%s.%s", solution, workflow)
}

// FireRunSubjectPrefix is the wildcard covering all of a solution's fire requests
// (`solid.fire.run.<solution>.>`) — the publish permission a partner account is
// granted to fire any of its announced workflows.
func FireRunSubjectPrefix(solution string) string {
	return fmt.Sprintf("solid.fire.run.%s.>", solution)
}

// FireRunSubjectAll is the work-queue stream's capture filter across every
// solution (`solid.fire.run.>`) — the subject set the platform's SINGLE fire
// consumer binds (see transport.EnsureFireStreams / ServeFires). Unlike the
// runnable wire (one consumer per solution), the platform drains every solution's
// fires through one consumer.
func FireRunSubjectAll() string { return "solid.fire.run.>" }

// FireResultSubject is the JetStream per-fire subject the terminal FireResult is
// published to: `solid.fire.result.<runkey>`. Durable (keyed by RunKey) so the
// disposition can be read after the fact — there is no live reply subject across
// the durable queue.
func FireResultSubject(runKey string) string {
	return fmt.Sprintf("solid.fire.result.%s", runKey)
}

// FireResultSubjectAll is the result stream's capture filter across every fire
// (`solid.fire.result.>`) — the subject set the durable result stream binds.
func FireResultSubjectAll() string { return "solid.fire.result.>" }

// StoreCallSubject is the request-reply subject a solution calls the governed
// store proxy on: `solid.store.call.<solution>.<op>`, op ∈ exec | query |
// test_connection | connect. It is the INVERSE direction of ToolSubject (solution→platform,
// not platform→solution) — the first solution→platform request-reply service —
// and the PLATFORM is the responder (binds a queue group so instances share the
// load).
//
// Like the tool + runnable + fire subjects, the shape IS the authz boundary: the
// solution name lives in the subject, not a bare solid.store.<op>, so a partner
// account can be granted publish only on StoreCallSubjectPrefix and the identity
// claim becomes account-enforceable. Until per-account permissions are
// provisioned (dev/embedded NATS), the segment is advisory: the payload
// StoreCallRequest.Solution MUST match the subject's solution segment and the
// platform rejects a mismatch (StoreCodeNotGranted). S-1706 (namespace
// reservation) covers announced-name squatting.
func StoreCallSubject(solution, op string) string {
	return fmt.Sprintf("solid.store.call.%s.%s", solution, op)
}

// StoreCallSubjectPrefix is the wildcard a partner account is granted publish on
// to reach the store proxy for all of a solution's ops
// (`solid.store.call.<solution>.>`) — the single permission that makes the
// solution's identity claim in the subject account-enforceable.
func StoreCallSubjectPrefix(solution string) string {
	return fmt.Sprintf("solid.store.call.%s.>", solution)
}
