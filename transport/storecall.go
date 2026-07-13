package transport

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/nats-io/nats.go"

	"github.com/riclib/solid-sdk/contract"
)

// StoreCall sends a governed store-proxy request to the platform and waits for
// the reply. This is the INVERSE direction of CallTool: the solution is the
// caller and the PLATFORM is the responder. The solution names a store,
// workspace, and op; the platform runs its two-hop grant check and executes with
// credentials it never releases (design: v4 docs/design/store-proxy-over-nats.md,
// S-1712). The ctx deadline bounds the wait — request-reply is live, not a
// durable queue.
//
// Liveness semantics mirror CallTool exactly: a transport failure (no responder,
// timeout) returns a Go error; an operation-level failure (the call reached the
// platform but was denied, the store was missing, or the statement errored)
// returns a successful StoreCallResult with Error + Code set — the caller checks
// res.Code to tell policy from outage.
func StoreCall(ctx context.Context, nc *nats.Conn, req contract.StoreCallRequest) (contract.StoreCallResult, error) {
	var zero contract.StoreCallResult
	if req.Solution == "" {
		return zero, fmt.Errorf("store call: solution is required")
	}
	if req.Op == "" {
		return zero, fmt.Errorf("store call %s: op is required", req.Solution)
	}
	body, err := json.Marshal(req)
	if err != nil {
		return zero, fmt.Errorf("store call %s/%s: marshal request: %w", req.Solution, req.Op, err)
	}
	msg, err := nc.RequestWithContext(ctx, contract.StoreCallSubject(req.Solution, req.Op), body)
	if err != nil {
		return zero, fmt.Errorf("store call %s/%s: %w", req.Solution, req.Op, err)
	}
	var res contract.StoreCallResult
	if err := json.Unmarshal(msg.Data, &res); err != nil {
		return zero, fmt.Errorf("store call %s/%s: unmarshal result: %w", req.Solution, req.Op, err)
	}
	return res, nil
}
