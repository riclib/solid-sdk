package contract_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/riclib/solid-sdk/contract"
)

// lake_stream_door_test.go — the STREAM door (0.12.0, S-2241). Shares
// validLake() with lake_test.go.

// streamDoorLake is the same fixture with its ingest flipped to the JetStream
// door: a subject inside the tenant's namespace, a declared message identity,
// and a window sized to the source's own re-poll distance.
func streamDoorLake() contract.LakeArtifact {
	a := validLake()
	a.Ingests = []contract.IngestDecl{{
		Stream:             "sales_events",
		Door:               contract.DoorStream,
		Subject:            "lake.salesdemo.sales_events",
		MsgID:              "deal_id:event_time",
		DedupWindowSeconds: 3600,
	}}
	return a
}

func TestStreamDoor_ValidFixture(t *testing.T) {
	if err := streamDoorLake().Validate(); err != nil {
		t.Fatalf("valid stream-door fixture rejected: %v", err)
	}
}

// TestStreamDoor_DoorKindDefaultsToFile pins the frozen pre-0.12.0 meaning of
// the zero value: an ingest that declares no door IS a file ingest.
func TestStreamDoor_DoorKindDefaultsToFile(t *testing.T) {
	if got := (contract.IngestDecl{}).DoorKind(); got != contract.DoorFile {
		t.Fatalf("empty door resolved to %q, want %q", got, contract.DoorFile)
	}
	if got := (contract.IngestDecl{Door: contract.DoorStream}).DoorKind(); got != contract.DoorStream {
		t.Fatalf("declared door resolved to %q", got)
	}
}

func TestStreamDoor_DedupWindowDefault(t *testing.T) {
	if got := (contract.IngestDecl{}).DedupWindow(); got != contract.DefaultDedupWindowSeconds {
		t.Fatalf("undeclared window = %d, want %d", got, contract.DefaultDedupWindowSeconds)
	}
	if got := (contract.IngestDecl{DedupWindowSeconds: 7}).DedupWindow(); got != 7 {
		t.Fatalf("declared window = %d, want 7", got)
	}
}

func TestStreamSubjectPrefix(t *testing.T) {
	if got := contract.StreamSubjectPrefix("salesdemo"); got != "lake.salesdemo." {
		t.Fatalf("prefix = %q", got)
	}
}

// TestStreamDoor_HalfDeclaredIngestsAreRefused is the XOR: a field belonging to
// the OTHER door is refused, never ignored. An ingest carrying both a
// source_pattern and a subject has not chosen a door, and the platform must not
// pick one for it — whichever it picked, the author would have half a working
// ingest and no error to read.
func TestStreamDoor_HalfDeclaredIngestsAreRefused(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(*contract.IngestDecl)
		wantSub string
	}{
		{"file ingest with a subject", func(i *contract.IngestDecl) {
			*i = contract.IngestDecl{Stream: "sales_events", SourceKind: "test_local", SourcePattern: "*.ndjson",
				Subject: "lake.salesdemo.sales_events"}
		}, "stream-door fields"},
		{"file ingest with a msg_id", func(i *contract.IngestDecl) {
			*i = contract.IngestDecl{Stream: "sales_events", SourcePattern: "*.ndjson", MsgID: "sys_id"}
		}, "stream-door fields"},
		{"file ingest with a dedup window", func(i *contract.IngestDecl) {
			*i = contract.IngestDecl{Stream: "sales_events", SourcePattern: "*.ndjson", DedupWindowSeconds: 30}
		}, "stream-door fields"},
		{"stream ingest with a source pattern", func(i *contract.IngestDecl) {
			i.SourcePattern = "*.ndjson"
		}, "file-door fields"},
		{"stream ingest with a source kind", func(i *contract.IngestDecl) {
			i.SourceKind = "test_local"
		}, "file-door fields"},
		{"stream ingest with a seal margin", func(i *contract.IngestDecl) {
			i.SealMarginMinutes = -1
		}, "file-door fields"},
		{"stream ingest with a schedule", func(i *contract.IngestDecl) {
			i.Schedule = "0 * * * *"
		}, "file-door fields"},
		{"stream ingest with an envelope", func(i *contract.IngestDecl) {
			i.Envelope = "fields: []"
		}, "envelope on a STREAM door"},
		{"stream ingest with an envelope_ref", func(i *contract.IngestDecl) {
			i.EnvelopeRef = "servicenow"
		}, "envelope on a STREAM door"},
		{"stream ingest with no msg_id", func(i *contract.IngestDecl) {
			i.MsgID = ""
		}, "must declare msg_id"},
		{"stream ingest with no subject", func(i *contract.IngestDecl) {
			i.Subject = ""
		}, "must declare a subject"},
		{"subject outside the namespace", func(i *contract.IngestDecl) {
			i.Subject = "stream.bits"
		}, "outside this lake's namespace"},
		{"subject in another tenant's namespace", func(i *contract.IngestDecl) {
			i.Subject = "lake.conversations.bits"
		}, "outside this lake's namespace"},
		{"subject is the bare prefix", func(i *contract.IngestDecl) {
			i.Subject = "lake.salesdemo."
		}, "bare namespace prefix"},
		{"subject with a > wildcard", func(i *contract.IngestDecl) {
			i.Subject = "lake.salesdemo.>"
		}, "wildcard"},
		{"subject with a * wildcard", func(i *contract.IngestDecl) {
			i.Subject = "lake.salesdemo.*"
		}, "wildcard"},
		{"negative dedup window", func(i *contract.IngestDecl) {
			i.DedupWindowSeconds = -1
		}, "dedup_window_seconds"},
		{"unknown door", func(i *contract.IngestDecl) {
			i.Door = "kafka"
		}, "unknown door"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := streamDoorLake()
			tc.mutate(&a.Ingests[0])
			err := a.Validate()
			if err == nil {
				t.Fatalf("expected refusal for %s", tc.name)
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Fatalf("error %q does not mention %q", err, tc.wantSub)
			}
		})
	}
}

