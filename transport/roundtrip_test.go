package transport_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/riclib/solid-sdk/contract"
	"github.com/riclib/solid-sdk/transport"
)

// startEmbeddedNATS spins up an in-process JetStream-enabled NATS server and
// returns a connected client. Mirrors v4's embedded-server pattern so the test
// exercises the same substrate the platform runs.
func startEmbeddedNATS(t *testing.T) *nats.Conn {
	t.Helper()
	opts := &server.Options{
		Host:      "127.0.0.1",
		Port:      -1, // random free port
		JetStream: true,
		StoreDir:  t.TempDir(),
		NoSigs:    true,
	}
	s, err := server.NewServer(opts)
	if err != nil {
		t.Fatalf("new embedded nats: %v", err)
	}
	go s.Start()
	if !s.ReadyForConnections(10 * time.Second) {
		t.Fatal("embedded nats not ready")
	}
	t.Cleanup(s.Shutdown)

	nc, err := nats.Connect(s.ClientURL())
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(nc.Close)
	return nc
}

// TestRevAssureQueryRoundTrip is the first wire end-to-end: a partner solution
// announces a revassure-shaped manifest to the solutions KV bucket, the
// platform side discovers it via the watch, then routes a revassure_query tool
// call over request-reply with a scoped-identity envelope and reads the result.
//
// This is the strangler proof — the same announce + serve + call the eventual
// cross-process split will use, here in one process over loopback NATS.
func TestRevAssureQueryRoundTrip(t *testing.T) {
	nc := startEmbeddedNATS(t)
	js, err := jetstream.New(nc)
	if err != nil {
		t.Fatalf("jetstream: %v", err)
	}
	ctx := context.Background()

	// --- partner (fork) side: publish the solution tree + serve the tool ---
	kv, err := transport.EnsureSolutionsBucket(ctx, js)
	if err != nil {
		t.Fatalf("ensure bucket: %v", err)
	}

	publish := transport.SolutionPublish{
		Name:        "revassure",
		DisplayName: "Revenue Assurance",
		Description: "Telco B2C revenue assurance.",
		Version:     "0.1.0",
		Tools: []contract.ToolDescriptor{{
			Name:        "revassure_query",
			Description: "Query the LMT revenue-assurance store using DuckDB SQL.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"sql": map[string]any{"type": "string"},
				},
				"required": []string{"sql"},
			},
		}},
	}
	if err := transport.PublishSolution(ctx, kv, publish); err != nil {
		t.Fatalf("publish: %v", err)
	}

	var gotIdentity contract.ScopedIdentity
	sub, err := transport.ServeTool(nc, "revassure", "revassure_query",
		func(_ context.Context, id contract.ScopedIdentity, args json.RawMessage) contract.ToolCallResult {
			gotIdentity = id
			// The envelope is the agent-as-lens scope — gate on it.
			if id.Workspace == "" {
				return contract.ToolCallResult{Error: "no workspace scope"}
			}
			var a struct {
				SQL string `json:"sql"`
			}
			_ = json.Unmarshal(args, &a)
			if a.SQL == "" {
				return contract.ToolCallResult{Error: "sql is required"}
			}
			return contract.ToolCallResult{
				Output:       "Query returned 1 rows.\n\nColumns: eur\n\nData:\n[{\"eur\":4100}]",
				AccessCounts: map[string]int{"report_weekly": 1},
			}
		})
	if err != nil {
		t.Fatalf("serve tool: %v", err)
	}
	defer sub.Unsubscribe() //nolint:errcheck

	// --- platform side: discover + assemble the solution via the KV watch ---
	seen := make(chan contract.Solution, 1)
	if err := transport.WatchSolutions(ctx, kv,
		func(sol contract.Solution) { seen <- sol },
		nil,
	); err != nil {
		t.Fatalf("watch: %v", err)
	}
	select {
	case sol := <-seen:
		if sol.Manifest.Name != "revassure" {
			t.Fatalf("announced solution name = %q, want revassure", sol.Manifest.Name)
		}
		if sol.Manifest.Revision == 0 {
			t.Fatal("assembled manifest has revision 0, want >=1")
		}
		// The tool leaf was resolved from its own KV key, not the manifest blob.
		if len(sol.Tools) != 1 || sol.Tools[0].Name != "revassure_query" {
			t.Fatalf("assembled tools = %+v, want [revassure_query]", sol.Tools)
		}
		if len(sol.Manifest.Artifacts) != 1 || sol.Manifest.Artifacts[0].Kind != contract.ArtifactTool {
			t.Fatalf("manifest index = %+v, want one tool ref", sol.Manifest.Artifacts)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("did not observe announced solution within 3s")
	}

	// --- platform side: route a tool call to the partner-served tool ---
	args, _ := json.Marshal(map[string]string{"sql": "SELECT SUM(eur_impact) FROM report_weekly"})
	callCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	res, err := transport.CallTool(callCtx, nc, "revassure", "revassure_query", contract.ToolCallRequest{
		Identity: contract.ScopedIdentity{
			Identity:    "user@lmt",
			Workspace:   "lmt",
			Role:        "admin",
			Interactive: true,
		},
		Args: args,
	})
	if err != nil {
		t.Fatalf("call tool: %v", err)
	}
	if res.Error != "" {
		t.Fatalf("tool returned error: %s", res.Error)
	}
	if res.Output == "" {
		t.Fatal("tool returned empty output")
	}
	if res.AccessCounts["report_weekly"] != 1 {
		t.Fatalf("access counts = %+v, want report_weekly:1", res.AccessCounts)
	}

	// The scoped-identity envelope crossed the wire intact.
	if gotIdentity.Workspace != "lmt" || gotIdentity.Role != "admin" || !gotIdentity.Interactive {
		t.Fatalf("handler saw identity %+v, want workspace=lmt role=admin interactive=true", gotIdentity)
	}
}

