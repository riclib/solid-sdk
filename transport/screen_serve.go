package transport

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"

	"github.com/nats-io/nats.go"

	"github.com/riclib/solid-sdk/contract"
)

// screen_serve.go is the SOLUTION end of the screen wire (contract/screen.go) —
// the SDK adapter. Its whole job is to make the envelope disappear: a solution
// author writes an ordinary `http.Handler` (with ordinary templ components
// inside it) and this file turns each inbound envelope into a real
// `*http.Request`, runs the handler against a recorder, and folds the recorded
// response back into a contract.ScreenReply.
//
// The point is that a solution's screen code should be indistinguishable from a
// normal server-side handler. If a screen author ever has to think about NATS,
// subjects, or JSON envelopes to draw a form, this file has failed.
//
// Only the SERVE half is here. The two platform-side halves of the platform's
// origin package (S-1886 `infra/screenwire`) are deliberately absent:
//
//   - the CLIENT (Render / Submit over nats.Conn.RequestWithContext, collapsing
//     every transport-shaped failure into one "unavailable" signal so the
//     platform falls back to rendering the object generically). The platform is
//     the only caller of a screen; a solution never asks another solution to
//     render.
//   - the SANITIZER (the allowlist gate over the returned fragment). It is the
//     platform's boundary defence against the fragment it just received, and a
//     solution running it on its own output would prove nothing. What a fragment
//     must survive is documented in `docs/screens.md` — write inside the
//     allowlist; do not try to check it here.
//
// Naming: the exported names carry the `Screen` prefix because `transport` is a
// flat package already holding ServeTool / ServeRunnables / ServeFires. A bare
// `Serve`/`Dispatch` would be ambiguous at every call site.

// ScreenObjectContentType is the sentinel content type a handler writes to hand
// the platform a normalized object instead of HTML. See WriteScreenObject.
//
// A content type rather than a header because the object can be arbitrarily large
// (headers are the wrong place for a payload) and because it makes the two
// success shapes mutually exclusive by construction — a handler physically cannot
// write both an HTML body and an object body to the same recorder.
const ScreenObjectContentType = "application/vnd.solid.screen-object+json"

// screenCtxKey is the private key the platform ScreenContext rides under on the
// handler's request context.
type screenCtxKey struct{}

// ScreenContextFrom returns the platform-asserted context for this exchange.
// This is the ONLY way a screen handler learns which workspace / user / object it
// is serving — there is no ambient state and no read-back channel into the
// platform's configuration.
func ScreenContextFrom(r *http.Request) (contract.ScreenContext, bool) {
	c, ok := r.Context().Value(screenCtxKey{}).(contract.ScreenContext)
	return c, ok
}

// WriteScreenObject is how a submit handler hands the platform a validated,
// normalized object to persist. It writes the sentinel content type and the JSON
// body; the adapter lifts it into ScreenReply.Object and the PLATFORM performs
// the write. A solution never writes configuration itself.
//
// A handler that instead wants to refuse the submission simply renders its form
// again with the errors in it (a normal 200 HTML response) — that is the
// inline-error path, and it needs no special API at all.
func WriteScreenObject(w http.ResponseWriter, obj map[string]any) error {
	body, err := json.Marshal(obj)
	if err != nil {
		return fmt.Errorf("screen: marshal object: %w", err)
	}
	w.Header().Set("Content-Type", ScreenObjectContentType)
	_, err = w.Write(body)
	return err
}

// ServeScreen subscribes the solution's handler to one declared screen's subject
// (`ui.<solution>.<point>.<screen>`) and returns a stop func. The handler is a
// plain http.Handler; use ScreenContextFrom to read the platform context and
// WriteScreenObject to hand back a normalized object.
//
// One subscription per screen (not a wildcard) so a solution serves exactly what
// it declared: a screen it never announced has no responder, and the platform
// falls back rather than routing to a handler nobody approved.
func ServeScreen(conn *nats.Conn, solution, point, screen string, h http.Handler) (func(), error) {
	if conn == nil {
		return nil, fmt.Errorf("screen: serve %s/%s/%s: nil connection", solution, point, screen)
	}
	if h == nil {
		return nil, fmt.Errorf("screen: serve %s/%s/%s: nil handler", solution, point, screen)
	}

	sub, err := conn.Subscribe(contract.ScreenSubject(solution, point, screen), func(msg *nats.Msg) {
		reply := DispatchScreen(context.Background(), solution, point, screen, h, msg.Data)
		body, err := json.Marshal(reply)
		if err != nil {
			// A reply we cannot marshal is a bug in the handler's output, not a
			// transport failure. Answer with a minimal well-formed error so the
			// platform sees a decline instead of timing out into the offline
			// fallback (which would misattribute the fault).
			body, _ = json.Marshal(contract.ScreenReply{Status: http.StatusInternalServerError, Error: err.Error()})
		}
		_ = msg.Respond(body)
	})
	if err != nil {
		return nil, fmt.Errorf("screen: serve %s/%s/%s: %w", solution, point, screen, err)
	}
	return func() { _ = sub.Unsubscribe() }, nil
}

