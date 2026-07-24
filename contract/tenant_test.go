package contract_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/riclib/solid-sdk/contract"
)

// validTenant is the doubt-matrix-shaped fixture: a state stream with a
// latest projection (state changes), a copy + derive pair (aggregates that
// must never drift), an unscoped admin copy, bind-time view + seed table,
// FILE-door ingest, and the 90-day demo retention.
func validTenant() contract.TenantArtifact {
	return contract.TenantArtifact{
		Name:        "salesdemo",
		Description: "sales-integrity demo tenant",
		Source:      "salesdemo",
		Streams: []contract.StreamDecl{{
			Name: "sales_events",
			Columns: []contract.ColumnDecl{
				{Name: "event_time", Type: "TIMESTAMP", Role: contract.RoleTime},
				{Name: "workspace", Type: "VARCHAR"},
				{Name: "deal_id", Type: "VARCHAR"},
				{Name: "status", Type: "VARCHAR"},
				{Name: "amount", Type: "DECIMAL(18,2)"},
				{Name: "src_slice", Type: "VARCHAR"},
			},
			Labels:   []string{"workspace"},
			Residual: true,
		}},
		Projections: []contract.ProjectionDecl{
			{Name: "deals_latest", Stream: "sales_events", Kind: contract.ProjectionLatest,
				KeyColumns: []string{"deal_id"}, TimeColumn: "event_time"},
			{Name: "events_copy", Stream: "sales_events", Kind: contract.ProjectionCopy},
			{Name: "deal_totals", Stream: "sales_events", Kind: contract.ProjectionDerive,
				DeriveFrom: "events_copy",
				DeriveSQL:  "SELECT deal_id, SUM(amount) AS total FROM {from} GROUP BY deal_id",
				KeyColumns: []string{"deal_id"}},
			{Name: "admin_events", Stream: "sales_events", Kind: contract.ProjectionCopy, Unscoped: true},
		},
		Views: []contract.ViewDecl{
			{Name: "open_deals", SQL: "SELECT * FROM deals_latest WHERE status = 'open'"},
			{Name: "thresholds", Kind: contract.ViewKindSeed,
				SQL: "SELECT * FROM (VALUES (1, 100)) t(rule_id, threshold)"},
		},
		Ingests: []contract.IngestDecl{{
			Stream: "sales_events", SourceKind: "test_local", SourcePattern: "demo/*.ndjson",
		}},
		Retention: contract.RetentionDecl{Class: contract.RetentionWindow, Days: 90},
		Binding:   contract.TenantBindingSolution,
	}
}

func TestTenantArtifact_ValidFixture(t *testing.T) {
	if err := validTenant().Validate(); err != nil {
		t.Fatalf("valid fixture rejected: %v", err)
	}
}

