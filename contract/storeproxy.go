package contract

// The store-proxy wire — the FIRST solution→platform request-reply service.
// Every other live wire today runs platform→solution (CallTool, runnable-run,
// fire-run) or is a KV announce tree; here the solution is the caller and the
// PLATFORM is the responder.
//
// The doctrine (design: v4 repo docs/design/store-proxy-over-nats.md, S-1712):
// solutions never hold platform credentials. A solution names a store, a
// workspace, and an operation over NATS; the platform checks the workspace's
// grants (the two-hop check — the workspace draws the solution AND grants the
// store), executes with credentials it never releases, and replies with the
// result. The in-process governance boundary (domains/store: opaque Store
// instances, credentials resolved at Get() time, QueryTimeout applied) is
// extended over the wire, not bypassed.
//
// Liveness semantics mirror contract.CallTool exactly (see transport.StoreCall):
// a transport failure (no responder, NATS timeout) surfaces as a Go error at the
// caller; an operation-level failure (the call reached the platform but was
// denied, the store was missing, the statement errored) comes back as a
// SUCCESSFUL reply with Error + Code set. The caller tells policy (Code) from
// outage (Go error).

// StoreCallRequest is what a solution publishes to StoreCallSubject. It names
// the store, workspace (grant anchor), and operation; the platform enforces the
// two-hop grant check on every call before touching a credential.
type StoreCallRequest struct {
	// Solution is the calling solution's announced name. It MUST match the
	// subject's solution segment (StoreCallSubject) — the platform rejects a
	// mismatch with StoreCodeNotGranted. Once per-account subject permissions are
	// provisioned (S-1706), this claim becomes account-enforceable.
	Solution string `json:"solution"`

	// Workspace is the grant anchor (workspace slug). Grants live on the
	// workspace, not the store or the solution: the named workspace must draw the
	// calling solution AND grant the named store (§2 two-hop check).
	Workspace string `json:"workspace"`

	// Store is the platform store ID to execute against. Never a connection
	// string or credential — the platform resolves the credential at call time
	// and never releases it.
	Store string `json:"store"`

	// Op is the operation: StoreOpExec | StoreOpQuery | StoreOpTestConnection. It
	// MUST match the subject's op segment (StoreCallSubject).
	Op string `json:"op"`

	// Statement is the SQL for exec/query (e.g. CALL <proc>(…) for exec). Empty
	// for test_connection.
	Statement string `json:"statement,omitempty"`

	// Args are positional bind args for the statement (exec).
	Args []any `json:"args,omitempty"`

	// Opts are store-specific query options (e.g. limit); the responder clamps
	// opts["limit"] to a server-side max and sets HasMore (query).
	Opts map[string]any `json:"opts,omitempty"`
}

// StoreCallResult is the reply. Liveness semantics are contract.CallTool's:
// transport failure (no responder, timeout) = Go error at the caller;
// operation-level failure = successful reply with Error + Code set.
type StoreCallResult struct {
	// Error is a safe, human-readable failure message when the operation was
	// denied or failed. Empty on success. Never carries a credential or
	// connection string.
	Error string `json:"error,omitempty"`

	// Code is the machine-readable denial/failure code (the §5 StoreCode* set) —
	// lets the caller distinguish policy from outage without parsing Error.
	Code string `json:"code,omitempty"`

	// Columns are the result column names (query only).
	Columns []string `json:"columns,omitempty"`

	// Rows are the result rows keyed by column name (query only).
	Rows []map[string]any `json:"rows,omitempty"`

	// HasMore signals the server-side limit clamped the result set and the caller
	// should page (query only).
	HasMore bool `json:"has_more,omitempty"`

	// Duration is the server-side execution time in milliseconds.
	Duration int64 `json:"duration_ms"`
}

// Store operations — the op segment of StoreCallSubject and StoreCallRequest.Op.
const (
	// StoreOpExec runs a statement with no result set (DDL, CALL <proc>(…),
	// INSERT/UPDATE). The v1 op, the S-1711 consumer. No result set means the
	// ~1 MB NATS reply ceiling is a non-issue.
	StoreOpExec = "exec"

	// StoreOpQuery runs a read-shaped statement and returns Columns + Rows; the
	// responder clamps opts["limit"] and sets HasMore. The caller pages exactly
	// like the in-process API.
	StoreOpQuery = "query"

	// StoreOpTestConnection delegates to Store.TestConnection — nearly free,
	// carries no statement.
	StoreOpTestConnection = "test_connection"
)

// Denial/failure codes — StoreCallResult.Code. They mirror store.Service's error
// set plus the grant check (design §5), so the caller can tell a policy denial
// from an outage and a solution can surface the reason verbatim.
const (
	// StoreCodeNotFound — the named store does not exist (store.ErrNotFound).
	StoreCodeNotFound = "not_found"

	// StoreCodeNotGranted — the two-hop grant check failed, or the payload
	// solution did not match the subject segment.
	StoreCodeNotGranted = "not_granted"

	// StoreCodeInactive — the store is disabled (store.ErrInactive).
	StoreCodeInactive = "inactive"

	// StoreCodeCredentialMissing — no credential is configured for the store
	// (store.ErrCredentialMissing).
	StoreCodeCredentialMissing = "credential_missing"

	// StoreCodeCredentialInvalid — the credential failed to resolve/authenticate
	// (store.ErrCredentialInvalid).
	StoreCodeCredentialInvalid = "credential_invalid"

	// StoreCodeUnsupportedType — the store type is unknown to the platform
	// (store.ErrUnsupportedType).
	StoreCodeUnsupportedType = "unsupported_type"

	// StoreCodeUnsupportedOp — the store does not support the requested op (e.g.
	// lacks the Execer interface for exec).
	StoreCodeUnsupportedOp = "unsupported_op"

	// StoreCodeExecFailed — the statement itself errored (safe message, no
	// credential/connection detail).
	StoreCodeExecFailed = "exec_failed"

	// StoreCodeTimeout — the store's configured QueryTimeout elapsed server-side.
	StoreCodeTimeout = "timeout"
)
