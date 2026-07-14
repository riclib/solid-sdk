package quack

// The quack-client end-to-end: a REAL quack engine (DuckDB + the pinned quack
// extension, exactly what the platform serves) plus a FAKE platform connect
// responder over embedded NATS. Proves the full S-1728 contract: handshake →
// statements server-side via quack_query → the reconnect contract when the
// engine is retired and reborn with a new per-boot token on a new port.
//
// Prerequisite: scripts/duckdb-fetch.sh has staged the extension binaries for
// this arch (a normal build/test does this once; the embed fails loudly
// otherwise).

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"

	"github.com/riclib/solid-sdk/contract"
)

func startEmbeddedNATS(t *testing.T) *nats.Conn {
	t.Helper()
	opts := &server.Options{
		Host:   "127.0.0.1",
		Port:   -1, // random free port
		NoSigs: true,
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

// testEngine is a real quack engine — DuckDB serving the pinned quack wire on
// a loopback port with a per-boot token, the way the platform's quackdb
// manager boots workspace engines. Test-only: the SDK is client-side; engines
// are platform-owned.
type testEngine struct {
	db    *sql.DB
	conn  *sql.Conn // pinned: quack_serve lives inside this connection
	uri   string
	token string
}

func startTestEngine(t *testing.T, extBase string) *testEngine {
	t.Helper()
	extDir, err := installDir(extBase)
	if err != nil {
		t.Fatalf("stage extensions: %v", err)
	}
	port := freePort(t)
	token := fmt.Sprintf("boot-%d-%d", port, time.Now().UnixNano())

	db, err := sql.Open("duckdb", filepath.Join(t.TempDir(), "engine.duckdb"))
	if err != nil {
		t.Fatalf("open engine duckdb: %v", err)
	}
	ctx := context.Background()
	conn, err := db.Conn(ctx)
	if err != nil {
		db.Close()
		t.Fatalf("pin serve conn: %v", err)
	}
	for _, stmt := range []string{
		fmt.Sprintf("SET extension_directory = '%s'", escapeSingleQuotes(extDir)),
		"SET autoinstall_known_extensions = false",
		"LOAD quack",
		fmt.Sprintf("CALL quack_serve('quack:127.0.0.1:%d', token=>'%s', allow_other_hostname=>true, disable_ssl=>true)", port, token),
	} {
		if _, err := conn.ExecContext(ctx, stmt); err != nil {
			conn.Close()
			db.Close()
			t.Fatalf("engine boot (%q): %v", stmt, err)
		}
	}
	e := &testEngine{db: db, conn: conn, uri: fmt.Sprintf("quack:localhost:%d", port), token: token}
	t.Cleanup(e.stop)
	return e
}

func (e *testEngine) stop() {
	if e.conn != nil {
		_, _ = e.conn.ExecContext(context.Background(), "CHECKPOINT")
		_ = e.conn.Close()
		e.conn = nil
	}
	if e.db != nil {
		_ = e.db.Close()
		e.db = nil
	}
}

func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("free port: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()
	return port
}

// fakePlatform is the platform's connect responder, faked: hop-1 grant check
// (only workspace "lmt" draws the solution), the smuggle denial, and the
// CURRENT engine handle — mutable, so a test can retire an engine and hand out
// the successor's per-boot token, exactly what the quackdb manager does.
type fakePlatform struct {
	mu       sync.Mutex
	uri      string
	token    string
	connects int
}

func (f *fakePlatform) set(e *testEngine) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.uri, f.token = e.uri, e.token
}

func (f *fakePlatform) connectCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.connects
}

func (f *fakePlatform) serve(t *testing.T, nc *nats.Conn, solution string) {
	t.Helper()
	sub, err := nc.Subscribe(contract.StoreCallSubjectPrefix(solution), func(msg *nats.Msg) {
		respond := func(res contract.QuackConnectResult) {
			body, _ := json.Marshal(res)
			_ = msg.Respond(body)
		}
		var req contract.StoreCallRequest
		if err := json.Unmarshal(msg.Data, &req); err != nil || !strings.HasSuffix(msg.Subject, "."+contract.StoreOpConnect) {
			respond(contract.QuackConnectResult{Error: "not granted", Code: contract.StoreCodeNotGranted})
			return
		}
		if req.Store != "" || req.Statement != "" || len(req.Args) > 0 || req.Solution != solution || req.Workspace != "lmt" {
			respond(contract.QuackConnectResult{Error: "not granted", Code: contract.StoreCodeNotGranted})
			return
		}
		f.mu.Lock()
		f.connects++
		uri, token := f.uri, f.token
		f.mu.Unlock()
		respond(contract.QuackConnectResult{URI: uri, Token: token, TLS: false, Duration: 1})
	})
	if err != nil {
		t.Fatalf("subscribe fake platform: %v", err)
	}
	t.Cleanup(func() { _ = sub.Unsubscribe() })
}