// TestStreamDoor_DuplicateSubjectRefused: two doors on one subject would bind
// two durable consumers to the same messages and land every record twice —
// under two sources, so the lake cannot even see it as duplication.
func TestStreamDoor_DuplicateSubjectRefused(t *testing.T) {
	a := streamDoorLake()
	second := a.Ingests[0]
	second.Source = "sales_events_alt" // a distinct SOURCE, so only the subject collides
	a.Ingests = append(a.Ingests, second)
	err := a.Validate()
	if err == nil || !strings.Contains(err.Error(), "duplicate ingest subject") {
		t.Fatalf("two stream doors on one subject must be refused, got %v", err)
	}
}

// TestStreamDoor_MixedDoorsValidate: a lake may feed one stream from files and
// another from the wire — the doors are exclusive per ENTRY, not per lake.
func TestStreamDoor_MixedDoorsValidate(t *testing.T) {
	a := streamDoorLake()
	a.Streams = append(a.Streams, contract.StreamDecl{
		Name: "reference_prices",
		Columns: []contract.ColumnDecl{
			{Name: "as_of", Type: "TIMESTAMP", Role: contract.RoleTime},
			{Name: "workspace", Type: "VARCHAR"},
			{Name: "sku", Type: "VARCHAR"},
			{Name: "src_slice", Type: "VARCHAR"},
		},
		Labels: []string{"workspace"},
	})
	a.Ingests = append(a.Ingests, contract.IngestDecl{
		Stream: "reference_prices", SourceKind: "test_local", SourcePattern: "prices/*.ndjson",
	})
	if err := a.Validate(); err != nil {
		t.Fatalf("a lake with one door of each kind must validate: %v", err)
	}
}

// TestStreamDoor_JSONStable pins the new wire field names, and pins that an
// artifact declaring none of them does not grow them (the additive rule).
func TestStreamDoor_JSONStable(t *testing.T) {
	b, err := json.Marshal(streamDoorLake())
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, field := range []string{`"door"`, `"subject"`, `"msg_id"`, `"dedup_window_seconds"`} {
		if !strings.Contains(string(b), field) {
			t.Fatalf("wire JSON missing field %s:\n%s", field, b)
		}
	}
	old, err := json.Marshal(validLake())
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, field := range []string{`"door"`, `"subject"`, `"msg_id"`, `"dedup_window_seconds"`} {
		if strings.Contains(string(old), field) {
			t.Fatalf("a file-door artifact must not carry %s:\n%s", field, old)
		}
	}
}

// TestStreamDoor_SatisfiesTheWriteDoorRequirement: a lake whose ONLY door is a
// stream door still validates — the "at least one ingest" rule was never about
// files. A lake with no door at all is still refused.
func TestStreamDoor_SatisfiesTheWriteDoorRequirement(t *testing.T) {
	a := streamDoorLake()
	if len(a.Ingests) != 1 || a.Ingests[0].DoorKind() != contract.DoorStream {
		t.Fatalf("fixture drifted")
	}
	if err := a.Validate(); err != nil {
		t.Fatalf("a stream-only lake must validate: %v", err)
	}
	a.Ingests = nil
	err := a.Validate()
	if err == nil || !strings.Contains(err.Error(), "at least one ingest") {
		t.Fatalf("a lake with no door at all must still be refused, got %v", err)
	}
}
