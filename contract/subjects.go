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
