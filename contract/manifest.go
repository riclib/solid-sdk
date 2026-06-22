// Package contract holds the wire types a partner solution and the Solid
// platform exchange over NATS/JetStream/KV. These are pure data structures —
// no behavior, no v4 dependency — so both sides marshal the same bytes.
//
// Today the surface is the capability announcement (a manifest into a KV
// bucket) and the tool-call envelope (request-reply). Dashboard/workflow
// bindings and the data-model registration land here as they get their first
// real wire.
package contract

// SolutionManifest is what a partner solution announces into the solutions KV
// bucket. The platform watches the bucket and wires the solution's surface
// live; a stale/absent entry (TTL or heartbeat) greys it out.
//
// It mirrors the in-tree solutions.Solution manifest, but as a pure wire type:
// the in-process version carries Go func pointers (OnRegister, Validate); the
// wire version carries only declarative data the platform can render and route.
type SolutionManifest struct {
	Name         string           `json:"name"`
	DisplayName  string           `json:"display_name"`
	Description  string           `json:"description"`
	Icon         string           `json:"icon,omitempty"`
	SystemPrompt string           `json:"system_prompt,omitempty"`
	Tools        []ToolDescriptor `json:"tools,omitempty"`

	// Version lets the platform reason about a solution's published artifact
	// across upgrades (the additive-only SDK / reconciliation story). Free-form
	// for now; semver once the channel needs it.
	Version string `json:"version,omitempty"`

	// Dashboards and Workflows bindings are deferred — they're the "bound"
	// declarative tier (a workflow binds a skill slug; a dashboard binds a
	// store) and need announce-time validation. Start with the tool surface.
}

// ToolDescriptor is the LLM-facing schema for a tool the solution serves over
// NATS request-reply. It carries exactly what the platform needs to advertise
// the tool to the agent loop — the same (name, description, JSON-schema
// parameters) triple the in-process providers.Tool carries.
type ToolDescriptor struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"`
}