// TestSkillWire_RoundTrip proves the control-plane skill wire: a solution
// announces a skill (pure markdown content, no data plane) as its own leaf, the
// platform discovers it via the watch, and assembles the full skill body back
// from the `<name>.skill.<id>` leaf — alongside a tool, to show the tree carries
// mixed artifact kinds. This is the clean first real integration: a skill needs
// no quack/store, only the LLM context it lands in.
func TestSkillWire_RoundTrip(t *testing.T) {
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

	body := "# Weekly Revenue Assurance Check\n\nReconcile provisioning/billing/CRM and publish leakage findings with EUR impact."
	err = transport.PublishSolution(ctx, kv, transport.SolutionPublish{
		Name:        "revassure",
		DisplayName: "Revenue Assurance",
		Version:     "0.1.0",
		Tools: []contract.ToolDescriptor{
			{Name: "revassure_query", Description: "query the store", Parameters: map[string]any{"type": "object"}},
		},
		Skills: []contract.SkillArtifact{{
			ID:           "revassure-weekly-check",
			Name:         "Weekly Revenue Assurance Check",
			Description:  "Reconcile the week's signals and publish leakage findings.",
			Source:       "revassure",
			Tags:         []string{"revassure", "report"},
			OutputFormat: "report",
			Body:         body,
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
		if len(sol.Skills) != 1 {
			t.Fatalf("assembled skills = %+v, want one", sol.Skills)
		}
		sk := sol.Skills[0]
		if sk.ID != "revassure-weekly-check" || sk.OutputFormat != "report" {
			t.Fatalf("skill meta wrong: %+v", sk)
		}
		if sk.Body != body {
			t.Fatalf("skill body did not round-trip: got %q", sk.Body)
		}
		// The tree carried mixed kinds: tool + skill, two leaves, one manifest.
		if len(sol.Tools) != 1 || len(sol.Manifest.Artifacts) != 2 {
			t.Fatalf("expected 1 tool + 2 artifact refs, got tools=%d refs=%d", len(sol.Tools), len(sol.Manifest.Artifacts))
		}
	case <-time.After(3 * time.Second):
		t.Fatal("did not observe announced solution within 3s")
	}
}

// TestKVTree_BigArtifactStaysOffManifest is the 1 MB-limit proof: a solution
// with a large tool (a ~600 KB description, standing in for a big skill body or
// dashboard YAML) publishes fine because the artifact is its OWN leaf — and the
// manifest key stays tiny (core meta + a one-entry index), nowhere near the
// 1 MB payload cap. A single-blob manifest carrying the same payload would be
// over half the budget with one artifact.
func TestKVTree_BigArtifactStaysOffManifest(t *testing.T) {
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

	big := strings.Repeat("x", 600*1024)
	err = transport.PublishSolution(ctx, kv, transport.SolutionPublish{
		Name: "revassure",
		Tools: []contract.ToolDescriptor{
			{Name: "revassure_query", Description: big, Parameters: map[string]any{"type": "object"}},
		},
	})
	if err != nil {
		t.Fatalf("publish big artifact: %v", err)
	}

	// The manifest leaf is tiny — it holds only the index, not the body.
	mEntry, err := kv.Get(ctx, contract.ManifestKey("revassure"))
	if err != nil {
		t.Fatalf("get manifest: %v", err)
	}
	if len(mEntry.Value()) > 4*1024 {
		t.Fatalf("manifest is %d bytes — the big body leaked into it", len(mEntry.Value()))
	}

	// The body lives in its own leaf.
	tEntry, err := kv.Get(ctx, contract.ArtifactKey("revassure", contract.ArtifactTool, "revassure_query"))
	if err != nil {
		t.Fatalf("get tool leaf: %v", err)
	}
	if len(tEntry.Value()) < 600*1024 {
		t.Fatalf("tool leaf is %d bytes, expected the full body", len(tEntry.Value()))
	}
}

// TestPublish_RejectsOversizeLeaf proves a single artifact over the KV leaf cap
// is rejected up front with a clear, honest message — the cap is a tripwire for
// a malformed artifact (a context-breaking skill/prompt, an inlined-blob
// dashboard), not a quota to route around — rather than failing opaquely at the
// server.
func TestPublish_RejectsOversizeLeaf(t *testing.T) {
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

	oversize := strings.Repeat("y", transport.MaxArtifactSize+1)
	err = transport.PublishSolution(ctx, kv, transport.SolutionPublish{
		Name: "revassure",
		Tools: []contract.ToolDescriptor{
			{Name: "revassure_query", Description: oversize, Parameters: map[string]any{"type": "object"}},
		},
	})
	if err == nil {
		t.Fatal("expected oversize leaf to be rejected, got nil")
	}
	if !strings.Contains(err.Error(), "malformed artifact") {
		t.Fatalf("error should frame oversize as a malformed artifact to fix, got: %v", err)
	}
}

// TestCallTool_NoResponder proves a transport failure (no solution serving the
// subject) surfaces as a Go error, distinct from a tool-level Error result.
func TestCallTool_NoResponder(t *testing.T) {
	nc := startEmbeddedNATS(t)
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	_, err := transport.CallTool(ctx, nc, "revassure", "revassure_query", contract.ToolCallRequest{
		Identity: contract.ScopedIdentity{Identity: "u", Workspace: "lmt"},
		Args:     json.RawMessage(`{"sql":"SELECT 1"}`),
	})
	if err == nil {
		t.Fatal("expected transport error calling an unserved tool, got nil")
	}
}
