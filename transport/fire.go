package transport

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/riclib/solid-sdk/contract"
)

// The fire wire — solution→platform "run a parametrised workflow" (the inverse of
// the runnable wire in runnable.go). A solution publishes a FireRequest to the
// durable work-queue (PublishFire); the platform's single fire consumer
// (ServeFires) validates + fires it and publishes a durable FireResult. The
// trigger + disposition ride JetStream (durable, at-least-once, survive a
// restart); there is no ephemeral progress channel (a fire LAUNCHES a run and
// returns — the launched run streams its own progress through the platform's
// normal conversation chrome). See contract/fire.go for the wire types.

const (
	// FireRunStream is the JetStream work-queue stream capturing every fire
	// request (FireRunSubjectAll). WorkQueuePolicy + the platform's durable
	// consumer give at-least-once, wait-if-down delivery; a dedup window absorbs a
	// double-published RunKey (the first idempotency layer).
	FireRunStream = "SOLID_FIRE_RUN"

	// FireResultStream is the durable stream carrying terminal fire dispositions
	// (FireResultSubjectAll), keyed per fire by FireResultSubject(runKey).
	FireResultStream = "SOLID_FIRE_RESULT"
)

// fireDedupWindow is the JetStream dedup window on the run stream: a fire
// re-published with the same RunKey (Nats-Msg-Id) inside this window is dropped
// by the server. Matches the platform workflow-trigger stream's posture.
const fireDedupWindow = 5 * time.Minute

// fireAckWait bounds how long the server waits for the platform to ack a fire
// before redelivery. A fire HANDLER is fast — it launches a background run and
// returns — so this is modest, far shorter than a runnable's heavy-job window.
const fireAckWait = 1 * time.Minute

// fireResultMaxAge bounds how long a terminal disposition is retained — long
// enough for a caller to read it after the fact, not forever.
const fireResultMaxAge = 24 * time.Hour

// EnsureFireStreams creates-or-updates the two JetStream streams the fire wire
// needs: the work-queue trigger stream (with a dedup window) and the durable
// result stream. Mirrors EnsureRunnableStreams (idempotent, FileStorage); either
// side may call it before publishing/serving.
func EnsureFireStreams(ctx context.Context, js jetstream.JetStream) error {
	if _, err := js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name:        FireRunStream,
		Description: "Solution fire requests (durable work-queue: run a workflow in a workspace; wait-if-down, at-least-once)",
		Subjects:    []string{contract.FireRunSubjectAll()},
		Retention:   jetstream.WorkQueuePolicy,
		Storage:     jetstream.FileStorage,
		Duplicates:  fireDedupWindow,
	}); err != nil {
		return fmt.Errorf("ensure fire run stream: %w", err)
	}
	if _, err := js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name:        FireResultStream,
		Description: "Solution fire dispositions (durable, keyed by run key; survives a platform restart)",
		Subjects:    []string{contract.FireResultSubjectAll()},
		Retention:   jetstream.LimitsPolicy,
		Storage:     jetstream.FileStorage,
		MaxAge:      fireResultMaxAge,
	}); err != nil {
		return fmt.Errorf("ensure fire result stream: %w", err)
	}
	return nil
}

// PublishFire publishes a fire request to the durable work-queue and returns once
// JetStream has durably accepted it (the fire is then guaranteed at-least-once
// delivery, surviving a platform restart). The SOLUTION side — the fork's
// portal-intake consumer calls this per inbound message, then acks the inbound
// message once PublishFire returns nil (the fire is durable, so the inbound
// message is safe to drop).
//
// RunKey rides as the Nats-Msg-Id so a re-published duplicate collapses inside
// the stream's dedup window. Required: Solution, Workspace, Workflow, RunKey.
func PublishFire(ctx context.Context, js jetstream.JetStream, req contract.FireRequest) error {
	if req.Solution == "" || req.Workspace == "" || req.Workflow == "" {
		return fmt.Errorf("publish fire: solution, workspace and workflow are required")
	}
	if req.RunKey == "" {
		return fmt.Errorf("publish fire: empty run key (the idempotency + result key)")
	}
	if err := EnsureFireStreams(ctx, js); err != nil {
		return err
	}
	body, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("publish fire: marshal: %w", err)
	}
	if _, err := js.Publish(ctx, contract.FireRunSubject(req.Solution, req.Workflow), body,
		jetstream.WithMsgID(req.RunKey)); err != nil {
		return fmt.Errorf("publish fire %s/%s (key %s): %w", req.Solution, req.Workflow, req.RunKey, err)
	}
	return nil
}

