package transport

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/riclib/solid-sdk/contract"
)

// The runnable wire — the durable, scheduled-work analog of the tool wire
// (serve.go / call.go). A tool is a sub-second interactive request-reply call; a
// runnable is a long / unattended / must-survive-a-disconnect job. So the
// transport posture inverts: the TRIGGER and the RESULT ride JetStream (durable,
// at-least-once, survive a restart), while PROGRESS rides core NATS (ephemeral
// "watch it run" telemetry — a dropped line is cosmetic).
//
// Two JetStream streams back this wire:
//
//   - RunnableRunStream (work-queue, solid.runnable.run.>): the trigger queue. A
//     trigger waits in the queue if the solution is momentarily down, and is
//     delivered at-least-once. WorkQueuePolicy means one consumer drains it (the
//     solution's ServeRunnables consumer).
//   - RunnableResultStream (limits, solid.runnable.result.>): the durable result
//     channel keyed by RunID. The solution publishes the terminal RunnableResult
//     here; the platform (RunRunnable) reads exactly this run's result — and can
//     re-read it after a restart, because it is persisted, not a reply.
//
// Why a durable result stream and not a request-reply / reply-subject: a runnable
// can outlive the platform process that triggered it. A reply subject is bound to
// the live caller; if the platform restarts mid-run, the reply is lost. A result
// stream keyed by RunID lets a restarted platform re-consume the outcome by RunID.
const (
	// RunnableRunStream is the JetStream work-queue stream capturing every
	// runnable trigger (RunnableRunSubjectAll). WorkQueuePolicy + the solution's
	// durable consumer give at-least-once, wait-if-down delivery.
	RunnableRunStream = "SOLID_RUNNABLE_RUN"

	// RunnableResultStream is the durable stream carrying terminal results
	// (RunnableResultSubjectAll), keyed per run by RunnableResultSubject(runID).
	RunnableResultStream = "SOLID_RUNNABLE_RESULT"
)

// runnableAckWait bounds how long the server waits for the solution to ack a
// trigger before considering it for redelivery. A runnable is long-running, so
// it is generous; the serve loop also sends InProgress() heartbeats while the
// handler runs, so a job that outlasts even this window is not redelivered.
const runnableAckWait = 5 * time.Minute

// runnableResultMaxAge bounds how long a terminal result is retained on the
// durable stream — long enough for a platform restart to still read it, not
// forever. A result is small (one RunnableResult per run).
const runnableResultMaxAge = 24 * time.Hour

// EnsureRunnableStreams creates-or-updates the two JetStream streams the runnable
// wire needs: the work-queue trigger stream and the durable result stream.
// Mirrors EnsureSolutionsBucket (idempotent, FileStorage). Either side may call
// it before serving/triggering — it opens no connection of its own.
func EnsureRunnableStreams(ctx context.Context, js jetstream.JetStream) error {
	if _, err := js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name:        RunnableRunStream,
		Description: "Partner runnable triggers (durable work-queue: wait-if-down, at-least-once)",
		Subjects:    []string{contract.RunnableRunSubjectAll()},
		Retention:   jetstream.WorkQueuePolicy,
		Storage:     jetstream.FileStorage,
	}); err != nil {
		return fmt.Errorf("ensure runnable run stream: %w", err)
	}
	if _, err := js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name:        RunnableResultStream,
		Description: "Partner runnable terminal results (durable, keyed by run id; survives a platform restart)",
		Subjects:    []string{contract.RunnableResultSubjectAll()},
		Retention:   jetstream.LimitsPolicy,
		Storage:     jetstream.FileStorage,
		MaxAge:      runnableResultMaxAge,
	}); err != nil {
		return fmt.Errorf("ensure runnable result stream: %w", err)
	}
	return nil
}

// RunnableHandler runs one runnable invocation on the solution (partner) side. It
// receives the trigger envelope (with the run-as-a-human ScopedIdentity to gate
// against) and an emit callback for streaming progress. It returns the terminal
// RunnableResult — a declined/failed run sets Result.Status=RunStatusFailed +
// Result.Error (data), it does not return a Go error (there is no live caller to
// return one to across the durable queue).
type RunnableHandler func(ctx context.Context, req contract.RunnableRunRequest, emit func(contract.RunnableProgress)) contract.RunnableResult

