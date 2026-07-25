package contract_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/riclib/solid-sdk/contract"
)

// TestScreenDescriptor_JSONShape pins the wire field names. Both sides marshal
// these bytes; a rename here silently unmounts every declared tab.
func TestScreenDescriptor_JSONShape(t *testing.T) {
	raw, err := json.Marshal(contract.ScreenDescriptor{
		Point:      contract.ScreenPointWorkspaceSettings,
		Name:       "advisory-scope",
		Title:      "Advisory Scope",
		ObjectKind: "advisory_scope",
		Icon:       "filter",
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	want := `{"point":"workspace.settings","name":"advisory-scope","title":"Advisory Scope","object_kind":"advisory_scope","icon":"filter"}`
	if string(raw) != want {
		t.Fatalf("json = %s\nwant     %s", raw, want)
	}

	// Point and ObjectKind are NOT omitempty: their absence must be visible on
	// the wire (an empty point is the platform's drop signal, not a default).
	bare, err := json.Marshal(contract.ScreenDescriptor{Name: "x"})
	if err != nil {
		t.Fatalf("marshal bare: %v", err)
	}
	if got := string(bare); got != `{"point":"","name":"x","object_kind":""}` {
		t.Fatalf("bare json = %s", got)
	}
}

// TestManifest_ScreensRoundTrip proves Screens rides the manifest index (beside
// Fires) and survives a round trip — and that a manifest WITHOUT screens is
// unchanged, the additive-only contract.
func TestManifest_ScreensRoundTrip(t *testing.T) {
	m := contract.SolutionManifest{
		Name:        "cdl-advisory",
		DisplayName: "CDL Incident Advisory",
		Revision:    3,
		Screens: []contract.ScreenDescriptor{{
			Point:      contract.ScreenPointWorkspaceSettings,
			Name:       "advisory-scope",
			Title:      "Advisory Scope",
			ObjectKind: "advisory_scope",
		}},
	}
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(raw), `"screens":[`) {
		t.Fatalf("manifest json has no screens block: %s", raw)
	}

	var back contract.SolutionManifest
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(back.Screens) != 1 {
		t.Fatalf("screens = %d, want 1", len(back.Screens))
	}
	got := back.Screens[0]
	if got.Point != contract.ScreenPointWorkspaceSettings || got.Name != "advisory-scope" ||
		got.ObjectKind != "advisory_scope" || got.Label() != "Advisory Scope" {
		t.Fatalf("screen = %+v", got)
	}

	// Additive-only: no screens = no key, and an old manifest still parses.
	plain, err := json.Marshal(contract.SolutionManifest{Name: "old", Revision: 1})
	if err != nil {
		t.Fatalf("marshal plain: %v", err)
	}
	if strings.Contains(string(plain), "screens") {
		t.Fatalf("a screenless manifest must not emit the key: %s", plain)
	}
	var old contract.SolutionManifest
	if err := json.Unmarshal([]byte(`{"name":"old","revision":1}`), &old); err != nil {
		t.Fatalf("unmarshal legacy manifest: %v", err)
	}
	if old.Screens != nil {
		t.Fatalf("legacy manifest gained screens: %v", old.Screens)
	}
}

// TestScreenEnvelope_JSONShape pins the envelope field names — request, context
// and reply. The platform builds these bytes; the SDK adapter reads them.
func TestScreenEnvelope_JSONShape(t *testing.T) {
	raw, err := json.Marshal(contract.ScreenRequest{
		Verb:   contract.ScreenVerbSubmit,
		Method: "POST",
		Path:   "/",
		Form:   map[string][]string{"include": {"alteryx"}},
		Context: contract.ScreenContext{
			Solution: "cdl-advisory", Screen: "advisory-scope",
			Point: contract.ScreenPointWorkspaceSettings, Workspace: "cdl-ops",
			UserRole: "admin", ObjectID: "cdl-ops", ObjectVersion: "abc123",
			Object: map[string]any{"include": []any{"alteryx"}},
		},
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, key := range []string{
		`"verb":"submit"`, `"method":"POST"`, `"path":"/"`, `"form":{"include":["alteryx"]}`,
		`"solution":"cdl-advisory"`, `"screen":"advisory-scope"`, `"point":"workspace.settings"`,
		`"workspace":"cdl-ops"`, `"user_role":"admin"`, `"object_id":"cdl-ops"`,
		`"object_version":"abc123"`, `"object":{"include":["alteryx"]}`,
	} {
		if !strings.Contains(string(raw), key) {
			t.Errorf("envelope json missing %s\ngot %s", key, raw)
		}
	}

	reply, err := json.Marshal(contract.ScreenReply{Status: 200, HTML: "<p>hi</p>"})
	if err != nil {
		t.Fatalf("marshal reply: %v", err)
	}
	// encoding/json HTML-escapes <, > and & by default; the platform decodes it
	// back to the verbatim fragment, so the escaping is transparent — pinned here
	// so nobody "fixes" it with a custom encoder and changes the bytes.
	if got := string(reply); got != `{"status":200,"html":"\u003cp\u003ehi\u003c/p\u003e"}` {
		t.Fatalf("reply json = %s", got)
	}
	var backReply contract.ScreenReply
	if err := json.Unmarshal(reply, &backReply); err != nil {
		t.Fatalf("unmarshal reply: %v", err)
	}
	if backReply.HTML != "<p>hi</p>" {
		t.Fatalf("fragment did not survive the round trip: %q", backReply.HTML)
	}
}

// TestScreenVerbs — the closed pair. A third verb is a different transport, not
// another constant.
func TestScreenVerbs(t *testing.T) {
	if contract.ScreenVerbRender != "render" || contract.ScreenVerbSubmit != "submit" {
		t.Fatalf("verbs drifted: %q / %q", contract.ScreenVerbRender, contract.ScreenVerbSubmit)
	}
	if pts := contract.ScreenPoints(); len(pts) != 1 || pts[0] != contract.ScreenPointWorkspaceSettings {
		t.Fatalf("ScreenPoints = %v, want only the workspace.settings point", pts)
	}
}
