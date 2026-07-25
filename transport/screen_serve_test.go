package transport_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/riclib/solid-sdk/contract"
	"github.com/riclib/solid-sdk/transport"
)

// screenRequest is the PLATFORM half of the exchange, hand-rolled here on
// purpose: the client (Render/Submit) is platform-side and deliberately not in
// this SDK, so the test speaks the wire BYTES a platform would put on the
// subject. That makes it a contract test, not just an adapter test.
func screenRequest(t *testing.T, nc *nats.Conn, subject string, req contract.ScreenRequest) contract.ScreenReply {
	t.Helper()
	payload, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal envelope: %v", err)
	}
	msg, err := nc.Request(subject, payload, 3*time.Second)
	if err != nil {
		t.Fatalf("request %s: %v", subject, err)
	}
	var reply contract.ScreenReply
	if err := json.Unmarshal(msg.Data, &reply); err != nil {
		t.Fatalf("unmarshal reply: %v", err)
	}
	return reply
}

// TestScreen_SubjectShape pins the wire contract's addressing:
// `ui.<solution>.<point>.<screen>`, with one grant (`ui.<solution>.>`) covering
// every point and screen.
func TestScreen_SubjectShape(t *testing.T) {
	got := contract.ScreenSubject("cdl-advisory", contract.ScreenPointWorkspaceSettings, "advisory-scope")
	if want := "ui.cdl-advisory.workspace.settings.advisory-scope"; got != want {
		t.Fatalf("ScreenSubject = %q, want %q", got, want)
	}
	if got := contract.ScreenSubjectPrefix("cdl-advisory"); got != "ui.cdl-advisory.>" {
		t.Fatalf("ScreenSubjectPrefix = %q", got)
	}
	if !contract.ScreenPointKnown(contract.ScreenPointWorkspaceSettings) {
		t.Fatal("workspace.settings must be a known point")
	}
	if contract.ScreenPointKnown("workspace.dashboard") {
		t.Fatal("an unopened point must not be known")
	}
}

// TestScreen_DescriptorValidity pins the REQUIRED trio. Point has no default by
// design: a screen with no point has nowhere to appear, and the platform drops
// the declaration rather than guessing.
func TestScreen_DescriptorValidity(t *testing.T) {
	ok := contract.ScreenDescriptor{Point: contract.ScreenPointWorkspaceSettings, Name: "advisory-scope", ObjectKind: "advisory_scope"}
	if !ok.Valid() {
		t.Fatal("well-formed descriptor reported invalid")
	}
	if ok.Label() != "advisory-scope" {
		t.Fatalf("Label with no Title = %q, want the Name", ok.Label())
	}
	titled := ok
	titled.Title = "Advisory Scope"
	if titled.Label() != "Advisory Scope" {
		t.Fatalf("Label = %q, want the Title", titled.Label())
	}

	for name, d := range map[string]contract.ScreenDescriptor{
		"no point":       {Name: "advisory-scope", ObjectKind: "advisory_scope"},
		"no name":        {Point: contract.ScreenPointWorkspaceSettings, ObjectKind: "advisory_scope"},
		"no object kind": {Point: contract.ScreenPointWorkspaceSettings, Name: "advisory-scope"},
	} {
		if d.Valid() {
			t.Errorf("%s: descriptor reported valid", name)
		}
	}

	// A forward point is STRUCTURALLY valid — an older platform drops it, but a
	// solution on a newer SDK must be able to declare it.
	forward := contract.ScreenDescriptor{Point: "workspace.dashboard", Name: "x", ObjectKind: "y"}
	if !forward.Valid() {
		t.Fatal("an unknown-but-nonempty point must stay structurally valid")
	}
}

