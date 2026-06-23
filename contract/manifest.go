// Package contract holds the wire types a partner solution and the Solid
// platform exchange over NATS/JetStream/KV. These are pure data structures —
// no behavior, no v4 dependency — so both sides marshal the same bytes.
//
// A solution announces itself as a KV TREE, not a single blob: a small
// `<name>.manifest` (core metadata + an index of artifact refs) plus one leaf
// key per artifact (`<name>.tool.<id>`, `<name>.skill.<id>`, …). This keeps
// every KV value well under NATS's 1 MB payload limit — a big skill body or
// dashboard YAML is its own bounded leaf, never folded into one oversize
// descriptor. The manifest is the commit point: the platform watches
// `*.manifest` and reads the referenced leaves on change.
package contract

// ArtifactKind enumerates the leaf kinds a solution publishes. The manifest's
// index references artifacts by (Kind, ID); the platform reads each leaf at
// `<name>.<kind>.<id>`.
type ArtifactKind string

const (
	ArtifactTool      ArtifactKind = "tool"      // ID = tool name;  leaf payload = ToolDescriptor
	ArtifactSkill     ArtifactKind = "skill"     // ID = skill id;   leaf payload = SkillArtifact
	ArtifactPrompt    ArtifactKind = "prompt"    // ID = prompt id;  leaf payload = PromptArtifact
	ArtifactWorkflow  ArtifactKind = "workflow"  // ID = slug;       leaf payload = WorkflowArtifact
	ArtifactDashboard ArtifactKind = "dashboard" // ID = page id;    leaf payload = DashboardArtifact
)

// ArtifactRef is one entry in the manifest's index — kind + id is the leaf
// address. The platform iterates these and reads `<name>.<kind>.<id>`.
type ArtifactRef struct {
	Kind ArtifactKind `json:"kind"`
	ID   string       `json:"id"`
}

// SolutionManifest is the small root a solution announces at `<name>.manifest`.
// It carries the core declarative metadata plus the index of artifact leaves —
// NOT the artifact bodies. It is bounded by the number of artifacts (~50 bytes
// per ref), never by their size, so it cannot go oversize.
//
// It mirrors the in-tree solutions.Solution manifest, but as a pure wire type:
// the in-process version carries Go func pointers (OnRegister, Validate); the
// wire version carries only declarative data the platform can render and route.
type SolutionManifest struct {
	Name         string `json:"name"`
	DisplayName  string `json:"display_name"`
	Description  string `json:"description"`
	Icon         string `json:"icon,omitempty"`
	SystemPrompt string `json:"system_prompt,omitempty"`

	// Version is the solution's published artifact version (the additive-only
	// SDK / reconciliation story). Free-form for now; semver once the channel
	// needs it.
	Version string `json:"version,omitempty"`

	// Revision is bumped on every (re)publish. The manifest is the commit
	// point: a watcher reacts to a revision change and re-reads the index, so
	// even a content-only edit to one leaf is observed (the publisher rewrites
	// the manifest last on every change).
	Revision uint64 `json:"revision"`

	// Artifacts is the index of leaf keys this solution publishes. The platform
	// reads `<name>.<kind>.<id>` for each.
	Artifacts []ArtifactRef `json:"artifacts"`
}

// ToolDescriptor is the leaf payload for an ArtifactTool — the LLM-facing
// schema for a tool the solution serves over NATS request-reply. It carries the
// same (name, description, JSON-schema parameters) triple the in-process
// providers.Tool carries.
type ToolDescriptor struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"`
}

// SkillArtifact is the leaf payload for an ArtifactSkill — a reusable
// instruction set the agent activates for a domain workflow. It is PURE
// CONTENT: text that goes into the LLM context, with no data access — so a
// skill needs no data plane (unlike a tool, whose execution reads a store). It
// mirrors the in-tree skill.Skill's declarative fields plus the markdown body.
//
// Body is the markdown instruction set; it is bounded by the LLM context window
// (a megabyte skill is a context-breaker, not a storage case — see
// MaxArtifactSize), so it sits comfortably in one KV leaf.
type SkillArtifact struct {
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	Description  string   `json:"description"`
	Source       string   `json:"source,omitempty"`        // the solution that ships it
	Tags         []string `json:"tags,omitempty"`
	OutputFormat string   `json:"output_format,omitempty"` // e.g. "report"
	Body         string   `json:"body"`                    // the markdown instruction set
}

// PromptArtifact is the leaf payload for an ArtifactPrompt — a reusable prompt
// the solution ships into the agent. Like a skill it is PURE CONTROL-PLANE
// CONTENT: text that goes into the LLM context, with no data access — so it
// needs no data plane.
//
// Body is the prompt text/markdown; it is bounded by the LLM context window (a
// megabyte prompt is a context-breaker, not a storage case — see
// MaxArtifactSize), so it sits comfortably in one KV leaf.
type PromptArtifact struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Source      string   `json:"source,omitempty"` // the solution that ships it
	Tags        []string `json:"tags,omitempty"`
	Body        string   `json:"body"` // the prompt text/markdown
}

// WorkflowArtifact is the leaf payload for an ArtifactWorkflow — an adaptive
// workflow definition the solution ships. Like a skill it is PURE CONTROL-PLANE
// CONTENT: a declarative definition the platform parses and runs, with no data
// access of its own.
//
// Body is the workflow definition YAML (the v4 side parses it); a workflow is
// ~100 B per step, so it sits comfortably under MaxArtifactSize in one KV leaf.
type WorkflowArtifact struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Source      string   `json:"source,omitempty"` // the solution that ships it
	Tags        []string `json:"tags,omitempty"`
	Body        string   `json:"body"` // the workflow definition YAML
}

// DashboardArtifact is the leaf payload for an ArtifactDashboard — a dashboard
// page the solution ships. Like a skill it is PURE CONTROL-PLANE CONTENT: a
// declarative surface the platform renders, with no data access of its own (its
// panels query through scoped tools, never an ambient store).
//
// Body is the dashboard DSL YAML (the v4 side parses it); a dashboard is tens
// of KB of YAML+SQL — no raw-HTML/base64 passthrough — so it sits comfortably
// under MaxArtifactSize in one KV leaf.
type DashboardArtifact struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Source      string   `json:"source,omitempty"` // the solution that ships it
	Tags        []string `json:"tags,omitempty"`
	Body        string   `json:"body"` // the dashboard DSL YAML
}

// Solution is the ASSEMBLED view the platform-side watcher hands to its
// callback: the manifest plus the resolved leaf artifacts.
type Solution struct {
	Manifest   SolutionManifest
	Tools      []ToolDescriptor
	Skills     []SkillArtifact
	Prompts    []PromptArtifact
	Workflows  []WorkflowArtifact
	Dashboards []DashboardArtifact
}
