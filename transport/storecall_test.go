package transport_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/riclib/solid-sdk/contract"
	"github.com/riclib/solid-sdk/transport"
)

// TestStoreCall_Subjects pins the subject shape — the solution name lives in the
// subject so a partner account permission scoped to the prefix covers every op.
func TestStoreCall_Subjects(t *testing.T) {
	if got := contract.StoreCallSubject("solidmon", contract.StoreOpExec); got != "solid.store.call.solidmon.exec" {
		t.Fatalf("StoreCallSubject = %q, want solid.store.call.solidmon.exec", got)
	}
	if got := contract.StoreCallSubjectPrefix("solidmon"); got != "solid.store.call.solidmon.>" {
		t.Fatalf("StoreCallSubjectPrefix = %q, want solid.store.call.solidmon.>", got)
	}
}

// TestStoreCall_ValidatesRequest proves an empty Solution or Op is rejected as a
// Go error BEFORE any publish — a malformed request never touches the bus.
func TestStoreCall_ValidatesRequest(t *testing.T) {
	nc := startEmbeddedNATS(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	if _, err := transport.StoreCall(ctx, nc, contract.StoreCallRequest{Op: contract.StoreOpExec}); err == nil {
		t.Fatal("expected error for empty solution, got nil")
	}
	if _, err := transport.StoreCall(ctx, nc, contract.StoreCallRequest{Solution: "solidmon"}); err == nil {
		t.Fatal("expected error for empty op, got nil")
	}
}

// TestStoreCall_RoundTrip is the first store-proxy wire end-to-end: a FAKE
// platform responder subscribes on the store-call subject over embedded NATS
// (the platform is the responder — the inverse of the tool wire). An exec call
// succeeds; a second call the responder declines comes back as a SUCCESSFUL
// reply with Error + Code set and NO Go error — the caller tells policy from
// outage.
func TestStoreCall_RoundTrip(t *testing.T) {
	nc := startEmbeddedNATS(t)
	ctx := context.Background()

	// --- fake platform side: subscribe on the store-call subject and respond ---
	sub, err := nc.Subscribe(contract.StoreCallSubjectPrefix("solidmon"), func(msg *nats.Msg) {
		var req contract.StoreCallRequest
		if err := json.Unmarshal(msg.Data, &req); err != nil {
			reply, _ := json.Marshal(contract.StoreCallResult{Error: "bad request", Code: contract.StoreCodeExecFailed})
			_ = msg.Respond(reply)
			return
		}
		// The two-hop grant check lives platform-side; here we fake its outcome:
		// workspace "ungranted" does not grant the store.
		if req.Workspace == "ungranted" {
			reply, _ := json.Marshal(contract.StoreCallResult{
				Error:    "workspace does not grant store",
				Code:     contract.StoreCodeNotGranted,
				Duration: 1,
			})
			_ = msg.Respond(reply)
			return
		}
		reply, _ := json.Marshal(contract.StoreCallResult{Duration: 7})
		_ = msg.Respond(reply)
	})
	if err != nil {
		t.Fatalf("subscribe responder: %v", err)
	}
	defer sub.Unsubscribe() //nolint:errcheck

	// --- solution side: a granted exec call succeeds ---
	callCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	res, err := transport.StoreCall(callCtx, nc, contract.StoreCallRequest{
		Solution:  "solidmon",
		Workspace: "lmt",
		Store:     "customer-databricks",
		Op:        contract.StoreOpExec,
		Statement: "CALL ingest_kpis(?)",
		Args:      []any{"run-42"},
	})
	if err != nil {
		t.Fatalf("store call: %v", err)
	}
	if res.Error != "" || res.Code != "" {
		t.Fatalf("granted call returned error: %q / %q", res.Error, res.Code)
	}
	if res.Duration != 7 {
		t.Fatalf("duration = %d, want 7", res.Duration)
	}

	// --- solution side: an operation-level denial rides the reply, not a Go error ---
	denied, err := transport.StoreCall(callCtx, nc, contract.StoreCallRequest{
		Solution:  "solidmon",
		Workspace: "ungranted",
		Store:     "customer-databricks",
		Op:        contract.StoreOpExec,
		Statement: "CALL ingest_kpis(?)",
	})
	if err != nil {
		t.Fatalf("denied call should NOT surface a Go error, got: %v", err)
	}
	if denied.Code != contract.StoreCodeNotGranted {
		t.Fatalf("denied call Code = %q, want %q", denied.Code, contract.StoreCodeNotGranted)
	}
	if denied.Error == "" {
		t.Fatal("denied call should carry a human-readable Error")
	}
}

// TestStoreCall_NoResponder proves a transport failure (nothing serving the
// store-proxy subject — the platform is down) surfaces as a Go error, distinct
// from an operation-level denial. Mirrors TestCallTool_NoResponder.
func TestStoreCall_NoResponder(t *testing.T) {
	nc := startEmbeddedNATS(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	_, err := transport.StoreCall(ctx, nc, contract.StoreCallRequest{
		Solution:  "solidmon",
		Workspace: "lmt",
		Store:     "customer-databricks",
		Op:        contract.StoreOpExec,
		Statement: "CALL ingest_kpis(?)",
	})
	if err == nil {
		t.Fatal("expected transport error calling the store proxy with no responder, got nil")
	}
}
