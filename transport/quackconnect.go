package transport

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/nats-io/nats.go"

	"github.com/riclib/solid-sdk/contract"
)

// QuackConnect runs the workspace-engine connect handshake (S-1721/S-1728): a
// solution bound to a workspace asks the platform for a quack connection to
// that workspace's engine and gets back {uri, token, tls}. It rides the SAME
// wire as StoreCall (`solid.store.call.<solution>.connect` — the per-account
// publish prefix covers it), but connect is NOT a store op: the request
// carries solution + workspace ONLY, and this helper builds it so smuggling a
// store/statement/args is impossible by construction (the platform denies a
// smuggled connect with the uniform not_granted). The BINDING is the grant —
// there is no store to name. The ctx deadline bounds the wait; the platform
// side may include an engine boot (~73ms measured, generously bounded
// server-side), so give it a few seconds.
//
// Liveness semantics mirror StoreCall exactly: a transport failure (no
// responder, NATS timeout) returns a Go error; a handshake-level failure comes
// back as a successful QuackConnectResult with Error + Code set —
// StoreCodeNotGranted (unknown workspace / workspace doesn't draw you — one
// uniform message, no existence leak) or StoreCodeExecFailed ("workspace
// engine unavailable"). The caller checks res.Code to tell policy from outage.
//
// THE RECONNECT CONTRACT — how to hold the result:
//
// The token is minted PER ENGINE BOOT and engines are disposable by design
// (LRU eviction, compaction, platform restart, crash). Auth failures or
// connection refusals on an established handle are the normal signal that the
// engine was retired, not an incident. Treat {URI, Token} as per-session
// state: on ANY connection failure, re-run QuackConnect and reconnect —
// the re-handshake is cheap (the engine re-boots on first touch). Never
// persist the result across your own restarts, never share it between
// processes, and NEVER log the token (this helper never does; keep your
// error wrapping token-free too).
//
// Connecting with the result: use the quack package — quack.Connect wraps
// this handshake and owns the whole contract (pinned duckdb-go + air-gapped
// quack extension, disable_ssl driven by res.TLS, the statement marker on
// every statement, and the reconnect contract applied automatically). This
// helper stays public as the raw handshake for callers composing their own
// client. See ARCHITECTURE.md "Your quack connection".
func QuackConnect(ctx context.Context, nc *nats.Conn, solution, workspace string) (contract.QuackConnectResult, error) {
	var zero contract.QuackConnectResult
	if solution == "" {
		return zero, fmt.Errorf("quack connect: solution is required")
	}
	if workspace == "" {
		return zero, fmt.Errorf("quack connect %s: workspace is required", solution)
	}
	req := contract.StoreCallRequest{
		Solution:  solution,
		Workspace: workspace,
		Op:        contract.StoreOpConnect,
		// No Store, no Statement, no Args — a connect that smuggles any of
		// them is denied (uniform not_granted). This helper cannot smuggle.
	}
	body, err := json.Marshal(req)
	if err != nil {
		return zero, fmt.Errorf("quack connect %s/%s: marshal request: %w", solution, workspace, err)
	}
	msg, err := nc.RequestWithContext(ctx, contract.StoreCallSubject(solution, contract.StoreOpConnect), body)
	if err != nil {
		return zero, fmt.Errorf("quack connect %s/%s: %w", solution, workspace, err)
	}
	var res contract.QuackConnectResult
	if err := json.Unmarshal(msg.Data, &res); err != nil {
		return zero, fmt.Errorf("quack connect %s/%s: unmarshal result: %w", solution, workspace, err)
	}
	return res, nil
}