// TestScreen_RoundTripRender is the render half over real NATS: a plain
// http.Handler on one side, the platform's envelope bytes on the other, and the
// platform context arriving intact in between.
func TestScreen_RoundTripRender(t *testing.T) {
	nc := startEmbeddedNATS(t)

	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sc, ok := transport.ScreenContextFrom(r)
		if !ok {
			t.Error("handler got no screen context")
			http.Error(w, "no context", http.StatusBadRequest)
			return
		}
		// The whole promise of the adapter: a solution author sees an ordinary
		// request, with ordinary methods and an ordinary query string.
		if r.Method != http.MethodGet {
			t.Errorf("render method = %q, want GET", r.Method)
		}
		fmt.Fprintf(w, "<p>ws=%s role=%s inc=%v tab=%s</p>",
			sc.Workspace, sc.UserRole, sc.Object["include"], r.URL.Query().Get("tab"))
	})

	stop, err := transport.ServeScreen(nc, "cdl-advisory", contract.ScreenPointWorkspaceSettings, "advisory-scope", h)
	if err != nil {
		t.Fatalf("serve: %v", err)
	}
	defer stop()

	reply := screenRequest(t, nc,
		contract.ScreenSubject("cdl-advisory", contract.ScreenPointWorkspaceSettings, "advisory-scope"),
		contract.ScreenRequest{
			Verb: contract.ScreenVerbRender,
			Path: "/",
			Form: map[string][]string{"tab": {"scope"}},
			Context: contract.ScreenContext{
				Solution:  "cdl-advisory",
				Point:     contract.ScreenPointWorkspaceSettings,
				Screen:    "advisory-scope",
				Workspace: "cdl-ops",
				UserRole:  "admin",
				ObjectID:  "cdl-ops",
				Object:    map[string]any{"include": "alteryx"},
			},
		})

	if reply.Status != http.StatusOK {
		t.Fatalf("status = %d (%s), want 200", reply.Status, reply.Error)
	}
	if want := "<p>ws=cdl-ops role=admin inc=alteryx tab=scope</p>"; reply.HTML != want {
		t.Fatalf("html = %q, want %q", reply.HTML, want)
	}
	if reply.Object != nil {
		t.Fatalf("a render must not return an object, got %v", reply.Object)
	}
}

// TestScreen_RoundTripSubmit is the submit half: form values arrive as ordinary
// form values, and WriteScreenObject's normalized object comes back as
// Reply.Object rather than as HTML — the split the platform keys its persist
// decision on.
func TestScreen_RoundTripSubmit(t *testing.T) {
	nc := startEmbeddedNATS(t)

	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("submit method = %q, want POST", r.Method)
		}
		if err := r.ParseForm(); err != nil {
			t.Errorf("parse form: %v", err)
		}
		if got := r.FormValue("include"); got != "alteryx" {
			t.Errorf("include = %q, want alteryx", got)
		}
		if err := transport.WriteScreenObject(w, map[string]any{
			"include": []any{"alteryx"},
			"exclude": []any{"printer"},
		}); err != nil {
			t.Errorf("write object: %v", err)
		}
	})

	stop, err := transport.ServeScreen(nc, "cdl-advisory", contract.ScreenPointWorkspaceSettings, "advisory-scope", h)
	if err != nil {
		t.Fatalf("serve: %v", err)
	}
	defer stop()

	reply := screenRequest(t, nc,
		contract.ScreenSubject("cdl-advisory", contract.ScreenPointWorkspaceSettings, "advisory-scope"),
		contract.ScreenRequest{
			Verb:   contract.ScreenVerbSubmit,
			Method: http.MethodPost,
			Path:   "/",
			Form:   map[string][]string{"include": {"alteryx"}, "exclude": {"printer"}},
			Context: contract.ScreenContext{
				Solution: "cdl-advisory", Point: contract.ScreenPointWorkspaceSettings,
				Screen: "advisory-scope", Workspace: "cdl-ops", UserRole: "owner",
				ObjectVersion: "abc123",
			},
		})

	if reply.HTML != "" {
		t.Fatalf("an object reply must carry no HTML, got %q", reply.HTML)
	}
	inc, ok := reply.Object["include"].([]any)
	if !ok || len(inc) != 1 || inc[0] != "alteryx" {
		t.Fatalf("object = %#v, want include [alteryx]", reply.Object)
	}
}

