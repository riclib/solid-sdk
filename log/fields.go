package log

// Shared structured-log field keys. Both the platform (v4) and partner
// solutions attach these via slog attrs so every record correlates on identical
// keys — a partner record lines up with a platform record without a per-side
// rename, whether read from local sinks or ingested centrally (Loki etc.).
// These are the convention the solution template wires on; treat them as a
// frozen vocabulary (additive only).
const (
	// FieldSolution names the solution emitting the record (e.g. "revassure").
	FieldSolution = "solution"

	// FieldRevision is the solution's announced revision, so platform-side log
	// readers can tie a record to the exact published version.
	FieldRevision = "revision"

	// FieldWorkspace is the workspace the run is scoped to.
	FieldWorkspace = "workspace"

	// FieldCorrID is the correlation id that threads one logical operation
	// (a turn, a tool call, a skill run) across the platform/partner boundary.
	FieldCorrID = "corr_id"
)