// ServeScreens subscribes one handler per declared descriptor, resolved through
// handlerFor(descriptor). It is the convenience every screen-serving solution
// otherwise hand-rolls: iterate the SAME []contract.ScreenDescriptor announced in
// SolutionPublish.Screens, so the served subject and the declared tab cannot
// drift.
//
// A descriptor handlerFor has no handler for (nil) is skipped — declaring a
// screen the binary cannot serve is a bug worth surviving: the platform's
// fallback renders the object generically. A subscribe failure stops and unwinds
// every subscription already made, so a partial serve never lingers.
func ServeScreens(conn *nats.Conn, solution string, screens []contract.ScreenDescriptor, handlerFor func(contract.ScreenDescriptor) http.Handler) (func(), error) {
	var stops []func()
	unwind := func() {
		for _, s := range stops {
			s()
		}
	}
	for _, d := range screens {
		if !d.Valid() {
			continue // dropped platform-side too; nothing to serve
		}
		h := handlerFor(d)
		if h == nil {
			continue
		}
		stop, err := ServeScreen(conn, solution, d.Point, d.Name, h)
		if err != nil {
			unwind()
			return nil, err
		}
		stops = append(stops, stop)
	}
	return unwind, nil
}

// DispatchScreen is the pure envelope→handler→envelope adapter, exported so it
// can be exercised without a NATS connection at all (and so an alternative
// transport could reuse it verbatim). A solution's screen test drives this
// directly.
func DispatchScreen(ctx context.Context, solution, point, screen string, h http.Handler, payload []byte) contract.ScreenReply {
	var req contract.ScreenRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		return contract.ScreenReply{Status: http.StatusBadRequest, Error: "malformed request envelope: " + err.Error()}
	}

	// Advisory identity check, mirroring the store proxy's
	// StoreCallRequest.Solution rule: the payload must agree with the subject
	// this handler was bound to. Until per-account NATS permissions are
	// provisioned everywhere, this is the check that makes the claim mean
	// something; once they are, it is a cheap consistency assertion.
	if req.Context.Solution != solution || req.Context.Screen != screen || req.Context.Point != point {
		return contract.ScreenReply{
			Status: http.StatusForbidden,
			Error: fmt.Sprintf("envelope claims %s/%s/%s but subject serves %s/%s/%s",
				req.Context.Solution, req.Context.Point, req.Context.Screen, solution, point, screen),
		}
	}

	httpReq, err := screenHTTPRequest(ctx, req)
	if err != nil {
		return contract.ScreenReply{Status: http.StatusBadRequest, Error: err.Error()}
	}

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httpReq)
	return screenReplyFromRecorder(rec)
}

// screenHTTPRequest rebuilds a real *http.Request from the envelope. Form values
// ride as a urlencoded body on a submit (so `r.ParseForm` / `r.FormValue` work
// exactly as they do over HTTP) and as a query string on a render.
func screenHTTPRequest(ctx context.Context, req contract.ScreenRequest) (*http.Request, error) {
	method := req.Method
	if method == "" {
		method = http.MethodGet
	}
	path := req.Path
	if path == "" {
		path = "/"
	}
	// contract.ScreenRequest.Form is a plain map so the contract package stays
	// pure data; url.Values IS that map type, so this conversion is free and
	// JSON-identical on both sides.
	form := url.Values(req.Form)

	var (
		httpReq *http.Request
		err     error
	)
	if req.Verb == contract.ScreenVerbSubmit {
		body := strings.NewReader(form.Encode())
		httpReq, err = http.NewRequestWithContext(ctx, method, path, body)
		if err != nil {
			return nil, fmt.Errorf("build request: %w", err)
		}
		httpReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	} else {
		if len(form) > 0 {
			if strings.Contains(path, "?") {
				path += "&" + form.Encode()
			} else {
				path += "?" + form.Encode()
			}
		}
		httpReq, err = http.NewRequestWithContext(ctx, method, path, http.NoBody)
		if err != nil {
			return nil, fmt.Errorf("build request: %w", err)
		}
	}

	return httpReq.WithContext(context.WithValue(httpReq.Context(), screenCtxKey{}, req.Context)), nil
}

// screenReplyFromRecorder folds the handler's recorded response into a
// ScreenReply, splitting on the sentinel content type: an object body becomes
// Reply.Object, anything else becomes Reply.HTML.
func screenReplyFromRecorder(rec *httptest.ResponseRecorder) contract.ScreenReply {
	res := rec.Result()
	defer res.Body.Close() //nolint:errcheck

	raw, err := io.ReadAll(res.Body)
	if err != nil {
		return contract.ScreenReply{Status: http.StatusInternalServerError, Error: "read handler response: " + err.Error()}
	}

	reply := contract.ScreenReply{Status: res.StatusCode}
	if isScreenObjectContentType(res.Header.Get("Content-Type")) {
		var obj map[string]any
		if err := json.Unmarshal(bytes.TrimSpace(raw), &obj); err != nil {
			return contract.ScreenReply{Status: http.StatusInternalServerError, Error: "malformed normalized object: " + err.Error()}
		}
		reply.Object = obj
		return reply
	}

	reply.HTML = string(raw)
	return reply
}

// isScreenObjectContentType matches the sentinel type ignoring any parameters
// (`; charset=utf-8`) a handler or middleware may have appended.
func isScreenObjectContentType(ct string) bool {
	base, _, _ := strings.Cut(ct, ";")
	return strings.EqualFold(strings.TrimSpace(base), ScreenObjectContentType)
}