func TestTenantArtifact_ValidateRejects(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(*contract.TenantArtifact)
		wantSub string
	}{
		{"empty name", func(a *contract.TenantArtifact) { a.Name = "" }, "no name"},
		{"uppercase name", func(a *contract.TenantArtifact) { a.Name = "SalesDemo" }, "lowercase"},
		{"reserved audit", func(a *contract.TenantArtifact) { a.Name = "audit" }, "reserved"},
		{"reserved conversations", func(a *contract.TenantArtifact) { a.Name = "conversations" }, "reserved"},
		{"reserved metrics", func(a *contract.TenantArtifact) { a.Name = "metrics" }, "reserved"},
		{"reserved solidmon tenant", func(a *contract.TenantArtifact) { a.Name = "adf_ops" }, "reserved"},
		{"reserved solid prefix", func(a *contract.TenantArtifact) { a.Name = "solidx" }, "reserved"},
		{"reserved duckdb schema", func(a *contract.TenantArtifact) { a.Name = "main" }, "reserved"},
		{"no streams", func(a *contract.TenantArtifact) { a.Streams = nil }, "at least one stream"},
		{"duplicate stream", func(a *contract.TenantArtifact) {
			a.Streams = append(a.Streams, a.Streams[0])
		}, "duplicate stream"},
		{"no time column", func(a *contract.TenantArtifact) {
			a.Streams[0].Columns[0].Role = ""
		}, "exactly one role"},
		{"two time columns", func(a *contract.TenantArtifact) {
			a.Streams[0].Columns[2].Role = contract.RoleTime
		}, "exactly one role"},
		{"reserved column gen", func(a *contract.TenantArtifact) {
			a.Streams[0].Columns = append(a.Streams[0].Columns, contract.ColumnDecl{Name: "gen", Type: "BIGINT"})
		}, "reserved by the lake"},
		{"reserved column payload", func(a *contract.TenantArtifact) {
			a.Streams[0].Columns = append(a.Streams[0].Columns, contract.ColumnDecl{Name: "payload", Type: "VARCHAR"})
		}, "reserved by the lake"},
		{"injection in type", func(a *contract.TenantArtifact) {
			a.Streams[0].Columns[3].Type = "VARCHAR; DROP TABLE x"
		}, "invalid type"},
		{"label not a column", func(a *contract.TenantArtifact) {
			a.Streams[0].Labels = append(a.Streams[0].Labels, "nope")
		}, "not a declared column"},
		{"duplicate column", func(a *contract.TenantArtifact) {
			a.Streams[0].Columns = append(a.Streams[0].Columns, contract.ColumnDecl{Name: "deal_id", Type: "VARCHAR"})
		}, "duplicate column"},
		{"projection unknown stream", func(a *contract.TenantArtifact) {
			a.Projections[0].Stream = "ghost"
		}, "undeclared stream"},
		{"duplicate projection", func(a *contract.TenantArtifact) {
			a.Projections = append(a.Projections, a.Projections[1])
		}, "duplicate projection"},
		{"scoped projection without workspace label", func(a *contract.TenantArtifact) {
			a.Streams[0].Labels = nil
		}, "does not declare the \"workspace\" label"},
		{"copy transform with WITH", func(a *contract.TenantArtifact) {
			a.Projections[1].TransformSQL = "WITH x AS (SELECT 1) SELECT * FROM x"
		}, "bare SELECT only"},
		{"copy transform DISTINCT", func(a *contract.TenantArtifact) {
			a.Projections[1].TransformSQL = "SELECT DISTINCT deal_id FROM {from}"
		}, "DISTINCT"},
		{"copy transform two statements", func(a *contract.TenantArtifact) {
			a.Projections[1].TransformSQL = "SELECT 1; SELECT 2"
		}, "multiple statements"},
		{"latest without key", func(a *contract.TenantArtifact) {
			a.Projections[0].KeyColumns = nil
		}, "requires key_columns"},
		{"latest without time", func(a *contract.TenantArtifact) {
			a.Projections[0].TimeColumn = ""
		}, "requires time_column"},
		{"latest key not a stream column", func(a *contract.TenantArtifact) {
			a.Projections[0].KeyColumns = []string{"ghost"}
		}, "not a column of stream"},
		{"derive without derive_from", func(a *contract.TenantArtifact) {
			a.Projections[2].DeriveFrom = ""
		}, "requires derive_sql and derive_from"},
		{"derive_from not a copy", func(a *contract.TenantArtifact) {
			a.Projections[2].DeriveFrom = "deals_latest"
		}, "not a copy projection"},
		{"derive sql not a select", func(a *contract.TenantArtifact) {
			a.Projections[2].DeriveSQL = "DELETE FROM {from}"
		}, "must be a SELECT"},
		{"derive sql without from token", func(a *contract.TenantArtifact) {
			a.Projections[2].DeriveSQL = "SELECT deal_id FROM events_copy GROUP BY deal_id"
		}, "{from}"},
		{"bump pair split", func(a *contract.TenantArtifact) {
			a.Projections[2].BumpColumn = "bumped"
		}, "set together"},
		{"unknown projection kind", func(a *contract.TenantArtifact) {
			a.Projections[0].Kind = "mirror"
		}, "unknown kind"},
		{"view collides with projection", func(a *contract.TenantArtifact) {
			a.Views[0].Name = "deals_latest"
		}, "collides"},
		{"view sql dml", func(a *contract.TenantArtifact) {
			a.Views[0].SQL = "DROP TABLE deals_latest"
		}, "must be a SELECT"},
		{"ingest unknown stream", func(a *contract.TenantArtifact) {
			a.Ingests[0].Stream = "ghost"
		}, "undeclared stream"},
		{"ingest slice column missing", func(a *contract.TenantArtifact) {
			a.Ingests[0].SliceColumn = "missing_slice"
		}, "not a column of stream"},
		{"ingest both envelope forms", func(a *contract.TenantArtifact) {
			a.Ingests[0].Envelope = "columns: []"
			a.Ingests[0].EnvelopeRef = "databricks-audit"
		}, "both envelope and envelope_ref"},
		{"duplicate ingest source", func(a *contract.TenantArtifact) {
			a.Ingests = append(a.Ingests, a.Ingests[0])
		}, "duplicate ingest source"},
		{"retention missing", func(a *contract.TenantArtifact) {
			a.Retention = contract.RetentionDecl{}
		}, "retention is required"},
		{"retention window without days", func(a *contract.TenantArtifact) {
			a.Retention = contract.RetentionDecl{Class: contract.RetentionWindow}
		}, "days >= 1"},
		{"retention forever with days", func(a *contract.TenantArtifact) {
			a.Retention = contract.RetentionDecl{Class: contract.RetentionForever, Days: 30}
		}, "must not set days"},
		{"unknown retention class", func(a *contract.TenantArtifact) {
			a.Retention = contract.RetentionDecl{Class: "sometimes"}
		}, "unknown retention class"},
		{"missing binding", func(a *contract.TenantArtifact) { a.Binding = "" }, "binding must be"},
		{"unknown binding", func(a *contract.TenantArtifact) { a.Binding = "workspace" }, "binding must be"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := validTenant()
			tc.mutate(&a)
			err := a.Validate()
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.wantSub)
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Fatalf("expected error containing %q, got: %v", tc.wantSub, err)
			}
		})
	}
}

// TestTenantArtifact_JSONStable pins the wire shape: marshal → unmarshal is
// lossless, and the JSON field names are the frozen additive-only contract.
func TestTenantArtifact_JSONStable(t *testing.T) {
	a := validTenant()
	b, err := json.Marshal(a)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var back contract.TenantArtifact
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	b2, err := json.Marshal(back)
	if err != nil {
		t.Fatalf("re-marshal: %v", err)
	}
	if string(b) != string(b2) {
		t.Fatalf("round-trip not lossless:\n%s\nvs\n%s", b, b2)
	}
	for _, field := range []string{`"name"`, `"streams"`, `"projections"`, `"views"`, `"ingests"`, `"retention"`, `"binding"`, `"key_columns"`, `"time_column"`, `"derive_sql"`, `"derive_from"`, `"unscoped"`, `"class"`, `"days"`} {
		if !strings.Contains(string(b), field) {
			t.Fatalf("wire JSON missing field %s:\n%s", field, b)
		}
	}
}