// TestScreen_SubmitInlineErrors pins the refusal path: a handler that re-renders
// its form instead of writing an object produces HTML at 200 and NO object —
// which is exactly how the platform learns there is nothing to persist. A
// refusal must never be a 4xx the browser swallows.
func TestScreen_SubmitInlineErrors(t *testing.T) {
	nc := startEmbeddedNATS(t)

	h := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`<span class="field__error">Too many patterns.</span>`))
	})
	stop, err := transport.ServeScreen(nc, "cdl-advisory", contract.ScreenPointWorkspaceSettings, "advisory-scope", h)
	if err != nil {
		t.Fatalf("serve: %v", err)
	}
	defer stop()

	reply := screenRequest(t, nc,
		contract.ScreenSubject("cdl-advisory", contract.ScreenPointWorkspaceSettings, "advisory-scope"),
		contract.ScreenRequest{
			Verb: contract.ScreenVerbSubmit, Method: http.MethodPost,
			Context: contract.ScreenContext{
				Solution: "cdl-advisory", Point: contract.ScreenPointWorkspaceSettings,
				Screen: "advisory-scope", Workspace: "cdl-ops",
			},
		})

	if reply.Status != http.StatusOK {
		t.Fatalf("status = %d, want 200 (a refusal renders inline, it does not fail)", reply.Status)
	}
	if reply.Object != nil {
		t.Fatalf("a refused submit must return no object, got %v", reply.Object)
	}
	if reply.HTML == "" {
		t.Fatal("a refused submit must return the re-rendered form")
	}
}

// TestScreen_DispatchRejectsSubjectMismatch pins the advisory identity check: a
// payload claiming a different solution/point/screen than the subject it arrived
// on is refused before the handler ever runs.
func TestScreen_DispatchRejectsSubjectMismatch(t *testing.T) {
	for name, payload := range map[string]string{
		"solution": `{"verb":"render","context":{"solution":"impostor","point":"workspace.settings","screen":"advisory-scope"}}`,
		"point":    `{"verb":"render","context":{"solution":"cdl-advisory","point":"workspace.dashboard","screen":"advisory-scope"}}`,
		"screen":   `{"verb":"render","context":{"solution":"cdl-advisory","point":"workspace.settings","screen":"other"}}`,
	} {
		called := false
		h := http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true })
		reply := transport.DispatchScreen(context.Background(), "cdl-advisory",
			contract.ScreenPointWorkspaceSettings, "advisory-scope", h, []byte(payload))

		if called {
			t.Errorf("%s mismatch: handler ran anyway", name)
		}
		if reply.Status != http.StatusForbidden {
			t.Errorf("%s mismatch: status = %d, want 403", name, reply.Status)
		}
		if reply.Error == "" {
			t.Errorf("%s mismatch: reply carries no reason", name)
		}
	}
}

// TestScreen_DispatchMalformedEnvelope — a payload that is not an envelope is a
// 400 with a reason, never a panic that kills the responder goroutine.
func TestScreen_DispatchMalformedEnvelope(t *testing.T) {
	reply := transport.DispatchScreen(context.Background(), "s", "workspace.settings", "x",
		http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}), []byte("not json"))
	if reply.Status != http.StatusBadRequest || reply.Error == "" {
		t.Fatalf("reply = %+v, want 400 with a reason", reply)
	}
}

// TestScreen_ServeGuards — nil conn / nil handler are errors at wiring time, not
// panics at render time.
func TestScreen_ServeGuards(t *testing.T) {
	if _, err := transport.ServeScreen(nil, "s", "workspace.settings", "x", http.NotFoundHandler()); err == nil {
		t.Error("nil conn must error")
	}
	nc := startEmbeddedNATS(t)
	if _, err := transport.ServeScreen(nc, "s", "workspace.settings", "x", nil); err == nil {
		t.Error("nil handler must error")
	}
}

