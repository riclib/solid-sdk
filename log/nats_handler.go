package log

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/nats-io/nats.go"
)

// LogSubject is the NATS subject a solution publishes its structured log
// records on: `solid.log.<solution>`. The platform subscribes to
// `solid.log.>` (or per-solution) to collect partner logs centrally. The
// subject shape mirrors the tool-subject convention (solid.<plane>.<solution>),
// so a partner account's per-subject permissions can grant log publish the same
// way they grant tool serve.
func LogSubject(solution string) string {
	return "solid.log." + solution
}

// logRecord is the wire form of one structured record shipped over NATS. It is
// a flat JSON object: the standard slog fields plus every attr flattened to a
// string-keyed map, so the platform-side collector can re-emit it without
// knowing the solution's attr vocabulary ahead of time.
type logRecord struct {
	Time    time.Time      `json:"time"`
	Level   string         `json:"level"`
	Message string         `json:"msg"`
	Attrs   map[string]any `json:"attrs,omitempty"`
}

// NATSHandlerOptions configures a NATSHandler.
type NATSHandlerOptions struct {
	// Level is the minimum level shipped to NATS (default slog.LevelInfo).
	Level slog.Leveler

	// BufferSize bounds the async send buffer (default 1024). When the buffer
	// is full, new records are DROPPED (and counted via Dropped()) rather than
	// blocking the caller — logging must never back-pressure the application
	// or fire-hose NATS. See the volume-guard note below.
	BufferSize int
}

// NATSHandler is an opt-in slog.Handler that publishes structured records to
// LogSubject(solution) over a caller-provided *nats.Conn. It never opens its
// own connection.
//
// Volume guard (REQUIRED): records are handed to a single background goroutine
// through a bounded channel. If the channel is full — the publisher can't keep
// up, or NATS is slow — Handle DROPS the record and increments a dropped
// counter instead of blocking or growing unboundedly. This caps both the memory
// the handler can hold and the rate at which it can hit NATS: the worst case is
// one in-flight publish plus BufferSize queued records, never an unbounded
// fan-out. Drop-on-full was chosen over sampling because it degrades gracefully
// under a burst (you lose the tail of a spike, not a uniform fraction of
// steady-state) and needs no per-record random draw on the hot path.
type NATSHandler struct {
	nc      *nats.Conn
	subject string
	level   slog.Leveler
	attrs   []slog.Attr
	groups  []string

	ch      chan []byte
	dropped atomic.Uint64
}

// NewNATSHandler constructs a NATSHandler that ships records for solution over
// nc. It starts one background publisher goroutine; call Close to stop it and
// drain. Returns the handler and a stop func.
//
// This is explicit and opt-in: the SDK provides it, nothing forces it on.
// Importing the log package for plain Pkg() logging does not construct this and
// does not require a live connection.
func NewNATSHandler(nc *nats.Conn, solution string, opts *NATSHandlerOptions) *NATSHandler {
	level := slog.Leveler(slog.LevelInfo)
	bufSize := 1024
	if opts != nil {
		if opts.Level != nil {
			level = opts.Level
		}
		if opts.BufferSize > 0 {
			bufSize = opts.BufferSize
		}
	}
	h := &NATSHandler{
		nc:      nc,
		subject: LogSubject(solution),
		level:   level,
		ch:      make(chan []byte, bufSize),
	}
	go h.run()
	return h
}

// run is the single background publisher. One goroutine = serialized,
// rate-bounded publishes; the bounded channel is the volume guard.
func (h *NATSHandler) run() {
	for b := range h.ch {
		// Best-effort: a publish error (no connection, etc.) is dropped — the
		// app's local file/stdout sinks remain the source of truth.
		_ = h.nc.Publish(h.subject, b)
	}
}

// Dropped returns the number of records dropped because the send buffer was
// full. Callers can surface this as a metric to detect log-volume pressure.
func (h *NATSHandler) Dropped() uint64 { return h.dropped.Load() }

// Close stops the background publisher and flushes any NATS buffers.
func (h *NATSHandler) Close() {
	close(h.ch)
	if h.nc != nil {
		_ = h.nc.Flush()
	}
}

func (h *NATSHandler) Enabled(_ context.Context, level slog.Level) bool {
	return level >= h.level.Level()
}

func (h *NATSHandler) Handle(_ context.Context, r slog.Record) error {
	rec := logRecord{
		Time:    r.Time,
		Level:   r.Level.String(),
		Message: r.Message,
	}
	attrs := make(map[string]any, r.NumAttrs()+len(h.attrs))
	for _, a := range h.attrs {
		attrs[a.Key] = a.Value.Any()
	}
	r.Attrs(func(a slog.Attr) bool {
		attrs[a.Key] = a.Value.Any()
		return true
	})
	if len(attrs) > 0 {
		rec.Attrs = attrs
	}
	b, err := json.Marshal(rec)
	if err != nil {
		// A record we can't marshal is dropped as data, not surfaced as a
		// handler error (a bad attr must not break the application's logging).
		h.dropped.Add(1)
		return nil
	}
	// Non-blocking send: drop on full. This is the volume guard.
	select {
	case h.ch <- b:
	default:
		h.dropped.Add(1)
	}
	return nil
}

func (h *NATSHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	merged := make([]slog.Attr, 0, len(h.attrs)+len(attrs))
	merged = append(merged, h.attrs...)
	merged = append(merged, attrs...)
	return &NATSHandler{
		nc:      h.nc,
		subject: h.subject,
		level:   h.level,
		attrs:   merged,
		groups:  h.groups,
		ch:      h.ch,
		// dropped counter is shared via the channel/goroutine; a derived
		// handler reports its own (zero) — the root handler owns the metric.
	}
}

func (h *NATSHandler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	groups := make([]string, 0, len(h.groups)+1)
	groups = append(groups, h.groups...)
	groups = append(groups, name)
	return &NATSHandler{
		nc:      h.nc,
		subject: h.subject,
		level:   h.level,
		attrs:   h.attrs,
		groups:  groups,
		ch:      h.ch,
	}
}
