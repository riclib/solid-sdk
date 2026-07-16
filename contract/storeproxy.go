package contract

import "fmt"

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

	// Op is the operation: StoreOpExec | StoreOpQuery | StoreOpExecQuery |
	// StoreOpTestConnection. It MUST match the subject's op segment
	// (StoreCallSubject).
	Op string `json:"op"`

	// Statement is the SQL for exec/query/exec_query (e.g. CALL <proc>(…) for
	// exec). Empty for test_connection.
	Statement string `json:"statement,omitempty"`

	// Args are positional bind args for the statement (exec, exec_query). The
	// query op does NOT bind args — it is read-shaped and the store's Query
	// takes opts, not args; a query that needs binds wants exec_query (S-1761).
	Args []any `json:"args,omitempty"`

	// Opts are store-specific query options (e.g. limit); the responder clamps
	// opts["limit"] to a server-side max and sets HasMore (query). Ignored by
	// exec_query, which runs the statement verbatim.
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
	// StoreOpExec runs a statement with positional bind args and NO result set
	// (DDL, CALL <proc>(…), INSERT/UPDATE). The v1 op. No result set means the
	// ~1 MB NATS reply ceiling is a non-issue. A CALL whose OUT values must come
	// back wants StoreOpExecQuery, not this.
	StoreOpExec = "exec"

	// StoreOpQuery runs a read-shaped statement and returns Columns + Rows; the
	// responder clamps opts["limit"] and sets HasMore. The caller pages exactly
	// like the in-process API.
	//
	// query BINDS NO ARGS and does not run the statement verbatim: it is the
	// read path, so the store is free to wrap the statement for pagination/sort
	// (the Databricks store rewrites it to `WITH _q AS (<stmt>) … LIMIT n`).
	// Request.Args are IGNORED here. A statement that carries binds, or that
	// must not be rewritten (a BEGIN…END scripting block cannot be CTE-wrapped),
	// wants StoreOpExecQuery.
	StoreOpQuery = "query"

	// StoreOpExecQuery runs a statement VERBATIM with positional bind args and
	// returns its result set (S-1761). It is exec's execution shape — no
	// pagination wrapping, no LIMIT rewrite, Opts ignored — with query's reply
	// shape (Columns + Rows).
	//
	// The motivating case is a stored-procedure CALL whose OUT values are read
	// back: Databricks binds OUT args to variables only, so the statement is a
	// SQL-scripting block that DECLAREs, CALLs with the IN params, and SELECTs
	// the OUT variables as a one-row result set — needing binds AND rows at
	// once, which neither exec (no rows) nor query (no binds) provides.
	//
	// Because there is no LIMIT wrapper, the RESPONDER caps rows server-side and
	// sets HasMore rather than risking the ~1 MB NATS reply ceiling: exec_query
	// is for the handful of rows a statement reports about itself, not for bulk
	// reads. Use query to page real result sets.
	//
	// A store that does not implement the capability replies StoreCodeUnsupportedOp.
	StoreOpExecQuery = "exec_query"

	// StoreOpTestConnection delegates to Store.TestConnection — nearly free,
	// carries no statement.
	StoreOpTestConnection = "test_connection"

	// StoreOpConnect is the workspace-engine connect handshake (S-1721/S-1728):
	// a solution bound to a workspace asks the platform for a quack connection to
	// that workspace's engine. It rides the SAME subject family
	// (solid.store.call.<solution>.connect), so the per-account publish prefix
	// (StoreCallSubjectPrefix, S-1706) covers it with no extra grant.
	//
	// connect is NOT a store op: the request carries solution + workspace + op
	// ONLY — a connect that smuggles Store/Statement/Args is denied with the
	// uniform not_granted (no existence leak). The BINDING is the grant: the
	// named workspace must draw the calling solution; there is no store hop, and
	// workspace engines never appear in a workspace's store-grant list. The
	// reply is QuackConnectResult, not StoreCallResult. Use transport.QuackConnect,
	// which builds the request so smuggling is impossible by construction.
	StoreOpConnect = "connect"
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

	// StoreCodeDryRun — the store is configured exec-dry-run on the platform: the
	// grant check passed and the statement was audit-logged platform-side, but it
	// was NOT executed and no credential was touched. Exec-only (query and
	// test_connection still run real). Callers MUST treat this as "not executed",
	// never as success — e.g. solid-mon lands it in kpi_export_runs.export_error
	// so a dry run can never masquerade as a real export.
	StoreCodeDryRun = "dry_run"
)

