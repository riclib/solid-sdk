package contract

import "encoding/json"

// ScopedIdentity is the agent-as-lens envelope carried on EVERY tool call. The
// solution runs on-behalf with this scope handed per request — never a standing
// full-access connection. This is the invariant from the partner-model doc made
// concrete on the wire: "(scoped-identity {identity, workspace, role,
// interactivity}, argsJSON) → result".
//
// A solution's tool MUST enforce its gates against this envelope (e.g.
// salesintegrity's ratify_mitigation requires Role>=admin AND Interactive),
// never against ambient state — there is no ambient state across the bus.
type ScopedIdentity struct {
	// Identity is the acting principal: a user id, or a workspace-scoped
	// service principal for principal-less runs (e.g. an anonymous portal
	// visitor firing a skill in a workspace). Never empty.
	Identity string `json:"identity"`

	// Workspace is the workspace the run is scoped to. The partner account's
	// NATS subject permissions bound it to the workspaces it's installed in;
	// this names which one this call is for.
	Workspace string `json:"workspace"`

	// Role is the resolved workspace role of Identity (e.g. "admin", "member").
	Role string `json:"role,omitempty"`

	// Interactive distinguishes an interactive session from a background run —
	// some writes gate on it.
	Interactive bool `json:"interactive"`
}

// ToolCallRequest is the request payload for a tool served over NATS
// request-reply. Args is the raw JSON the executor unmarshals itself (the same
// contract as the in-process ExecuteFromJSON(ctx, argsJSON)).
type ToolCallRequest struct {
	Identity ScopedIdentity  `json:"identity"`
	Args     json.RawMessage `json:"args"`
}

// ToolCallResult is the wire form of a tool result. It mirrors the platform's
// in-process Result (Output + AccessCounts); the platform-side bridge
// translates between this wire type and its internal one. Error carries a
// handler-level failure as data (a refusal, a bad query) — transport failures
// surface as a Go error from CallTool instead.
type ToolCallResult struct {
	// Output is the human-readable text the LLM reads next.
	Output string `json:"output"`

	// AccessCounts records which tables/resources the call touched (audit /
	// stream metadata). Optional.
	AccessCounts map[string]int `json:"access_counts,omitempty"`

	// Error is a non-empty handler-level error message (the call reached the
	// solution but the tool declined or failed). Empty on success.
	Error string `json:"error,omitempty"`
}
