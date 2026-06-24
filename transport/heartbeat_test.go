package transport_test

import (
	"testing"
	"time"

	"github.com/riclib/solid-sdk/transport"
)

// TestHeartbeatSubject pins the subject shape (mirrors the log-subject
// convention solid.<plane>.<solution>) and the round-trip of the
// solution-from-subject parser.
func TestHeartbeatSubject(t *testing.T) {
	const sol = "revassure"
	got := transport.HeartbeatSubject(sol)
	if want := "solid.heartbeat.revassure"; got != want {
		t.Fatalf("HeartbeatSubject = %q, want %q", got, want)
	}
	if back := transport.SolutionFromHeartbeatSubject(got); back != sol {
		t.Fatalf("SolutionFromHeartbeatSubject(%q) = %q, want %q", got, back, sol)
	}
	// Defensive: an unrelated subject parses to "".
	for _, bad := range []string{"", "solid.heartbeat.", "solid.log.revassure", "nope"} {
		if back := transport.SolutionFromHeartbeatSubject(bad); back != "" {
			t.Errorf("SolutionFromHeartbeatSubject(%q) = %q, want empty", bad, back)
		}
	}
}

// TestHeartbeatRoundTrip proves PublishHeartbeat → SubscribeHeartbeats over
// embedded NATS: a published beat surfaces on the wildcard subscription with the
// solution name parsed from the subject.
func TestHeartbeatRoundTrip(t *testing.T) {
	nc := startEmbeddedNATS(t)

	beats := make(chan string, 4)
	sub, err := transport.SubscribeHeartbeats(nc, func(solution string) {
		beats <- solution
	})
	if err != nil {
		t.Fatalf("subscribe heartbeats: %v", err)
	}
	t.Cleanup(func() { _ = sub.Unsubscribe() })

	if err := transport.PublishHeartbeat(nc, "revassure"); err != nil {
		t.Fatalf("publish heartbeat: %v", err)
	}

	select {
	case got := <-beats:
		if got != "revassure" {
			t.Fatalf("beat solution = %q, want %q", got, "revassure")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for heartbeat")
	}
}

// TestPublishHeartbeatGuards covers the nil-conn / empty-name guards.
func TestPublishHeartbeatGuards(t *testing.T) {
	if err := transport.PublishHeartbeat(nil, "revassure"); err == nil {
		t.Error("PublishHeartbeat(nil conn) = nil error, want error")
	}
	nc := startEmbeddedNATS(t)
	if err := transport.PublishHeartbeat(nc, ""); err == nil {
		t.Error("PublishHeartbeat(empty name) = nil error, want error")
	}
}