// TestScreen_ServeScreens proves the announce/serve single-source-of-truth path:
// the SAME descriptors a solution announces drive the subscriptions, an invalid
// descriptor is skipped, and a descriptor with no handler is skipped.
func TestScreen_ServeScreens(t *testing.T) {
	nc := startEmbeddedNATS(t)

	screens := []contract.ScreenDescriptor{
		{Point: contract.ScreenPointWorkspaceSettings, Name: "advisory-scope", ObjectKind: "advisory_scope"},
		{Point: contract.ScreenPointWorkspaceSettings, Name: "unhandled", ObjectKind: "k"},
		{Name: "broken"}, // invalid: no point, no object kind
	}
	stop, err := transport.ServeScreens(nc, "cdl-advisory", screens, func(d contract.ScreenDescriptor) http.Handler {
		if d.Name != "advisory-scope" {
			return nil
		}
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte("<p>served</p>"))
		})
	})
	if err != nil {
		t.Fatalf("serve screens: %v", err)
	}
	defer stop()

	reply := screenRequest(t, nc,
		contract.ScreenSubject("cdl-advisory", contract.ScreenPointWorkspaceSettings, "advisory-scope"),
		contract.ScreenRequest{
			Verb: contract.ScreenVerbRender,
			Context: contract.ScreenContext{
				Solution: "cdl-advisory", Point: contract.ScreenPointWorkspaceSettings, Screen: "advisory-scope",
			},
		})
	if reply.HTML != "<p>served</p>" {
		t.Fatalf("html = %q", reply.HTML)
	}

	// The unhandled screen has no responder — the platform's own fallback
	// territory, and proof ServeScreens did not blanket-subscribe.
	if _, err := nc.Request(
		contract.ScreenSubject("cdl-advisory", contract.ScreenPointWorkspaceSettings, "unhandled"),
		[]byte("{}"), 200*time.Millisecond); err == nil {
		t.Fatal("a declared-but-unhandled screen must have no responder")
	}
}

// TestScreen_AnnounceCarriesScreens is the announce half: a solution's declared
// screens ride the manifest index (no leaf) and arrive on the platform's side of
// the watch, which is what earns the subject grant and mounts the tab.
func TestScreen_AnnounceCarriesScreens(t *testing.T) {
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

	screen := contract.ScreenDescriptor{
		Point:      contract.ScreenPointWorkspaceSettings,
		Name:       "advisory-scope",
		Title:      "Advisory Scope",
		ObjectKind: "advisory_scope",
		Icon:       "filter",
	}
	if err := transport.PublishSolution(ctx, kv, transport.SolutionPublish{
		Name:        "cdl-advisory",
		DisplayName: "CDL Incident Advisory",
		Version:     "0.1.0",
		Screens:     []contract.ScreenDescriptor{screen},
	}); err != nil {
		t.Fatalf("publish: %v", err)
	}

	entry, err := kv.Get(ctx, contract.ManifestKey("cdl-advisory"))
	if err != nil {
		t.Fatalf("get manifest: %v", err)
	}
	var m contract.SolutionManifest
	if err := json.Unmarshal(entry.Value(), &m); err != nil {
		t.Fatalf("unmarshal manifest: %v", err)
	}
	if len(m.Screens) != 1 || m.Screens[0] != screen {
		t.Fatalf("announced screens = %+v, want %+v", m.Screens, screen)
	}
	// Screens are a capability declaration, not an artifact: no leaf, no index
	// entry (the fragment never travels the announce path).
	for _, ref := range m.Artifacts {
		if ref.Kind == "screen" {
			t.Fatalf("screens must not appear as artifact leaves: %+v", ref)
		}
	}

	// A structurally broken declaration fails partner-side, before the bus.
	err = transport.PublishSolution(ctx, kv, transport.SolutionPublish{
		Name:    "cdl-advisory",
		Screens: []contract.ScreenDescriptor{{Name: "no-point"}},
	})
	if err == nil {
		t.Fatal("publish accepted a screen with no point / object kind")
	}
	if !strings.Contains(err.Error(), "point") {
		t.Fatalf("publish error should name the missing field: %v", err)
	}
}