// TestConn_ExecQueryRoundTrip is the package's proof of life: handshake, DDL +
// DML + query server-side (marker-prefixed, single-quote torture), one connect.
func TestConn_ExecQueryRoundTrip(t *testing.T) {
	nc := startEmbeddedNATS(t)
	extBase := t.TempDir()
	engine := startTestEngine(t, extBase)
	platform := &fakePlatform{}
	platform.set(engine)
	platform.serve(t, nc, "revassure")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	conn, err := Connect(ctx, nc, "revassure", "lmt", WithExtensionsDir(extBase))
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer conn.Close()

	if err := conn.Exec(ctx, "CREATE TABLE events (id INTEGER, note VARCHAR)"); err != nil {
		t.Fatalf("exec create: %v", err)
	}
	if err := conn.Exec(ctx, "INSERT INTO events VALUES (1, 'it''s alive')"); err != nil {
		t.Fatalf("exec insert: %v", err)
	}
	rows, err := conn.Query(ctx, "SELECT note FROM events WHERE id = 1")
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	defer rows.Close()
	if !rows.Next() {
		t.Fatal("no rows")
	}
	var note string
	if err := rows.Scan(&note); err != nil {
		t.Fatalf("scan: %v", err)
	}
	if note != "it's alive" {
		t.Fatalf("note = %q, want %q", note, "it's alive")
	}
	if got := platform.connectCount(); got != 1 {
		t.Fatalf("connect handshakes = %d, want 1", got)
	}
}

// TestConn_ReconnectOnEngineRetire is the reconnect contract end-to-end: the
// engine is retired (stopped) and a successor boots with a NEW per-boot token
// on a NEW port. The next statement fails against the corpse, Conn re-runs the
// handshake, sees a changed handle, reconnects, and retries — the caller never
// sees the failure.
func TestConn_ReconnectOnEngineRetire(t *testing.T) {
	nc := startEmbeddedNATS(t)
	extBase := t.TempDir()
	engineA := startTestEngine(t, extBase)
	platform := &fakePlatform{}
	platform.set(engineA)
	platform.serve(t, nc, "revassure")

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	conn, err := Connect(ctx, nc, "revassure", "lmt", WithExtensionsDir(extBase))
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer conn.Close()
	if err := conn.Exec(ctx, "CREATE TABLE a (x INTEGER)"); err != nil {
		t.Fatalf("exec on engine A: %v", err)
	}

	// Retire A; boot the successor with a fresh boot token on a fresh port.
	engineA.stop()
	engineB := startTestEngine(t, extBase)
	platform.set(engineB)

	// The cached {uri, token} now points at a corpse. The statement must
	// still succeed — re-handshake + reconnect + retry, invisibly.
	if err := conn.Exec(ctx, "CREATE TABLE b (x INTEGER)"); err != nil {
		t.Fatalf("exec across engine retirement: %v", err)
	}
	rows, err := conn.Query(ctx, "SELECT count(*) FROM b")
	if err != nil {
		t.Fatalf("query on engine B: %v", err)
	}
	rows.Close()

	if got := platform.connectCount(); got != 2 {
		t.Fatalf("connect handshakes = %d, want 2 (initial + reconnect)", got)
	}
}

// TestConn_StatementErrorNoRetry pins the boot-identity probe: a statement
// that fails on a LIVE engine (same handle handed back) is returned as-is —
// exactly one probe handshake, no reconnect, no blind retry of DML.
func TestConn_StatementErrorNoRetry(t *testing.T) {
	nc := startEmbeddedNATS(t)
	extBase := t.TempDir()
	engine := startTestEngine(t, extBase)
	platform := &fakePlatform{}
	platform.set(engine)
	platform.serve(t, nc, "revassure")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	conn, err := Connect(ctx, nc, "revassure", "lmt", WithExtensionsDir(extBase))
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer conn.Close()

	err = conn.Exec(ctx, "INSERT INTO no_such_table VALUES (1)")
	if err == nil {
		t.Fatal("expected statement error, got nil")
	}
	// Initial handshake + exactly one boot-identity probe.
	if got := platform.connectCount(); got != 2 {
		t.Fatalf("connect handshakes = %d, want 2 (initial + probe)", got)
	}
	// The connection is still live on the same engine.
	if err := conn.Exec(ctx, "CREATE TABLE fine (x INTEGER)"); err != nil {
		t.Fatalf("exec after statement error: %v", err)
	}
}

// TestConnect_Denied proves a policy denial surfaces as a Go error carrying
// the platform's code — a Conn cannot exist without a granted engine.
func TestConnect_Denied(t *testing.T) {
	nc := startEmbeddedNATS(t)
	platform := &fakePlatform{}
	platform.serve(t, nc, "revassure")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	_, err := Connect(ctx, nc, "revassure", "someone-elses-workspace", WithExtensionsDir(t.TempDir()))
	if err == nil {
		t.Fatal("expected denial error, got nil")
	}
	if !strings.Contains(err.Error(), contract.StoreCodeNotGranted) {
		t.Fatalf("denial error should carry the code, got: %v", err)
	}
}

// TestConnect_NoResponder proves a transport outage (platform down) is a Go
// error, distinct from policy.
func TestConnect_NoResponder(t *testing.T) {
	nc := startEmbeddedNATS(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	_, err := Connect(ctx, nc, "revassure", "lmt", WithExtensionsDir(t.TempDir()))
	if err == nil {
		t.Fatal("expected transport error with no responder, got nil")
	}
}
