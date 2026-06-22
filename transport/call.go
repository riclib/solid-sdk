package transport

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/nats-io/nats.go"

	"github.com/riclib/solid-sdk/contract"
)

// CallTool sends a tool-call request to a solution's subject and waits for the
// reply. The platform side calls this to route an agent's tool call to the
// partner-served tool. The ctx deadline bounds the wait — request-reply is
// live, tied to the running agent loop, NOT a durable queue (a lost turn is
// re-asked, never replayed).
//
// A transport failure (no responder, timeout) returns a Go error; a tool-level
// failure (the call reached the solution but it declined) returns a successful
// ToolCallResult with Error set — the caller checks res.Error.
func CallTool(ctx context.Context, nc *nats.Conn, solution, tool string, req contract.ToolCallRequest) (contract.ToolCallResult, error) {
	var zero contract.ToolCallResult
	body, err := json.Marshal(req)
	if err != nil {
		return zero, fmt.Errorf("call %s/%s: marshal request: %w", solution, tool, err)
	}
	msg, err := nc.RequestWithContext(ctx, contract.ToolSubject(solution, tool), body)
	if err != nil {
		return zero, fmt.Errorf("call %s/%s: %w", solution, tool, err)
	}
	var res contract.ToolCallResult
	if err := json.Unmarshal(msg.Data, &res); err != nil {
		return zero, fmt.Errorf("call %s/%s: unmarshal result: %w", solution, tool, err)
	}
	return res, nil
}
