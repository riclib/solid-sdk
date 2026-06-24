package transport_test

import (
	"context"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/riclib/solid-sdk/contract"
	"github.com/riclib/solid-sdk/transport"
)

// TestRunnableWire_RoundTrip is the runnable wire end-to-end over embedded
// NATS+JetStream: the solution side ServeRunnables registers a handler that
// emits two progress events then returns completed; the platform side
// RunRunnable triggers it over the durable work-queue, collects the streamed
// progress, and asserts the durable terminal result.
//
// This is the durable, scheduled-work analog of TestRevAssureQueryRoundTrip —
// the trigger + result ride JetStream (survive a disconnect), progress rides
// core NATS (ephemeral).
func TestRunnableWire_RoundTrip(t *testing.T) {
	nc := startEmbeddedNATS(t)
	js, err := jetstream.New(nc)
	if err != nil {
		t.Fatalf("jetstream: %v", err)
	}
	ctx := context.Background()

	// --- solution (fork) side: serve a runnable ---
	var gotIdentity contract.ScopedIdentity
	var gotConfigID string
	stop, err := transport.ServeRunnables(ctx, js, nc, "revassure",
		func(_ context.Context, req contract.RunnableRunRequest, emit func(contract.RunnableProgress)) contract.RunnableResult {
			gotIdentity = req.Identity
			gotConfigID = req.ConfigID
			// The envelope is the run-as-a-human scope — gate on it.
			if req.Identity.Workspace == "" {
				return contract.RunnableResult{Status: contract.RunStatusFailed, Error: "no workspace scope"}
			}
			emit(contract.RunnableProgress{Status: "running", Message: "loading parquet inbox"})
			emit(contract.RunnableProgress{Status: "running", Message: "projecting into snapshot"})
			return contract.RunnableResult{
				Status:  contract.RunStatusCompleted,
				Message: "loaded 12,000 rows into leakage_findings",
			}
		})
	if err != nil {
		t.Fatalf("serve runnables: %v", err)
	}
	defer stop()

	// --- platform side: trigger + collect progress + await result ---
	var progress []contract.RunnableProgress
	progCh := make(chan contract.RunnableProgress, 8)
	callCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	req := contract.RunnableRunRequest{
		Solution: "revassure",
		Type:     "revassure-datagen",
		ConfigID: "weekly",
		RunID:    "run-001",
		Identity: contract.ScopedIdentity{
			Identity:    "user@lmt",
			Workspace:   "lmt",
			Role:        "admin",
			Interactive: false,
		},
	}
	res, err := transport.RunRunnable(callCtx, js, nc, req, func(p contract.RunnableProgress) {
		progCh <- p
	})
	if err != nil {
		t.Fatalf("run runnable: %v", err)
	}

	// Drain whatever progress arrived (best-effort core-NATS telemetry).
	close(progCh)
	for p := range progCh {
		progress = append(progress, p)
	}

	// Terminal result asserts.
	if res.Status != contract.RunStatusCompleted {
		t.Fatalf("result status = %q, want %q (err=%q)", res.Status, contract.RunStatusCompleted, res.Error)
	}
	if res.RunID != "run-001" {
		t.Fatalf("result run id = %q, want run-001", res.RunID)
	}
	if res.Message == "" {
		t.Fatal("result message is empty, want the load summary")
	}

	// The run-as-a-human envelope crossed the durable queue intact.
	if gotIdentity.Workspace != "lmt" || gotIdentity.Role != "admin" {
		t.Fatalf("handler saw identity %+v, want workspace=lmt role=admin", gotIdentity)
	}
	if gotConfigID != "weekly" {
		t.Fatalf("handler saw config id %q, want weekly", gotConfigID)
	}

	// Progress is best-effort, but in a loopback test the two emits should
	// arrive before the result; assert we saw them with the right run id.
	if len(progress) < 2 {
		t.Fatalf("collected %d progress events, want at least 2: %+v", len(progress), progress)
	}
	for _, p := range progress {
		if p.RunID != "run-001" {
			t.Fatalf("progress event run id = %q, want run-001 (%+v)", p.RunID, p)
		}
	}
}

// TestRunnableWire_Failure proves a runnable-level failure propagates as data: a
// handler returning RunStatusFailed with an Error surfaces in the terminal
// RunnableResult (RunRunnable returns no Go error — that is reserved for
// transport failures).
func TestRunnableWire_Failure(t *testing.T) {
	nc := startEmbeddedNATS(t)
	js, err := jetstream.New(nc)
	if err != nil {
		t.Fatalf("jetstream: %v", err)
	}
	ctx := context.Background()

	stop, err := transport.ServeRunnables(ctx, js, nc, "revassure",
		func(_ context.Context, _ contract.RunnableRunRequest, _ func(contract.RunnableProgress)) contract.RunnableResult {
			return contract.RunnableResult{
				Status: contract.RunStatusFailed,
				Error:  "parquet inbox is empty — nothing to load",
			}
		})
	if err != nil {
		t.Fatalf("serve runnables: %v", err)
	}
	defer stop()

	callCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	res, err := transport.RunRunnable(callCtx, js, nc, contract.RunnableRunRequest{
		Solution: "revassure",
		Type:     "revassure-datagen",
		RunID:    "run-fail-1",
		Identity: contract.ScopedIdentity{Identity: "u", Workspace: "lmt"},
	}, nil)
	if err != nil {
		t.Fatalf("run runnable returned a transport error, want a failed RESULT: %v", err)
	}
	if res.Status != contract.RunStatusFailed {
		t.Fatalf("result status = %q, want %q", res.Status, contract.RunStatusFailed)
	}
	if res.Error == "" {
		t.Fatal("failed result has empty Error, want the handler's message")
	}
}

// TestRunnableDescriptor_RoundTrip proves the declarative half: a runnable
// descriptor announces as its own leaf (peer of a tool) and assembles back with
// its config options intact.
func TestRunnableDescriptor_RoundTrip(t *testing.T) {
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
		Name:        "revassure",
		DisplayName: "Revenue Assurance",
		Version:     "0.1.0",
		Runnables: []contract.RunnableDescriptor{{
			Type:        "revassure-datagen",
			DisplayName: "Revenue Assurance Datagen",
			Description: "Generate the week's revenue-assurance demo data.",
			ConfigOptions: []contract.ConfigOption{
				{ID: "weekly", Name: "Weekly", Description: "Full weekly dataset."},
				{ID: "smoke", Name: "Smoke", Description: "Tiny dataset for a smoke test."},
			},
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
		if len(sol.Runnables) != 1 {
			t.Fatalf("assembled runnables = %+v, want one", sol.Runnables)
		}
		rn := sol.Runnables[0]
		if rn.Type != "revassure-datagen" || rn.DisplayName != "Revenue Assurance Datagen" {
			t.Fatalf("runnable meta wrong: %+v", rn)
		}
		if len(rn.ConfigOptions) != 2 || rn.ConfigOptions[0].ID != "weekly" || rn.ConfigOptions[1].ID != "smoke" {
			t.Fatalf("config options did not round-trip: %+v", rn.ConfigOptions)
		}
		if len(sol.Manifest.Artifacts) != 1 || sol.Manifest.Artifacts[0].Kind != contract.ArtifactRunnable {
			t.Fatalf("manifest index = %+v, want one runnable ref", sol.Manifest.Artifacts)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("did not observe announced solution within 3s")
	}
}