// ServeRunnables binds a JetStream work-queue consumer to a solution's runnable
// triggers (RunnableRunSubjectPrefix) and runs handler for each. For every
// trigger it: invokes handler (which calls emit for progress → published on the
// core-NATS per-run subject), publishes the terminal RunnableResult durably to
// the result stream, then acks the trigger. Returns a stop func to drain the
// consumer on shutdown.
//
// Ack discipline (one in-flight, no double-delivery): the consumer is durable
// with AckPolicy=explicit and MaxAckPending=1, so the solution works one trigger
// at a time (a runnable is a heavy job, not a fan-out). The trigger is acked only
// AFTER the result is published — so a crash mid-run leaves the trigger
// unacked and JetStream redelivers it (at-least-once). A long AckWait plus
// InProgress() heartbeats while the handler runs keep a legitimately-long job
// from being redelivered as a false timeout. The handler must therefore be
// idempotent-tolerant: a redelivered trigger re-runs the work (at-least-once, not
// exactly-once — the durable RunID lets the platform dedupe the result).
func ServeRunnables(ctx context.Context, js jetstream.JetStream, nc *nats.Conn, solution string, handler RunnableHandler) (stop func(), err error) {
	if nc == nil {
		return nil, fmt.Errorf("serve runnables: nil nats connection")
	}
	if solution == "" {
		return nil, fmt.Errorf("serve runnables: empty solution name")
	}
	if handler == nil {
		return nil, fmt.Errorf("serve runnables: nil handler")
	}
	if err := EnsureRunnableStreams(ctx, js); err != nil {
		return nil, err
	}

	stream, err := js.Stream(ctx, RunnableRunStream)
	if err != nil {
		return nil, fmt.Errorf("serve runnables: get run stream: %w", err)
	}
	// Durable consumer filtered to this solution's triggers. The durable name is
	// per-solution so each solution drains only its own subtree of the shared
	// work-queue stream.
	cons, err := stream.CreateOrUpdateConsumer(ctx, jetstream.ConsumerConfig{
		Durable:       "runnable_" + sanitizeDurable(solution),
		FilterSubject: contract.RunnableRunSubjectPrefix(solution),
		AckPolicy:     jetstream.AckExplicitPolicy,
		AckWait:       runnableAckWait,
		MaxAckPending: 1, // one heavy job in flight at a time
	})
	if err != nil {
		return nil, fmt.Errorf("serve runnables: consumer: %w", err)
	}

	cc, err := cons.Consume(func(msg jetstream.Msg) {
		serveOne(ctx, js, nc, handler, msg)
	})
	if err != nil {
		return nil, fmt.Errorf("serve runnables: consume: %w", err)
	}
	return cc.Stop, nil
}

// serveOne processes a single trigger message: decode → run handler (heart-
// beating InProgress so a long run isn't redelivered) → publish the terminal
// result durably → ack. A trigger that fails to decode is terminated (Nak would
// just loop a poison message); a missing result publish leaves it unacked for
// redelivery.
func serveOne(ctx context.Context, js jetstream.JetStream, nc *nats.Conn, handler RunnableHandler, msg jetstream.Msg) {
	var req contract.RunnableRunRequest
	if err := json.Unmarshal(msg.Data(), &req); err != nil {
		// Poison message — drop it (TermWithReason: stop redelivery of garbage).
		_ = msg.Term()
		return
	}

	// Heartbeat InProgress while the handler runs so a legitimately-long job is
	// not redelivered as an ack timeout.
	beat := time.NewTicker(runnableAckWait / 3)
	defer beat.Stop()
	done := make(chan struct{})
	go func() {
		for {
			select {
			case <-done:
				return
			case <-beat.C:
				_ = msg.InProgress()
			}
		}
	}()

	emit := func(p contract.RunnableProgress) {
		if p.RunID == "" {
			p.RunID = req.RunID
		}
		// Progress is ephemeral core-NATS telemetry — best-effort, no flush.
		if b, err := json.Marshal(p); err == nil {
			_ = nc.Publish(contract.RunnableProgressSubject(req.RunID), b)
		}
	}

	res := handler(ctx, req, emit)
	close(done)
	if res.RunID == "" {
		res.RunID = req.RunID
	}
	if res.Status == "" {
		res.Status = contract.RunStatusCompleted
	}

	// Publish the terminal result durably to the result stream BEFORE acking the
	// trigger. If this publish fails the trigger stays unacked and is redelivered
	// (at-least-once) — better a re-run than a lost outcome.
	body, err := json.Marshal(res)
	if err != nil {
		body, _ = json.Marshal(contract.RunnableResult{
			RunID: req.RunID, Status: contract.RunStatusFailed,
			Error: "marshal result: " + err.Error(),
		})
	}
	// Publish to JetStream (the result stream captures this subject). A core
	// nc.Publish would not be persisted; use the JetStream publish path.
	if _, perr := js.Publish(ctx, contract.RunnableResultSubject(req.RunID), body); perr != nil {
		// Leave unacked → redelivery. Do not ack.
		return
	}
	_ = msg.Ack()
}

