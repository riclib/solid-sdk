package transport

import (
	"fmt"

	"github.com/nats-io/nats.go"
)

// HeartbeatSubject is the NATS subject a solution's daemon publishes a liveness
// beat on: `solid.heartbeat.<solution>`. It mirrors the log-subject shape
// (solid.<plane>.<solution>) so a partner account's per-subject permissions can
// grant heartbeat publish the same way they grant tool serve and log publish.
//
// Heartbeat is DISTINCT from announce: the announce (a durable KV manifest tree)
// is the "this solution exists" signal — re-publishing it bumps a revision and
// is the wrong tool for a 10-second pulse. The heartbeat is a cheap, ephemeral
// "the process is alive right now" pub on its own subject; it never touches the
// manifest, so it cannot churn the announce revision.
func HeartbeatSubject(solution string) string {
	return "solid.heartbeat." + solution
}

// HeartbeatWatchFilter is the wildcard subject the platform subscribes to in
// order to observe every solution's heartbeat: `solid.heartbeat.>`. The
// consumer parses the trailing token (SolutionFromHeartbeatSubject) to learn
// which solution beat.
func HeartbeatWatchFilter() string { return "solid.heartbeat.>" }

// SolutionFromHeartbeatSubject extracts `<solution>` from a heartbeat subject
// `solid.heartbeat.<solution>`. Returns "" for a subject that does not match
// the expected shape (defensive against an unrelated message on a shared
// subscription).
func SolutionFromHeartbeatSubject(subject string) string {
	const prefix = "solid.heartbeat."
	if len(subject) <= len(prefix) || subject[:len(prefix)] != prefix {
		return ""
	}
	return subject[len(prefix):]
}

// PublishHeartbeat publishes one liveness beat for solution over nc. It is a
// single cheap pub of an empty body — the subject's trailing token IS the
// payload (which solution is alive); the consumer stamps its own receive time.
// It does not flush (the daemon's loop tolerates a best-effort beat) and never
// opens a connection. A nil conn is a no-op error so a daemon wired without
// NATS degrades rather than panicking.
func PublishHeartbeat(nc *nats.Conn, solution string) error {
	if nc == nil {
		return fmt.Errorf("heartbeat: nil nats connection")
	}
	if solution == "" {
		return fmt.Errorf("heartbeat: empty solution name")
	}
	return nc.Publish(HeartbeatSubject(solution), nil)
}

// SubscribeHeartbeats subscribes to every solution's heartbeat
// (HeartbeatWatchFilter) and invokes onBeat with the solution name parsed from
// each beat's subject. It returns the subscription so the caller can
// Drain/Unsubscribe on shutdown. A beat whose subject does not parse to a
// solution name is ignored (onBeat is not called with "").
//
// The SDK opens no connection of its own — the caller hands it a live nc.
func SubscribeHeartbeats(nc *nats.Conn, onBeat func(solution string)) (*nats.Subscription, error) {
	if nc == nil {
		return nil, fmt.Errorf("heartbeat: nil nats connection")
	}
	return nc.Subscribe(HeartbeatWatchFilter(), func(msg *nats.Msg) {
		if name := SolutionFromHeartbeatSubject(msg.Subject); name != "" && onBeat != nil {
			onBeat(name)
		}
	})
}
