package transport_test

import (
	"context"
	"encoding/json"
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

	// --- partner (fork) side: announce the manifest + serve the tool ---
	kv, err := transport.EnsureSolutionsBucket(ctx, js)
	if err != nil {
		t.Fatalf("ensure bucket: %v", err)
	}

	manifest := contract.SolutionManifest{
		Name:        "revassure",
		DisplayName: "Revenue Assurance",
		Description: "Telco B2C revenue assurance.",
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
		Version: "0.1.0",
	}
	if err := transport.AnnounceSolution(ctx, kv, manifest); err != nil {
		t.Fatalf("announce: %v", err)
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

	// --- platform side: discover the solution via the KV watch ---
	seen := make(chan contract.SolutionManifest, 1)
	if err := transport.WatchSolutions(ctx, kv,
		func(m contract.SolutionManifest) { seen <- m },
		nil,
	); err != nil {
		t.Fatalf("watch: %v", err)
	}
	select {
	case m := <-seen:
		if m.Name != "revassure" {
			t.Fatalf("announced solution name = %q, want revassure", m.Name)
		}
		if len(m.Tools) != 1 || m.Tools[0].Name != "revassure_query" {
			t.Fatalf("announced tools = %+v, want [revassure_query]", m.Tools)
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
