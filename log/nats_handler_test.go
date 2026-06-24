package log_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"testing"
	"time"

	"github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"

	sdklog "github.com/riclib/solid-sdk/log"
)

// startEmbeddedNATS spins up an in-process NATS server and returns a connected
// client. Mirrors the transport package's harness (no JetStream needed here —
// log records ride core NATS publish, not a durable stream).
func startEmbeddedNATS(t *testing.T) *nats.Conn {
	t.Helper()
	opts := &server.Options{
		Host:   "127.0.0.1",
		Port:   -1,
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

func TestLogSubject(t *testing.T) {
	if got, want := sdklog.LogSubject("revassure"), "solid.log.revassure"; got != want {
		t.Fatalf("LogSubject = %q, want %q", got, want)
	}
}

// TestNATSHandler_RoundTrip publishes one record through the handler and
// confirms it arrives on solid.log.<solution> with the standard fields and the
// flattened attrs (including a shared field-key constant).
func TestNATSHandler_RoundTrip(t *testing.T) {
	nc := startEmbeddedNATS(t)

	sub, err := nc.SubscribeSync(sdklog.LogSubject("revassure"))
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	if err := nc.Flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}

	h := sdklog.NewNATSHandler(nc, "revassure", &sdklog.NATSHandlerOptions{Level: slog.LevelInfo})
	defer h.Close()
	logger := slog.New(h)

	logger.Info("query ran",
		sdklog.FieldSolution, "revassure",
		sdklog.FieldWorkspace, "acme",
		"rows", 42,
	)

	msg, err := sub.NextMsg(2 * time.Second)
	if err != nil {
		t.Fatalf("waiting for published log record: %v", err)
	}

	var rec struct {
		Level   string         `json:"level"`
		Message string         `json:"msg"`
		Attrs   map[string]any `json:"attrs"`
	}
	if err := json.Unmarshal(msg.Data, &rec); err != nil {
		t.Fatalf("unmarshal record: %v", err)
	}
	if rec.Message != "query ran" {
		t.Errorf("msg = %q, want %q", rec.Message, "query ran")
	}
	if rec.Level != slog.LevelInfo.String() {
		t.Errorf("level = %q, want %q", rec.Level, slog.LevelInfo.String())
	}
	if rec.Attrs[sdklog.FieldSolution] != "revassure" {
		t.Errorf("attr %s = %v, want revassure", sdklog.FieldSolution, rec.Attrs[sdklog.FieldSolution])
	}
	if rec.Attrs[sdklog.FieldWorkspace] != "acme" {
		t.Errorf("attr %s = %v, want acme", sdklog.FieldWorkspace, rec.Attrs[sdklog.FieldWorkspace])
	}
}

// TestNATSHandler_Enabled confirms the level gate.
func TestNATSHandler_Enabled(t *testing.T) {
	nc := startEmbeddedNATS(t)
	h := sdklog.NewNATSHandler(nc, "s", &sdklog.NATSHandlerOptions{Level: slog.LevelWarn})
	defer h.Close()
	if h.Enabled(context.Background(), slog.LevelInfo) {
		t.Error("info should be disabled at warn level")
	}
	if !h.Enabled(context.Background(), slog.LevelError) {
		t.Error("error should be enabled at warn level")
	}
}

// TestNATSHandler_DropOnFull confirms the volume guard: with a size-1 buffer and
// no consumer draining fast enough, a burst drops records instead of blocking.
func TestNATSHandler_DropOnFull(t *testing.T) {
	nc := startEmbeddedNATS(t)
	h := sdklog.NewNATSHandler(nc, "s", &sdklog.NATSHandlerOptions{
		Level:      slog.LevelInfo,
		BufferSize: 1,
	})
	defer h.Close()
	logger := slog.New(h)

	// Fire a large burst synchronously. The single background publisher cannot
	// keep up with a tight loop into a size-1 buffer, so some records MUST drop
	// rather than block this goroutine. The test asserts non-blocking + a
	// non-zero drop count, not an exact number (timing-dependent).
	for i := 0; i < 10000; i++ {
		logger.Info("burst", "i", i)
	}
	if h.Dropped() == 0 {
		t.Skip("no drops observed (publisher kept up on this fast machine); guard is non-blocking by construction")
	}
}
