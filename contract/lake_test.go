package contract_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/riclib/solid-sdk/contract"
)

// validLake is the doubt-matrix-shaped fixture: a state stream with a
// latest projection (state changes), a copy + derive pair (aggregates that
// must never drift), an unscoped admin copy, bind-time view + seed table,
// FILE-door ingest, and the 90-day demo retention.
func validLake() contract.LakeArtifact {
	return contract.LakeArtifact{
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
		Binding:   contract.LakeBindingSolution,
	}
}

func TestLakeArtifact_ValidFixture(t *testing.T) {
	if err := validLake().Validate(); err != nil {
		t.Fatalf("valid fixture rejected: %v", err)
	}
}

func TestLakeArtifact_ValidateRejects(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(*contract.LakeArtifact)
		wantSub string
	}{
		{"empty name", func(a *contract.LakeArtifact) { a.Name = "" }, "no name"},
		{"uppercase name", func(a *contract.LakeArtifact) { a.Name = "SalesDemo" }, "lowercase"},
		{"reserved audit", func(a *contract.LakeArtifact) { a.Name = "audit" }, "reserved"},
		{"reserved conversations", func(a *contract.LakeArtifact) { a.Name = "conversations" }, "reserved"},
		{"reserved metrics", func(a *contract.LakeArtifact) { a.Name = "metrics" }, "reserved"},
		{"reserved solidmon tenant", func(a *contract.LakeArtifact) { a.Name = "adf_ops" }, "reserved"},
		{"reserved solid prefix", func(a *contract.LakeArtifact) { a.Name = "solidx" }, "reserved"},
		{"reserved duckdb schema", func(a *contract.LakeArtifact) { a.Name = "main" }, "reserved"},
		{"reserved _admin suffix", func(a *contract.LakeArtifact) { a.Name = "foo_admin" }, "_admin"},
		{"no streams", func(a *contract.LakeArtifact) { a.Streams = nil }, "at least one stream"},
		{"duplicate stream", func(a *contract.LakeArtifact) {
			a.Streams = append(a.Streams, a.Streams[0])
		}, "duplicate stream"},
		{"no time column", func(a *contract.LakeArtifact) {
			a.Streams[0].Columns[0].Role = ""
		}, "exactly one role"},
		{"two time columns", func(a *contract.LakeArtifact) {
			a.Streams[0].Columns[2].Role = contract.RoleTime
		}, "exactly one role"},
		{"reserved column gen", func(a *contract.LakeArtifact) {
			a.Streams[0].Columns = append(a.Streams[0].Columns, contract.ColumnDecl{Name: "gen", Type: "BIGINT"})
		}, "reserved by the lake"},
		{"reserved column payload", func(a *contract.LakeArtifact) {
			a.Streams[0].Columns = append(a.Streams[0].Columns, contract.ColumnDecl{Name: "payload", Type: "VARCHAR"})
		}, "reserved by the lake"},
		{"injection in type", func(a *contract.LakeArtifact) {
			a.Streams[0].Columns[3].Type = "VARCHAR; DROP TABLE x"
		}, "invalid type"},
		{"label not a column", func(a *contract.LakeArtifact) {
			a.Streams[0].Labels = append(a.Streams[0].Labels, "nope")
		}, "not a declared column"},
		{"duplicate column", func(a *contract.LakeArtifact) {
			a.Streams[0].Columns = append(a.Streams[0].Columns, contract.ColumnDecl{Name: "deal_id", Type: "VARCHAR"})
		}, "duplicate column"},
		{"projection unknown stream", func(a *contract.LakeArtifact) {
			a.Projections[0].Stream = "ghost"
		}, "undeclared stream"},
		{"duplicate projection", func(a *contract.LakeArtifact) {
			a.Projections = append(a.Projections, a.Projections[1])
		}, "duplicate projection"},
		{"scoped projection without workspace label", func(a *contract.LakeArtifact) {
			a.Streams[0].Labels = nil
		}, "does not declare the \"workspace\" label"},
		{"copy transform with WITH", func(a *contract.LakeArtifact) {
			a.Projections[1].TransformSQL = "WITH x AS (SELECT 1) SELECT * FROM x"
		}, "bare SELECT only"},
		{"copy transform DISTINCT", func(a *contract.LakeArtifact) {
			a.Projections[1].TransformSQL = "SELECT DISTINCT deal_id FROM {from}"
		}, "DISTINCT"},
		{"copy transform two statements", func(a *contract.LakeArtifact) {
			a.Projections[1].TransformSQL = "SELECT 1; SELECT 2"
		}, "multiple statements"},
		{"copy transform without from token", func(a *contract.LakeArtifact) {
			a.Projections[1].TransformSQL = "SELECT deal_id FROM sales_events"
		}, "{from}"},
		{"derive-only fields on a copy", func(a *contract.LakeArtifact) {
			a.Projections[1].TombstoneCondition = "status = 'gone'"
		}, "derive-only fields"},
		{"derive-only fields on a latest", func(a *contract.LakeArtifact) {
			a.Projections[0].BumpColumn = "b"
			a.Projections[0].BumpTimeColumn = "bt"
		}, "derive-only fields"},
		{"tombstone projection undeclared", func(a *contract.LakeArtifact) {
			a.Projections[2].TombstoneCondition = "total = 0"
			a.Projections[2].TombstoneProjections = []string{"ghost_projection"}
		}, "not declared in this artifact"},
		{"view collides with stream", func(a *contract.LakeArtifact) {
			a.Views[0].Name = "sales_events"
		}, "collides"},
		{"projection collides with another stream", func(a *contract.LakeArtifact) {
			a.Projections[1].Name = "audit_trail"
			a.Streams = append(a.Streams, contract.StreamDecl{
				Name: "audit_trail",
				Columns: []contract.ColumnDecl{
					{Name: "t", Type: "TIMESTAMP", Role: contract.RoleTime},
					{Name: "workspace", Type: "VARCHAR"},
				},
				Labels: []string{"workspace"},
			})
		}, "collides"},
		{"seal margin below -1", func(a *contract.LakeArtifact) {
			a.Ingests[0].SealMarginMinutes = -2
		}, "seal_margin_minutes"},
		{"latest without key", func(a *contract.LakeArtifact) {
			a.Projections[0].KeyColumns = nil
		}, "requires key_columns"},
		{"latest without time", func(a *contract.LakeArtifact) {
			a.Projections[0].TimeColumn = ""
		}, "requires time_column"},
		{"latest key not a stream column", func(a *contract.LakeArtifact) {
			a.Projections[0].KeyColumns = []string{"ghost"}
		}, "not a column of stream"},
		{"derive without derive_from", func(a *contract.LakeArtifact) {
			a.Projections[2].DeriveFrom = ""
		}, "requires derive_sql and derive_from"},
		{"derive_from not a copy", func(a *contract.LakeArtifact) {
			a.Projections[2].DeriveFrom = "deals_latest"
		}, "not a copy projection"},
		{"derive sql not a select", func(a *contract.LakeArtifact) {
			a.Projections[2].DeriveSQL = "DELETE FROM {from}"
		}, "must be a SELECT"},
		{"derive sql without from token", func(a *contract.LakeArtifact) {
			a.Projections[2].DeriveSQL = "SELECT deal_id FROM events_copy GROUP BY deal_id"
		}, "{from}"},
		{"bump pair split", func(a *contract.LakeArtifact) {
			a.Projections[2].BumpColumn = "bumped"
		}, "set together"},
		{"unknown projection kind", func(a *contract.LakeArtifact) {
			a.Projections[0].Kind = "mirror"
		}, "unknown kind"},
		{"view collides with projection", func(a *contract.LakeArtifact) {
			a.Views[0].Name = "deals_latest"
		}, "collides"},
		{"view sql dml", func(a *contract.LakeArtifact) {
			a.Views[0].SQL = "DROP TABLE deals_latest"
		}, "must be a SELECT"},
		{"ingest unknown stream", func(a *contract.LakeArtifact) {
			a.Ingests[0].Stream = "ghost"
		}, "undeclared stream"},
		{"ingest slice column missing", func(a *contract.LakeArtifact) {
			a.Ingests[0].SliceColumn = "missing_slice"
		}, "not a column of stream"},
		{"ingest both envelope forms", func(a *contract.LakeArtifact) {
			a.Ingests[0].Envelope = "columns: []"
			a.Ingests[0].EnvelopeRef = "databricks-audit"
		}, "both envelope and envelope_ref"},
		{"duplicate ingest source", func(a *contract.LakeArtifact) {
			a.Ingests = append(a.Ingests, a.Ingests[0])
		}, "duplicate ingest source"},
		{"retention missing", func(a *contract.LakeArtifact) {
			a.Retention = contract.RetentionDecl{}
		}, "retention is required"},
		{"retention window without days", func(a *contract.LakeArtifact) {
			a.Retention = contract.RetentionDecl{Class: contract.RetentionWindow}
		}, "days >= 1"},
		{"retention forever with days", func(a *contract.LakeArtifact) {
			a.Retention = contract.RetentionDecl{Class: contract.RetentionForever, Days: 30}
		}, "must not set days"},
		{"unknown retention class", func(a *contract.LakeArtifact) {
			a.Retention = contract.RetentionDecl{Class: "sometimes"}
		}, "unknown retention class"},
		{"missing binding", func(a *contract.LakeArtifact) { a.Binding = "" }, "binding must be"},
		{"unknown binding", func(a *contract.LakeArtifact) { a.Binding = "workspace" }, "binding must be"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := validLake()
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

// TestLakeArtifact_JSONStable pins the wire shape: marshal → unmarshal is
// lossless, and the JSON field names are the frozen additive-only contract.
func TestLakeArtifact_JSONStable(t *testing.T) {
	a := validLake()
	b, err := json.Marshal(a)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var back contract.LakeArtifact
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

// TestSealMarginDisabledIsLegal pins the three-valued contract: -1 (gate
// disabled — dev sources whose files never grow) is a LEGAL declaration.
func TestSealMarginDisabledIsLegal(t *testing.T) {
	a := validLake()
	a.Ingests[0].SealMarginMinutes = -1
	if err := a.Validate(); err != nil {
		t.Fatalf("seal_margin_minutes = -1 must be legal (gate disabled): %v", err)
	}
}

// TestUnknownFieldsIgnored pins forward compatibility: a NEWER producer's
// leaf carrying fields this consumer does not know must unmarshal cleanly
// with the known fields intact (the additive-only wire rule).
func TestUnknownFieldsIgnored(t *testing.T) {
	payload := `{
		"name": "futuredemo",
		"future_top_level": {"x": 1},
		"streams": [{
			"name": "events",
			"future_stream_field": true,
			"columns": [
				{"name": "event_time", "type": "TIMESTAMP", "role": "time", "future_col_field": 7},
				{"name": "workspace", "type": "VARCHAR"}
			],
			"labels": ["workspace"]
		}],
		"retention": {"class": "window", "days": 90, "future_retention_field": "x"},
		"binding": "solution"
	}`
	var a contract.LakeArtifact
	if err := json.Unmarshal([]byte(payload), &a); err != nil {
		t.Fatalf("unknown fields must not break decode: %v", err)
	}
	if a.Name != "futuredemo" || len(a.Streams) != 1 || len(a.Streams[0].Columns) != 2 ||
		a.Retention.Days != 90 {
		t.Fatalf("known fields lost around unknown ones: %+v", a)
	}
	if err := a.Validate(); err != nil {
		t.Fatalf("decoded artifact must validate: %v", err)
	}
}
