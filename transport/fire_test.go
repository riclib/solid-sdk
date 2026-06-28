package transport_test

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/riclib/solid-sdk/contract"
	"github.com/riclib/solid-sdk/transport"
)

// awaitFireResult reads this fire's terminal disposition off the durable result
// stream (mirrors RunRunnable's result consumer) — an ephemeral consumer over the
// durable stream, filtered to the run key.
func awaitFireResult(t *testing.T, js jetstream.JetStream, runKey string, within time.Duration) contract.FireResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), within)
	defer cancel()

	stream, err := js.Stream(ctx, transport.FireResultStream)
	if err != nil {
		t.Fatalf("get result stream: %v", err)
	}
	cons, err := stream.CreateOrUpdateConsumer(ctx, jetstream.ConsumerConfig{
		FilterSubject: contract.FireResultSubject(runKey),
		AckPolicy:     jetstream.AckExplicitPolicy,
		DeliverPolicy: jetstream.DeliverAllPolicy,
	})
	if err != nil {
		t.Fatalf("result consumer: %v", err)
	}
	resCh := make(chan contract.FireResult, 1)
	cc, err := cons.Consume(func(msg jetstream.Msg) {
		var r contract.FireResult
		if err := json.Unmarshal(msg.Data(), &r); err != nil {
			_ = msg.Term()
			return
		}
		_ = msg.Ack()
		select {
		case resCh <- r:
		default:
		}
	})
	if err != nil {
		t.Fatalf("consume result: %v", err)
	}
	defer cc.Stop()

	select {
	case r := <-resCh:
		return r
	case <-ctx.Done():
		t.Fatalf("no fire result for %q within %s", runKey, within)
		return contract.FireResult{}
	}
}

// TestFireWire_RoundTrip is the fire wire end-to-end over embedded NATS: the
// solution side PublishFire enqueues a request on the durable work-queue; the
// platform side ServeFires validates it, "launches" a run, and publishes the
// durable disposition. Asserts the request crossed the queue intact and the
// accepted disposition (with the created conv id) came back.
func TestFireWire_RoundTrip(t *testing.T) {
	nc := startEmbeddedNATS(t)
	js, err := jetstream.New(nc)
	if err != nil {
		t.Fatalf("jetstream: %v", err)
	}
	ctx := context.Background()

	// --- platform side: serve fires ---
	var got contract.FireRequest
	stop, err := transport.ServeFires(ctx, js, func(_ context.Context, req contract.FireRequest) (contract.FireResult, error) {
		got = req
		// The workspace is validated platform-side, never trusted off the wire.
		if req.Workspace == "" || req.Workflow == "" {
			return contract.FireResult{Status: contract.FireStatusRejected, Error: "missing workspace/workflow"}, nil
		}
		return contract.FireResult{
			Status:  contract.FireStatusAccepted,
			ConvID:  "conv-001",
			Message: "fired servicedesk-correlate-intake in lvrtc",
		}, nil
	})
	if err != nil {
		t.Fatalf("serve fires: %v", err)
	}
	defer stop()

	// --- solution side: publish a fire ---
	req := contract.FireRequest{
		Solution:  "servicedesk",
		Workspace: "lvrtc",
		Workflow:  "servicedesk-correlate-intake",
		Question:  "SRS reports eID logins failing since this morning.",
		RunKey:    "REF-260628-091500-01",
	}
	if err := transport.PublishFire(ctx, js, req); err != nil {
		t.Fatalf("publish fire: %v", err)
	}

	res := awaitFireResult(t, js, req.RunKey, 10*time.Second)

	if res.Status != contract.FireStatusAccepted {
		t.Fatalf("disposition status = %q, want %q (err=%q)", res.Status, contract.FireStatusAccepted, res.Error)
	}
	if res.RunKey != req.RunKey {
		t.Fatalf("disposition run key = %q, want %q", res.RunKey, req.RunKey)
	}
	if res.ConvID != "conv-001" {
		t.Fatalf("disposition conv id = %q, want conv-001", res.ConvID)
	}

	// The request crossed the durable queue intact.
	if got.Workspace != "lvrtc" || got.Workflow != "servicedesk-correlate-intake" {
		t.Fatalf("handler saw %+v, want workspace=lvrtc workflow=servicedesk-correlate-intake", got)
	}
	if got.Question == "" {
		t.Fatal("handler saw empty Question, want the seeded issue text")
	}
}