// RunRunnable triggers a runnable on a solution and awaits its terminal result,
// relaying progress to onProgress as it streams. It is the platform-side caller
// (the remote-proxy jobs.Runnable's Execute uses it).
//
//  1. Subscribe core-NATS to this run's progress subject (before publishing the
//     trigger, so no early progress is missed).
//  2. Set up a durable consumer on the result stream filtered to this run's
//     result subject (before the trigger, so the result is captured even if the
//     solution is fast).
//  3. Publish the trigger to the JetStream work-queue (durable — waits if the
//     solution is down).
//  4. Relay progress to onProgress; await the terminal RunnableResult (or
//     ctx deadline). Return the result.
//
// A transport failure (publish error, ctx deadline before any result) returns a
// Go error; a runnable-level failure returns a RunnableResult with Status=failed
// + Error set (the caller checks res.Status / res.Error). req.RunID must be set
// (the platform mints it); it keys the progress + result subjects.
func RunRunnable(ctx context.Context, js jetstream.JetStream, nc *nats.Conn, req contract.RunnableRunRequest, onProgress func(contract.RunnableProgress)) (contract.RunnableResult, error) {
	var zero contract.RunnableResult
	if nc == nil {
		return zero, fmt.Errorf("run runnable: nil nats connection")
	}
	if req.RunID == "" {
		return zero, fmt.Errorf("run runnable: empty run id (the platform mints it)")
	}
	if req.Solution == "" || req.Type == "" {
		return zero, fmt.Errorf("run runnable: solution and type are required")
	}
	if err := EnsureRunnableStreams(ctx, js); err != nil {
		return zero, err
	}

	// (1) progress subscription — core NATS, best-effort relay.
	progSub, err := nc.Subscribe(contract.RunnableProgressSubject(req.RunID), func(msg *nats.Msg) {
		if onProgress == nil {
			return
		}
		var p contract.RunnableProgress
		if err := json.Unmarshal(msg.Data, &p); err == nil {
			onProgress(p)
		}
	})
	if err != nil {
		return zero, fmt.Errorf("run runnable: subscribe progress: %w", err)
	}
	defer progSub.Unsubscribe() //nolint:errcheck

	// (2) result consumer — durable stream filtered to this run, created before
	// the trigger so a fast result is not missed. An ephemeral consumer (no
	// Durable) is fine: the STREAM is durable (survives a restart; a re-created
	// consumer with DeliverAll re-reads the persisted result), the consumer is a
	// cursor for this live call.
	stream, err := js.Stream(ctx, RunnableResultStream)
	if err != nil {
		return zero, fmt.Errorf("run runnable: get result stream: %w", err)
	}
	resCons, err := stream.CreateOrUpdateConsumer(ctx, jetstream.ConsumerConfig{
		FilterSubject: contract.RunnableResultSubject(req.RunID),
		AckPolicy:     jetstream.AckExplicitPolicy,
		DeliverPolicy: jetstream.DeliverAllPolicy,
	})
	if err != nil {
		return zero, fmt.Errorf("run runnable: result consumer: %w", err)
	}

	resultCh := make(chan contract.RunnableResult, 1)
	cc, err := resCons.Consume(func(msg jetstream.Msg) {
		var r contract.RunnableResult
		if err := json.Unmarshal(msg.Data(), &r); err != nil {
			_ = msg.Term()
			return
		}
		_ = msg.Ack()
		select {
		case resultCh <- r:
		default:
		}
	})
	if err != nil {
		return zero, fmt.Errorf("run runnable: consume result: %w", err)
	}
	defer cc.Stop()

	// (3) trigger — durable JetStream work-queue publish (waits if down).
	body, err := json.Marshal(req)
	if err != nil {
		return zero, fmt.Errorf("run runnable: marshal request: %w", err)
	}
	if _, err := js.Publish(ctx, contract.RunnableRunSubject(req.Solution, req.Type), body); err != nil {
		return zero, fmt.Errorf("run runnable: publish trigger: %w", err)
	}

	// (4) await the terminal result or ctx deadline.
	select {
	case r := <-resultCh:
		return r, nil
	case <-ctx.Done():
		return zero, fmt.Errorf("run runnable %s/%s (run %s): %w", req.Solution, req.Type, req.RunID, ctx.Err())
	}
}

// sanitizeDurable replaces characters illegal in a JetStream durable name
// (whitespace, ., *, >, path separators) with '_' so a solution name with a dot
// still yields a valid consumer durable.
func sanitizeDurable(s string) string {
	out := []byte(s)
	for i, c := range out {
		switch c {
		case ' ', '\t', '\n', '.', '*', '>', '/', '\\':
			out[i] = '_'
		}
	}
	return string(out)
}
