package contract

import "fmt"

// SolutionsBucket is the JetStream KV bucket partner solutions announce their
// manifest into. The platform watches it to wire solutions live.
const SolutionsBucket = "solutions"

// SolutionKey is the KV key a solution's manifest lands at — its bare name.
func SolutionKey(solution string) string { return solution }

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
