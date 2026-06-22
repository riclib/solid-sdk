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
