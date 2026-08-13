package contract_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"

	"github.com/riclib/solid-sdk/contract"
)

// mechanicBody is a deliberately HOSTILE def body: comments, a trailing-space
// line, a tab inside a block scalar, CRLF, a blank line, non-ASCII text,
// template tokens, quoted SQL with punctuation, and NO trailing newline. Every
// one of those is something a parse-and-re-emit round trip would silently
// rewrite — which is the point: the content hash is the def's version
// identity, so the SDK must return these bytes exactly as it received them.
const mechanicBody = "# cdl-advisory.incident — the mechanic def\r\n" +
	"id: cdl-advisory.incident\n" +
	"kind: mechanic   \n" + // trailing spaces, deliberate
	"\n" +
	"queries:\n" +
	"  incident_closed:\n" +
	"    description: |\n" +
	"      Has it closed with a note?\n" +
	"\tIndented with a tab, deliberately.\n" + // tab inside a block scalar
	"    sql: SELECT 1 FROM {{.ReadSchemaLatest}}.{{.Table}} i WHERE i.state IN ('800', '900')\n" +
	"steps:\n" +
	"  - id: precedents          # trailing comment, café ☕\n" +
	"    retrieve:\n" +
	"      text: subject.short_description\n" +
	"      arms: [fts, vector]\n" +
	"  - id: advise\n" +
	"    function: generate_resolution"

func mechanicArtifact() contract.WorkflowArtifact {
	return contract.WorkflowArtifact{
		ID:          "cdl-advisory.incident",
		Name:        "CDL Advisory — Incident",
		Description: "Opens a case per landed incident and advises on it.",
		Source:      "cdl-advisory",
		Tags:        []string{"cdl"},
		Kind:        contract.WorkflowKindMechanic,
		Body:        mechanicBody,
	}
}

// TestWorkflowArtifact_BodyIsCarriedVerbatim is the load-bearing test of this
// wire: a round trip through the SDK's announce encoding returns the body BYTE
// FOR BYTE, so its content hash — the def's version identity — is unchanged.
// If this ever fails, re-announcing an unedited def mints a phantom new
// version.
func TestWorkflowArtifact_BodyIsCarriedVerbatim(t *testing.T) {
	orig := mechanicArtifact()
	wantHash := orig.DefVersion()

	b, err := json.Marshal(orig)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var back contract.WorkflowArtifact
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if back.Body != mechanicBody {
		t.Fatalf("body was altered on the wire:\nwant %q\ngot  %q", mechanicBody, back.Body)
	}
	if got := back.DefVersion(); got != wantHash {
		t.Fatalf("def version moved across the wire: want %s, got %s", wantHash, got)
	}
	// The specific mutations a YAML re-marshal would make, pinned one by one
	// so a failure says WHICH property was lost.
	for _, probe := range []struct{ name, substr string }{
		{"comment kept", "# cdl-advisory.incident"},
		{"inline comment kept", "# trailing comment, café ☕"},
		{"CRLF kept", "def\r\n"},
		{"trailing spaces kept", "kind: mechanic   \n"},
		{"blank line kept", "\n\nqueries:"},
		{"tab indent kept", "\n\tIndented with a tab"},
		{"template token kept", "{{.ReadSchemaLatest}}"},
	} {
		if !strings.Contains(back.Body, probe.substr) {
			t.Errorf("%s: %q missing from the round-tripped body", probe.name, probe.substr)
		}
	}
	if strings.HasSuffix(back.Body, "\n") {
		t.Errorf("a trailing newline appeared: the SDK re-emitted the body")
	}
}