// QuackConnectResult is the reply payload of the connect op (StoreOpConnect,
// S-1721/S-1728). It MIRRORS the platform's app/storeproxy.QuackConnectResult
// — the platform shipped first and its responder is the source of truth; the
// FIELD NAMES (json tags) ARE THE WIRE CONTRACT — do not rename, additive only.
//
// Semantics follow StoreCallResult exactly: a policy denial or engine failure
// comes back as a SUCCESSFUL reply with Error + Code set (not_granted for
// unknown workspace / not bound / malformed connect — one uniform message, no
// existence leak; exec_failed "workspace engine unavailable" when the engine
// could not boot); a transport failure (no responder, NATS timeout) is a Go
// error at the caller. The caller tells policy from outage.
//
// THE RECONNECT CONTRACT: Token is minted PER ENGINE BOOT and engines are
// disposable by design — an LRU eviction, a compaction, a platform restart, or
// a crash retires the engine, and the next boot mints a new token (usually on
// a new port). Treat {URI, Token} as per-session state: on any connection
// failure, re-run the handshake (transport.QuackConnect) and retry. Never
// persist a handle across your own restarts, never share it between
// processes, and NEVER log the token.
type QuackConnectResult struct {
	// Error is a safe, human-readable failure message when the handshake was
	// denied or the engine could not be resolved. Empty on success. Never
	// carries a credential, path, or connection detail.
	Error string `json:"error,omitempty"`

	// Code is the machine-readable denial/failure code: StoreCodeNotGranted
	// (the binding-is-the-grant check failed, or the connect smuggled store
	// fields) or StoreCodeExecFailed (engine boot/resolve failed). Empty on
	// success.
	Code string `json:"code,omitempty"`

	// URI is the engine's quack URI (e.g. quack:localhost:9601). Loopback by
	// construction today — the pinned quack extension has no server-side TLS,
	// so the platform refuses to bind engines off-box.
	URI string `json:"uri,omitempty"`

	// Token is the engine's PER-BOOT auth token: minted at engine boot,
	// resolved only through this handshake, never configured and NEVER logged.
	// It stops working when the engine reboots/evicts — re-run the handshake
	// on connection failure (the reconnect contract above).
	Token string `json:"token,omitempty"`

	// TLS reports whether the engine serves TLS. Always false today (loopback
	// engines, no server-side TLS in the pinned quack extension) — connect
	// with disable_ssl=>true. When it flips true, connect with
	// disable_ssl=>false; the wire shape does not change.
	TLS bool `json:"tls"`

	// Duration is the server-side handling time in milliseconds (includes an
	// engine boot on first access — ~73ms measured).
	Duration int64 `json:"duration_ms"`
}

// StatementMarker is the statement-log attribution prefix for statements a
// solution runs over its quack connection (docs: the platform's
// docs/solution-stores.md in this repo, "Your statements are logged"). The serving
// engine logs statements at engine grain (= workspace grain) and cannot tell
// which caller a wire connection belongs to; prefixing every DML statement
// with this marker — the comment survives verbatim into the log line — gives
// the platform operator statement-level attribution:
//
//	stmt := contract.StatementMarker("revassure") + "INSERT INTO revassure.interestingevents SELECT ..."
//
// The marker is a convention, not enforcement (the peek doctrine: observable,
// not prevented) — but a solution that omits it is indistinguishable in the
// statement log from every other caller on the workspace, so use it on every
// statement.
func StatementMarker(solution string) string {
	return fmt.Sprintf("/* solid:solution=%s */ ", solution)
}
