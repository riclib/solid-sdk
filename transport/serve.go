package transport

import (
	"context"
	"encoding/json"

	"github.com/nats-io/nats.go"

	"github.com/riclib/solid-sdk/contract"
)

// ToolHandler runs a single tool call on the solution (partner) side. The
// ScopedIdentity is the agent-as-lens envelope — enforce role/interactivity
// gates against it here. The handler returns a ToolCallResult; a declined or
// failed call sets Result.Error (data), it does not return a Go error (there is
// no caller to return one to across the bus).
type ToolHandler func(ctx context.Context, id contract.ScopedIdentity, args json.RawMessage) contract.ToolCallResult

// ServeTool subscribes to a tool's request-reply subject and runs handler for
// each call, replying with the marshalled result. Returns the subscription so
// the caller can Drain/Unsubscribe on shutdown.
//
// Each call runs on its own goroutine (nats.go delivers per-message); the
// handler must be safe for concurrent calls — it holds only the scoped identity
// it was handed, no shared mutable state.
func ServeTool(nc *nats.Conn, solution, tool string, handler ToolHandler) (*nats.Subscription, error) {
	subj := contract.ToolSubject(solution, tool)
	return nc.Subscribe(subj, func(msg *nats.Msg) {
		var req contract.ToolCallRequest
		if err := json.Unmarshal(msg.Data, &req); err != nil {
			respond(msg, contract.ToolCallResult{Error: "bad request: " + err.Error()})
			return
		}
		// ctx lifetime = the turn, not a durable queue; a future revision
		// carries a deadline from the request envelope.
		res := handler(context.Background(), req.Identity, req.Args)
		respond(msg, res)
	})
}

func respond(msg *nats.Msg, res contract.ToolCallResult) {
	b, err := json.Marshal(res)
	if err != nil {
		b, _ = json.Marshal(contract.ToolCallResult{Error: "marshal result: " + err.Error()})
	}
	_ = msg.Respond(b)
}
