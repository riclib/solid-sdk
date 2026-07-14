package transport_test

import (
	"context"
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/riclib/solid-sdk/contract"
	"github.com/riclib/solid-sdk/transport"
)

// TestQuackConnect_Subject pins the connect op onto the store-call subject
// family — the per-account publish prefix (S-1706) covers it with no extra
// grant.
func TestQuackConnect_Subject(t *testing.T) {
	if got := contract.StoreCallSubject("revassure", contract.StoreOpConnect); got != "solid.store.call.revassure.connect" {
		t.Fatalf("StoreCallSubject = %q, want solid.store.call.revassure.connect", got)
	}
}

// TestQuackConnect_ValidatesRequest proves an empty solution or workspace is
// rejected as a Go error BEFORE any publish — a malformed handshake never
// touches the bus. Mirrors TestStoreCall_ValidatesRequest.
func TestQuackConnect_ValidatesRequest(t *testing.T) {
	nc := startEmbeddedNATS(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	if _, err := transport.QuackConnect(ctx, nc, "", "lmt"); err == nil {
		t.Fatal("expected error for empty solution, got nil")
	}
	if _, err := transport.QuackConnect(ctx, nc, "revassure", ""); err == nil {
		t.Fatal("expected error for empty workspace, got nil")
	}
}

// TestQuackConnect_RoundTrip is the connect handshake end-to-end: a FAKE
// platform responder on the connect subject over embedded NATS. It pins the
// WIRE SHAPE the platform's responder enforces (app/storeproxy: subject op =
// connect, payload op agrees, NO store/statement/args — the helper cannot
// smuggle them by construction) and both reply legs: a granted connect returns
// {uri, token, tls}; a denial rides the reply as Error + Code with NO Go error
// — the caller tells policy from outage.
func TestQuackConnect_RoundTrip(t *testing.T) {
	nc := startEmbeddedNATS(t)

	// --- fake platform side: subscribe on the connect subject and respond ---
	sub, err := nc.Subscribe(contract.StoreCallSubject("revassure", contract.StoreOpConnect), func(msg *nats.Msg) {
		var req contract.StoreCallRequest
		if err := json.Unmarshal(msg.Data, &req); err != nil {
			reply, _ := json.Marshal(contract.QuackConnectResult{Error: "bad request", Code: contract.StoreCodeExecFailed})
			_ = msg.Respond(reply)
			return
		}
		// The platform denies a connect that smuggles store fields (uniform
		// denial, byte-identical to every other grant failure). The helper
		// builds the request, so this leg must be unreachable from QuackConnect.
		if req.Store != "" || req.Statement != "" || len(req.Args) > 0 || req.Op != contract.StoreOpConnect || req.Solution != "revassure" {
			reply, _ := json.Marshal(contract.QuackConnectResult{Error: "not granted", Code: contract.StoreCodeNotGranted})
			_ = msg.Respond(reply)
			return
		}
		// Binding-is-the-grant, faked: workspace "unbound" does not draw us.
		if req.Workspace == "unbound" {
			reply, _ := json.Marshal(contract.QuackConnectResult{Error: "not granted", Code: contract.StoreCodeNotGranted, Duration: 1})
			_ = msg.Respond(reply)
			return
		}
		reply, _ := json.Marshal(contract.QuackConnectResult{URI: "quack:localhost:9601", Token: "boot-token-1", TLS: false, Duration: 74})
		_ = msg.Respond(reply)
	})
	if err != nil {
		t.Fatalf("subscribe responder: %v", err)
	}
	defer sub.Unsubscribe() //nolint:errcheck

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	// --- solution side: a bound workspace gets {uri, token, tls} ---
	res, err := transport.QuackConnect(ctx, nc, "revassure", "lmt")
	if err != nil {
		t.Fatalf("quack connect: %v", err)
	}
	if res.Error != "" || res.Code != "" {
		t.Fatalf("granted connect returned error: %q / %q", res.Error, res.Code)
	}
	if res.URI != "quack:localhost:9601" {
		t.Fatalf("uri = %q, want quack:localhost:9601", res.URI)
	}
	if res.Token != "boot-token-1" {
		t.Fatalf("token = %q, want boot-token-1", res.Token)
	}
	if res.TLS {
		t.Fatal("tls = true, want false (loopback engines have no server-side TLS)")
	}
	if res.Duration != 74 {
		t.Fatalf("duration = %d, want 74", res.Duration)
	}

	// --- solution side: a policy denial rides the reply, not a Go error ---
	denied, err := transport.QuackConnect(ctx, nc, "revassure", "unbound")
	if err != nil {
		t.Fatalf("denied connect should NOT surface a Go error, got: %v", err)
	}
	if denied.Code != contract.StoreCodeNotGranted {
		t.Fatalf("denied connect Code = %q, want %q", denied.Code, contract.StoreCodeNotGranted)
	}
	if denied.Error != "not granted" {
		t.Fatalf("denied connect Error = %q, want the uniform %q", denied.Error, "not granted")
	}
	if denied.URI != "" || denied.Token != "" {
		t.Fatal("denied connect must not carry a uri or token")
	}
}

// TestQuackConnect_ReconnectOnStaleToken exercises the reconnect contract:
// tokens are PER ENGINE BOOT, so an evicted/compacted/restarted engine
// invalidates the held {uri, token} — the solution's move on any connection
// failure is to re-run the handshake, never to persist or retry the stale
// handle. The fake platform "reboots" its engine between calls (new token,
// new port), and the second handshake returns the fresh pair.
func TestQuackConnect_ReconnectOnStaleToken(t *testing.T) {
	nc := startEmbeddedNATS(t)

	// boot counts the fake engine's boots: each "boot" mints a new token and
	// usually lands on a new port, exactly like the real LRU manager.
	var boot atomic.Int64
	boot.Store(1)

	sub, err := nc.Subscribe(contract.StoreCallSubject("revassure", contract.StoreOpConnect), func(msg *nats.Msg) {
		n := boot.Load()
		reply, _ := json.Marshal(contract.QuackConnectResult{
			URI:   "quack:localhost:960" + string(rune('0'+n)),
			Token: "boot-token-" + string(rune('0'+n)),
		})
		_ = msg.Respond(reply)
	})
	if err != nil {
		t.Fatalf("subscribe responder: %v", err)
	}
	defer sub.Unsubscribe() //nolint:errcheck

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	// First handshake: the solution holds {uri, token} as per-session state.
	first, err := transport.QuackConnect(ctx, nc, "revassure", "lmt")
	if err != nil {
		t.Fatalf("first connect: %v", err)
	}
	if first.Token != "boot-token-1" || first.URI != "quack:localhost:9601" {
		t.Fatalf("first connect = %q @ %q, want boot-token-1 @ quack:localhost:9601", first.Token, first.URI)
	}

	// The engine is retired (eviction / compaction / restart): the next boot
	// mints a NEW token on a NEW port. The solution's held handle now fails
	// (auth failure / connection refused — the normal signal, not an incident).
	boot.Store(2)

	// The contract: on connection failure, re-run the handshake and reconnect.
	second, err := transport.QuackConnect(ctx, nc, "revassure", "lmt")
	if err != nil {
		t.Fatalf("re-handshake: %v", err)
	}
	if second.Token != "boot-token-2" || second.URI != "quack:localhost:9602" {
		t.Fatalf("re-handshake = %q @ %q, want boot-token-2 @ quack:localhost:9602", second.Token, second.URI)
	}
	if second.Token == first.Token {
		t.Fatal("re-handshake must mint a fresh per-boot token")
	}
}

// TestQuackConnect_NoResponder proves a transport failure (nothing serving the
// connect subject — the platform is down) surfaces as a Go error, distinct from
// an operation-level denial. Mirrors TestStoreCall_NoResponder.
func TestQuackConnect_NoResponder(t *testing.T) {
	nc := startEmbeddedNATS(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	if _, err := transport.QuackConnect(ctx, nc, "revassure", "lmt"); err == nil {
		t.Fatal("expected transport error calling connect with no responder, got nil")
	}
}

// TestQuackConnectResult_WireShape pins the mirror against the platform's
// reply bytes: contract.QuackConnectResult must parse app/storeproxy's
// QuackConnectResult JSON verbatim (field names ARE the wire contract), and a
// success/denial reply must marshal to the exact same shape the platform
// emits — tls and duration_ms always present, everything else omitempty.
func TestQuackConnectResult_WireShape(t *testing.T) {
	// The platform's success reply, byte-for-byte per app/storeproxy/responder.go.
	platformReply := `{"uri":"quack:localhost:9601","token":"per-boot-token","tls":false,"duration_ms":74}`
	var res contract.QuackConnectResult
	if err := json.Unmarshal([]byte(platformReply), &res); err != nil {
		t.Fatalf("unmarshal platform reply: %v", err)
	}
	if res.URI != "quack:localhost:9601" || res.Token != "per-boot-token" || res.TLS || res.Duration != 74 {
		t.Fatalf("parsed reply = %+v", res)
	}

	// The platform's uniform denial, byte-identical for every grant failure.
	denial := `{"error":"not granted","code":"not_granted","tls":false,"duration_ms":1}`
	var den contract.QuackConnectResult
	if err := json.Unmarshal([]byte(denial), &den); err != nil {
		t.Fatalf("unmarshal denial: %v", err)
	}
	if den.Error != "not granted" || den.Code != contract.StoreCodeNotGranted {
		t.Fatalf("parsed denial = %+v", den)
	}

	// Marshal side: the mirror emits the platform's field names, nothing more.
	out, err := json.Marshal(contract.QuackConnectResult{URI: "quack:localhost:9601", Token: "per-boot-token", Duration: 74})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if string(out) != platformReply {
		t.Fatalf("marshal = %s, want %s", out, platformReply)
	}
}

// TestStatementMarker pins the statement-log attribution prefix — the comment
// must survive verbatim into the engine's statement log, so its exact shape is
// part of the convention.
func TestStatementMarker(t *testing.T) {
	got := contract.StatementMarker("revassure")
	if got != "/* solid:solution=revassure */ " {
		t.Fatalf("StatementMarker = %q, want %q", got, "/* solid:solution=revassure */ ")
	}
	stmt := got + "INSERT INTO revassure.interestingevents SELECT 1"
	if !strings.HasPrefix(stmt, "/* solid:solution=revassure */") {
		t.Fatalf("marker must prefix the statement, got %q", stmt)
	}
}