// FireHandler runs one fire on the PLATFORM side: validate the request (is the
// solution installed in Workspace? does Workflow resolve?), assign the run-as
// principal (the workspace's scheduled-run identity — a fire is the workspace
// intaking a signal), and launch the run. It returns the terminal FireResult — a
// DATA disposition (accepted/rejected) — OR a Go error for a TRANSIENT failure (a
// momentary store/NATS problem). A returned error leaves the trigger unacked so
// JetStream redelivers it; a rejected FireResult (nil error) is acked and never
// redelivered.
//
// The handler MUST be idempotent on RunKey: a redelivered fire (the at-least-once
// guarantee) re-invokes it, so the platform's existing source-run-id pre-check
// (ConversationExistsBySourceRun keyed on RunKey) makes a re-fire resolve to the
// same run and return rejected (skip), not a duplicate.
type FireHandler func(ctx context.Context, req contract.FireRequest) (contract.FireResult, error)

// ServeFires binds the platform's SINGLE durable work-queue consumer to every
// solution's fire requests (FireRunSubjectAll) and runs handler for each. It is
// the inverse of ServeRunnables: there, one consumer per solution serves that
// solution's runnables; here, ONE platform consumer drains every solution's
// fires. For each fire it: runs handler, publishes the terminal FireResult
// durably to the result stream, then acks. Returns a stop func to drain on
// shutdown.
//
// Ack discipline: durable, AckExplicit. The trigger is acked only AFTER the
// disposition is published — a crash before that leaves it unacked → redelivered
// (at-least-once). A handler Go error (transient) leaves it unacked too. An
// undecodable poison message is terminated (Nak would loop it forever).
func ServeFires(ctx context.Context, js jetstream.JetStream, handler FireHandler) (stop func(), err error) {
	if handler == nil {
		return nil, fmt.Errorf("serve fires: nil handler")
	}
	if err := EnsureFireStreams(ctx, js); err != nil {
		return nil, err
	}

	stream, err := js.Stream(ctx, FireRunStream)
	if err != nil {
		return nil, fmt.Errorf("serve fires: get run stream: %w", err)
	}
	// One durable consumer drains the whole fire work-queue. Unlike the runnable
	// wire there is no MaxAckPending=1 cap: a fire handler is fast (launch +
	// return) and idempotent on RunKey, so fires may process concurrently.
	cons, err := stream.CreateOrUpdateConsumer(ctx, jetstream.ConsumerConfig{
		Durable:       "fire_platform",
		FilterSubject: contract.FireRunSubjectAll(),
		AckPolicy:     jetstream.AckExplicitPolicy,
		AckWait:       fireAckWait,
	})
	if err != nil {
		return nil, fmt.Errorf("serve fires: consumer: %w", err)
	}

	cc, err := cons.Consume(func(msg jetstream.Msg) {
		serveOneFire(ctx, js, handler, msg)
	})
	if err != nil {
		return nil, fmt.Errorf("serve fires: consume: %w", err)
	}
	return cc.Stop, nil
}

// serveOneFire processes a single fire trigger: decode → run handler → publish
// the terminal disposition durably → ack. A transient handler error leaves it
// unacked for redelivery; a poison message is terminated; a disposition publish
// failure leaves it unacked (better a re-fire, deduped by RunKey, than a lost
// record).
func serveOneFire(ctx context.Context, js jetstream.JetStream, handler FireHandler, msg jetstream.Msg) {
	var req contract.FireRequest
	if err := json.Unmarshal(msg.Data(), &req); err != nil {
		_ = msg.Term() // poison — stop redelivery of garbage
		return
	}

	res, err := handler(ctx, req)
	if err != nil {
		// Transient — leave unacked → redelivery. Publish no disposition.
		return
	}
	if res.RunKey == "" {
		res.RunKey = req.RunKey
	}
	if res.Status == "" {
		res.Status = contract.FireStatusAccepted
	}

	// Publish the disposition durably BEFORE acking the trigger. If this fails the
	// trigger stays unacked and is redelivered (the handler's RunKey idempotency
	// absorbs the re-fire) — better a re-fire than a lost record.
	body, merr := json.Marshal(res)
	if merr != nil {
		body, _ = json.Marshal(contract.FireResult{
			RunKey: req.RunKey, Status: contract.FireStatusRejected,
			Error: "marshal result: " + merr.Error(),
		})
	}
	if _, perr := js.Publish(ctx, contract.FireResultSubject(req.RunKey), body); perr != nil {
		return // leave unacked → redelivery
	}
	_ = msg.Ack()
}