// TestFireWire_Rejected proves a permanent decline propagates as a DATA
// disposition: a handler returning FireStatusRejected (nil Go error) surfaces in
// the durable FireResult and the trigger is acked (not redelivered).
func TestFireWire_Rejected(t *testing.T) {
	nc := startEmbeddedNATS(t)
	js, err := jetstream.New(nc)
	if err != nil {
		t.Fatalf("jetstream: %v", err)
	}
	ctx := context.Background()

	stop, err := transport.ServeFires(ctx, js, func(_ context.Context, _ contract.FireRequest) (contract.FireResult, error) {
		return contract.FireResult{Status: contract.FireStatusRejected, Error: "workflow not registered in workspace"}, nil
	})
	if err != nil {
		t.Fatalf("serve fires: %v", err)
	}
	defer stop()

	req := contract.FireRequest{
		Solution: "servicedesk", Workspace: "lvrtc", Workflow: "nope", RunKey: "REF-x-1",
	}
	if err := transport.PublishFire(ctx, js, req); err != nil {
		t.Fatalf("publish fire: %v", err)
	}

	res := awaitFireResult(t, js, req.RunKey, 10*time.Second)
	if res.Status != contract.FireStatusRejected {
		t.Fatalf("disposition status = %q, want %q", res.Status, contract.FireStatusRejected)
	}
	if res.Error == "" {
		t.Fatal("rejected disposition has empty Error, want the reason")
	}
}

// TestFireCapability_Manifest proves the declarative half: a solution's fire
// CAPABILITY (SolutionManifest.Fires) announces in the manifest header — like
// Partner, no leaf — and assembles back intact, so the platform can grant the
// publish permission + render the operator approval context.
func TestFireCapability_Manifest(t *testing.T) {
	nc := startEmbeddedNATS(t)
	js, err := jetstream.New(nc)
	if err != nil {
		t.Fatalf("jetstream: %v", err)
	}
	ctx := context.Background()
	kv, err := transport.EnsureSolutionsBucket(ctx, js)
	if err != nil {
		t.Fatalf("ensure bucket: %v", err)
	}

	err = transport.PublishSolution(ctx, kv, transport.SolutionPublish{
		Name:        "servicedesk",
		DisplayName: "Service Desk",
		Version:     "0.1.0",
		Fires: []contract.FireDescriptor{{
			Workflow:    "servicedesk-correlate-intake",
			DisplayName: "Correlate Portal Intake",
			Description: "a public-portal intake report → correlate against ongoing situations",
			Source:      "portal.intake",
		}},
	})
	if err != nil {
		t.Fatalf("publish: %v", err)
	}

	seen := make(chan contract.Solution, 1)
	if err := transport.WatchSolutions(ctx, kv, func(sol contract.Solution) { seen <- sol }, nil); err != nil {
		t.Fatalf("watch: %v", err)
	}
	select {
	case sol := <-seen:
		if len(sol.Manifest.Fires) != 1 {
			t.Fatalf("assembled fires = %+v, want one", sol.Manifest.Fires)
		}
		f := sol.Manifest.Fires[0]
		if f.Workflow != "servicedesk-correlate-intake" || f.Source != "portal.intake" {
			t.Fatalf("fire descriptor did not round-trip: %+v", f)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("did not observe announced solution within 3s")
	}
}

// TestFireWire_Dedup proves the RunKey idempotency layer: two publishes with the
// same RunKey inside the dedup window collapse to one stored message, so the
// platform handler fires exactly once.
func TestFireWire_Dedup(t *testing.T) {
	nc := startEmbeddedNATS(t)
	js, err := jetstream.New(nc)
	if err != nil {
		t.Fatalf("jetstream: %v", err)
	}
	ctx := context.Background()

	var calls int32
	stop, err := transport.ServeFires(ctx, js, func(_ context.Context, _ contract.FireRequest) (contract.FireResult, error) {
		atomic.AddInt32(&calls, 1)
		return contract.FireResult{Status: contract.FireStatusAccepted, ConvID: "conv-dedup"}, nil
	})
	if err != nil {
		t.Fatalf("serve fires: %v", err)
	}
	defer stop()

	req := contract.FireRequest{
		Solution: "servicedesk", Workspace: "lvrtc", Workflow: "servicedesk-correlate-intake", RunKey: "REF-dup-1",
	}
	if err := transport.PublishFire(ctx, js, req); err != nil {
		t.Fatalf("publish fire #1: %v", err)
	}
	if err := transport.PublishFire(ctx, js, req); err != nil {
		t.Fatalf("publish fire #2: %v", err)
	}

	// Wait for the first to be handled, then confirm no second handling.
	res := awaitFireResult(t, js, req.RunKey, 10*time.Second)
	if res.Status != contract.FireStatusAccepted {
		t.Fatalf("disposition status = %q, want accepted", res.Status)
	}
	time.Sleep(500 * time.Millisecond)
	if n := atomic.LoadInt32(&calls); n != 1 {
		t.Fatalf("handler fired %d times, want exactly 1 (RunKey dedup)", n)
	}
}