// TestWorkflowArtifact_DefVersionIsContentIdentity pins the identity semantics:
// same bytes → same hash regardless of the surrounding metadata; one changed
// character → a different hash.
func TestWorkflowArtifact_DefVersionIsContentIdentity(t *testing.T) {
	a := mechanicArtifact()

	b := a
	b.Name = "renamed"
	b.Description = "reworded"
	b.Tags = []string{"other"}
	if a.DefVersion() != b.DefVersion() {
		t.Fatalf("metadata changed the def version — the hash covers the body only")
	}

	c := a
	c.Body = a.Body + " " // one character
	if a.DefVersion() == c.DefVersion() {
		t.Fatalf("a one-character edit did not change the def version")
	}

	// The exact algorithm the platform stamps as `def_version`: the first 12
	// hex characters of SHA-256 over the raw body, no normalization. Pinned
	// literally — if this drifts, an announced def and the same bytes seeded
	// in-tree would carry two different versions of one file.
	sum := sha256.Sum256([]byte(mechanicBody))
	want := hex.EncodeToString(sum[:])[:12]
	if a.DefVersion() != want {
		t.Fatalf("def version = %q, want %q (first 12 hex of sha256 over the raw body)", a.DefVersion(), want)
	}
	if len(a.DefVersion()) != 12 {
		t.Fatalf("def version is %d chars, want 12: %s", len(a.DefVersion()), a.DefVersion())
	}
}

// TestWorkflowArtifact_EmptyKindMeansSkill pins the frozen zero value: every
// producer that announced before Kind existed shipped a pure-skill def, and a
// decode of that older leaf must still resolve to exactly that.
func TestWorkflowArtifact_EmptyKindMeansSkill(t *testing.T) {
	legacy := `{"id":"salesintegrity-pursuit","name":"Pursuit","description":"d","body":"id: x\nskill: y\n"}`
	var wf contract.WorkflowArtifact
	if err := json.Unmarshal([]byte(legacy), &wf); err != nil {
		t.Fatalf("legacy leaf must decode: %v", err)
	}
	if wf.Kind != "" {
		t.Fatalf("legacy leaf gained a kind: %q", wf.Kind)
	}
	if wf.EffectiveKind() != contract.WorkflowKindSkill {
		t.Fatalf("empty kind must resolve to %q, got %q", contract.WorkflowKindSkill, wf.EffectiveKind())
	}
	if err := wf.Validate(); err != nil {
		t.Fatalf("legacy leaf must validate: %v", err)
	}
}

// TestWorkflowArtifact_JSONStable pins the wire shape: lossless round trip,
// the frozen field names present, and `kind` omitted when empty (so a
// pure-skill leaf is byte-identical to what pre-0.11.0 producers wrote).
func TestWorkflowArtifact_JSONStable(t *testing.T) {
	a := mechanicArtifact()
	b, err := json.Marshal(a)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var back contract.WorkflowArtifact
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
	for _, field := range []string{`"id"`, `"name"`, `"description"`, `"source"`, `"tags"`, `"kind"`, `"body"`} {
		if !strings.Contains(string(b), field) {
			t.Fatalf("wire JSON missing field %s:\n%s", field, b)
		}
	}

	skill := contract.WorkflowArtifact{ID: "w", Name: "W", Description: "d", Body: "id: w\n"}
	sb, err := json.Marshal(skill)
	if err != nil {
		t.Fatalf("marshal skill: %v", err)
	}
	if strings.Contains(string(sb), `"kind"`) {
		t.Fatalf("empty kind must be omitted from the wire: %s", sb)
	}
}

func TestWorkflowArtifact_ValidateRejects(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(*contract.WorkflowArtifact)
		wantSub string
	}{
		{"no id", func(w *contract.WorkflowArtifact) { w.ID = "" }, "no id"},
		{"unknown kind", func(w *contract.WorkflowArtifact) { w.Kind = "pipeline" }, "unknown kind"},
		{"empty body", func(w *contract.WorkflowArtifact) { w.Body = "" }, "body is empty"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := mechanicArtifact()
			tc.mutate(&w)
			err := w.Validate()
			if err == nil {
				t.Fatalf("expected rejection, got nil")
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Fatalf("error %q does not mention %q", err, tc.wantSub)
			}
		})
	}
}

func TestWorkflowArtifact_ValidateAcceptsBothKinds(t *testing.T) {
	for _, k := range []contract.WorkflowKind{"", contract.WorkflowKindSkill, contract.WorkflowKindMechanic} {
		w := mechanicArtifact()
		w.Kind = k
		if err := w.Validate(); err != nil {
			t.Fatalf("kind %q rejected: %v", k, err)
		}
	}
}
