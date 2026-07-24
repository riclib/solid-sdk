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
		Partner: contract.Partner{
			Name:    "CUBE Systems SIA",
			URL:     "https://www.cubesystems.lv/",
			LogoRef: transport.AssetKey("revassure", "partner-logo"),
		},
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
		// The Partner block rode in the manifest index (not a leaf).
		if sol.Manifest.Partner.Name != "CUBE Systems SIA" ||
			sol.Manifest.Partner.LogoRef != transport.AssetKey("revassure", "partner-logo") {
			t.Fatalf("partner did not round-trip: %+v", sol.Manifest.Partner)
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

// TestSkillWire_QueriesActiveParameters_RoundTrip proves the v0.4.0 additions
// to SkillArtifact (S-1587; design: v4 repo docs/design/skill-named-queries.md)
// round-trip intact: named Queries, an explicit Active:false, and a reserved
// Parameters entry (the S-1590 general-parameter shape, defined on the wire
// now but not yet consumed by any harness turn).
func TestSkillWire_QueriesActiveParameters_RoundTrip(t *testing.T) {
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

	inactive := false
	err = transport.PublishSolution(ctx, kv, transport.SolutionPublish{
		Name:        "revassure",
		DisplayName: "Revenue Assurance",
		Version:     "0.1.0",
		Skills: []contract.SkillArtifact{{
			ID:           "revassure-deep-audit",
			Name:         "Deep Audit",
			Description:  "Opt-in deep reconciliation audit.",
			Source:       "revassure",
			Tags:         []string{"revassure", "report"},
			OutputFormat: "report",
			Body:         "# Deep Audit\n\nReconcile per {query:per_category} and {query:totals}.",
			Active:       &inactive,
			Queries: []contract.SkillQuery{
				{
					Name:        "per_category",
					Description: "Findings grouped by category for the period.",
					SQL:         "SELECT category, COUNT(*) AS cnt FROM report_weekly WHERE ts >= '{period_start}' AND ts < '{period_end}' GROUP BY category",
					MaxRows:     50,
				},
				{
					Name:        "totals",
					Description: "All-time container inventory (deliberately unbounded).",
					SQL:         "SELECT COUNT(*) AS cnt FROM containers",
					MaxRows:     1,
				},
			},
			Parameters: []contract.SkillParameter{{
				Name:        "container",
				Type:        "enum",
				Description: "Which container to scope the audit to.",
				Required:    false,
				Default:     "secrets",
				Values:      []string{"secrets", "billing", "crm"},
			}},
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
		if sk.Active == nil || *sk.Active != false {
			t.Fatalf("skill Active = %v, want pointer to false", sk.Active)
		}
		if len(sk.Queries) != 2 {
			t.Fatalf("skill queries = %+v, want 2", sk.Queries)
		}
		q0 := sk.Queries[0]
		if q0.Name != "per_category" || q0.MaxRows != 50 || !strings.Contains(q0.SQL, "{period_start}") {
			t.Fatalf("query[0] did not round-trip: %+v", q0)
		}
		q1 := sk.Queries[1]
		if q1.Name != "totals" || q1.MaxRows != 1 {
			t.Fatalf("query[1] did not round-trip: %+v", q1)
		}
		if len(sk.Parameters) != 1 {
			t.Fatalf("skill parameters = %+v, want 1", sk.Parameters)
		}
		p0 := sk.Parameters[0]
		if p0.Name != "container" || p0.Type != "enum" || p0.Default != "secrets" || len(p0.Values) != 3 {
			t.Fatalf("parameter did not round-trip: %+v", p0)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("did not observe announced solution within 3s")
	}
}

// TestSkillWire_ActiveNil_MeansActive proves the zero-value contract: a skill
// published without setting Active round-trips with a nil pointer, which
// consumers must treat as active (preserving every pre-v0.4.0 producer's
// behavior — S-1587).
func TestSkillWire_ActiveNil_MeansActive(t *testing.T) {
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
		Skills: []contract.SkillArtifact{{
			ID:          "revassure-weekly-check",
			Name:        "Weekly Check",
			Description: "Reconcile the week's signals.",
			Source:      "revassure",
			Body:        "# Weekly check",
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
		if sol.Skills[0].Active != nil {
			t.Fatalf("skill Active = %v, want nil (= active)", sol.Skills[0].Active)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("did not observe announced solution within 3s")
	}
}

// TestDeclarativeArtifacts_RoundTrip proves the three control-plane declarative
// wires (prompt, workflow, dashboard) announce as their own leaves and assemble
// back with their Body intact — alongside a skill, to show the tree carries all
// the control-plane kinds together. Each is pure content (no data plane), so
// like the skill wire they need no quack/store, only the LLM context / renderer
// they land in.
func TestDeclarativeArtifacts_RoundTrip(t *testing.T) {
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

	promptBody := "You are a revenue-assurance analyst. Reconcile provisioning, billing and CRM."
	workflowBody := "steps:\n  - reconcile\n  - publish_findings\n"
	dashboardBody := "title: Weekly Leakage\npanels:\n  - kind: table\n    sql: SELECT * FROM report_weekly\n"

	err = transport.PublishSolution(ctx, kv, transport.SolutionPublish{
		Name:        "revassure",
		DisplayName: "Revenue Assurance",
		Version:     "0.1.0",
		Skills: []contract.SkillArtifact{{
			ID: "revassure-weekly-check", Name: "Weekly Check",
			Description: "Reconcile the week's signals.", Source: "revassure",
			Body: "# Weekly check",
		}},
		Prompts: []contract.PromptArtifact{{
			ID: "revassure-analyst", Name: "RA Analyst Prompt",
			Description: "Analyst system prompt.", Source: "revassure",
			Tags: []string{"revassure"}, Body: promptBody,
		}},
		Workflows: []contract.WorkflowArtifact{{
			ID: "revassure-weekly-flow", Name: "Weekly Flow",
			Description: "Weekly reconciliation workflow.", Source: "revassure",
			Tags: []string{"revassure"}, Body: workflowBody,
		}},
		Dashboards: []contract.DashboardArtifact{{
			ID: "revassure-leakage", Name: "Leakage Dashboard",
			Description: "Weekly leakage view.", Source: "revassure",
			Tags: []string{"revassure"}, Body: dashboardBody,
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
		if len(sol.Prompts) != 1 || sol.Prompts[0].ID != "revassure-analyst" || sol.Prompts[0].Body != promptBody {
			t.Fatalf("prompt did not round-trip: %+v", sol.Prompts)
		}
		if len(sol.Workflows) != 1 || sol.Workflows[0].ID != "revassure-weekly-flow" || sol.Workflows[0].Body != workflowBody {
			t.Fatalf("workflow did not round-trip: %+v", sol.Workflows)
		}
		if len(sol.Dashboards) != 1 || sol.Dashboards[0].ID != "revassure-leakage" || sol.Dashboards[0].Body != dashboardBody {
			t.Fatalf("dashboard did not round-trip: %+v", sol.Dashboards)
		}
		// One skill + one each of the three new kinds = four leaves, one manifest.
		if len(sol.Skills) != 1 || len(sol.Manifest.Artifacts) != 4 {
			t.Fatalf("expected 1 skill + 4 artifact refs, got skills=%d refs=%d", len(sol.Skills), len(sol.Manifest.Artifacts))
		}
	case <-time.After(3 * time.Second):
		t.Fatal("did not observe announced solution within 3s")
	}
}

// TestDataArtifacts_RoundTrip proves the two data-shaped declarative wires
// (catalog, projection) announce as their own leaves and assemble back with
// their Body intact — alongside a skill, to show the tree carries them with the
// other control-plane kinds. Each Body is opaque YAML (a catalog schema / a
// DuckDB transform) the v4 side parses; the SDK only round-trips the bytes.
func TestDataArtifacts_RoundTrip(t *testing.T) {
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

	catalogBody := "catalog: leakage_findings\ndialect: duckdb\ngrounding: |\n  One row per reconciliation finding.\nlabels:\n  severity: status\n"
	projectionBody := "target_catalog: leakage_findings\nsource: raw_signals\nlabels:\n  severity: status\nsql: SELECT * FROM raw_signals WHERE mismatch\n"

	err = transport.PublishSolution(ctx, kv, transport.SolutionPublish{
		Name:        "revassure",
		DisplayName: "Revenue Assurance",
		Version:     "0.1.0",
		Skills: []contract.SkillArtifact{{
			ID: "revassure-weekly-check", Name: "Weekly Check",
			Description: "Reconcile the week's signals.", Source: "revassure",
			Body: "# Weekly check",
		}},
		Catalogs: []contract.CatalogArtifact{{
			ID: "revassure-leakage-catalog", Name: "Leakage Findings Catalog",
			Description: "Schema for reconciliation findings.", Source: "revassure",
			Tags: []string{"revassure"}, Body: catalogBody,
		}},
		Projections: []contract.ProjectionArtifact{{
			ID: "revassure-leakage-projection", Name: "Leakage Projection",
			Description: "Transform raw signals into findings.", Source: "revassure",
			Tags: []string{"revassure"}, Body: projectionBody,
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
		if len(sol.Catalogs) != 1 || sol.Catalogs[0].ID != "revassure-leakage-catalog" || sol.Catalogs[0].Body != catalogBody {
			t.Fatalf("catalog did not round-trip: %+v", sol.Catalogs)
		}
		if len(sol.Projections) != 1 || sol.Projections[0].ID != "revassure-leakage-projection" || sol.Projections[0].Body != projectionBody {
			t.Fatalf("projection did not round-trip: %+v", sol.Projections)
		}
		// One skill + one catalog + one projection = three leaves, one manifest.
		if len(sol.Skills) != 1 || len(sol.Manifest.Artifacts) != 3 {
			t.Fatalf("expected 1 skill + 3 artifact refs, got skills=%d refs=%d", len(sol.Skills), len(sol.Manifest.Artifacts))
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

// TestTenantArtifact_RoundTrip proves the lake-tenant declaration (S-1874)
// announces as its own typed leaf and assembles back field-for-field — the
// one artifact whose declaration is typed rather than an opaque Body, since
// the platform enforces its fields at materialization. Also proves the
// publish-side fail-fast: an invalid declaration refuses to publish at all.
func TestTenantArtifact_RoundTrip(t *testing.T) {
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

	tenant := contract.TenantArtifact{
		Name:   "salesdemo",
		Source: "salesdemo",
		Streams: []contract.StreamDecl{{
			Name: "sales_events",
			Columns: []contract.ColumnDecl{
				{Name: "event_time", Type: "TIMESTAMP", Role: contract.RoleTime},
				{Name: "workspace", Type: "VARCHAR"},
				{Name: "deal_id", Type: "VARCHAR"},
				{Name: "amount", Type: "DECIMAL(18,2)"},
				{Name: "src_slice", Type: "VARCHAR"},
			},
			Labels: []string{"workspace"},
		}},
		Projections: []contract.ProjectionDecl{
			{Name: "deals_latest", Stream: "sales_events", Kind: contract.ProjectionLatest,
				KeyColumns: []string{"deal_id"}, TimeColumn: "event_time"},
		},
		Ingests:   []contract.IngestDecl{{Stream: "sales_events", SourceKind: "test_local", SourcePattern: "demo/*.ndjson"}},
		Retention: contract.RetentionDecl{Class: contract.RetentionWindow, Days: 90},
		Binding:   contract.TenantBindingSolution,
	}

	err = transport.PublishSolution(ctx, kv, transport.SolutionPublish{
		Name:        "salesdemo",
		DisplayName: "Sales Demo",
		Version:     "0.1.0",
		Tenants:     []contract.TenantArtifact{tenant},
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
		if len(sol.Tenants) != 1 {
			t.Fatalf("expected 1 tenant, got %d", len(sol.Tenants))
		}
		got := sol.Tenants[0]
		if got.Name != "salesdemo" || len(got.Streams) != 1 || len(got.Streams[0].Columns) != 5 ||
			got.Streams[0].Columns[0].Role != contract.RoleTime ||
			len(got.Projections) != 1 || got.Projections[0].Kind != contract.ProjectionLatest ||
			len(got.Ingests) != 1 || got.Ingests[0].SourceKind != "test_local" ||
			got.Retention.Class != contract.RetentionWindow || got.Retention.Days != 90 ||
			got.Binding != contract.TenantBindingSolution {
			t.Fatalf("tenant did not round-trip: %+v", got)
		}
		if len(sol.Manifest.Artifacts) != 1 || sol.Manifest.Artifacts[0].Kind != contract.ArtifactTenant {
			t.Fatalf("expected one tenant artifact ref, got %+v", sol.Manifest.Artifacts)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("did not observe announced solution within 3s")
	}

	// Publish-side fail-fast: a reserved tenant name never reaches the wire.
	bad := tenant
	bad.Name = "audit"
	err = transport.PublishSolution(ctx, kv, transport.SolutionPublish{
		Name: "salesdemo", Tenants: []contract.TenantArtifact{bad},
	})
	if err == nil || !strings.Contains(err.Error(), "reserved") {
		t.Fatalf("expected reserved-name publish refusal, got: %v", err)
	}
}
