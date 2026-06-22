// Package transport carries the thin NATS/JetStream helpers over the contract
// wire types: the capability announcement (KV) and tool serving/calling
// (request-reply). It takes the standard nats.go handles (*nats.Conn,
// jetstream.JetStream/KeyValue) so it composes with whatever the platform
// already runs — the SDK adds no connection lifecycle of its own.
package transport

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/riclib/solid-sdk/contract"
)

// EnsureSolutionsBucket creates-or-gets the solutions announce bucket. Mirrors
// v4's createOrGetKVBucket pattern (FileStorage, get-then-create). Idempotent.
func EnsureSolutionsBucket(ctx context.Context, js jetstream.JetStream) (jetstream.KeyValue, error) {
	if kv, err := js.KeyValue(ctx, contract.SolutionsBucket); err == nil {
		return kv, nil
	}
	kv, err := js.CreateKeyValue(ctx, jetstream.KeyValueConfig{
		Bucket:      contract.SolutionsBucket,
		Description: "Partner solution manifests (capability announcement)",
		Storage:     jetstream.FileStorage,
	})
	if err != nil {
		return nil, fmt.Errorf("ensure solutions bucket: %w", err)
	}
	return kv, nil
}

// AnnounceSolution publishes a solution's manifest into the announce bucket.
// Re-announcing the same name is an update — the platform's watcher re-wires.
// (A heartbeat/TTL liveness story sits on top of this later; for now a Put is a
// durable announce.)
func AnnounceSolution(ctx context.Context, kv jetstream.KeyValue, m contract.SolutionManifest) error {
	if m.Name == "" {
		return fmt.Errorf("announce: manifest has no name")
	}
	b, err := json.Marshal(m)
	if err != nil {
		return fmt.Errorf("announce %q: marshal: %w", m.Name, err)
	}
	if _, err := kv.Put(ctx, contract.SolutionKey(m.Name), b); err != nil {
		return fmt.Errorf("announce %q: %w", m.Name, err)
	}
	return nil
}

// WatchSolutions invokes onPut for every current and future manifest in the
// bucket, and onDelete (if non-nil) when a solution's entry is removed (the
// grey-out signal). It returns once the initial replay has been delivered; the
// watch then continues in a goroutine until ctx is cancelled.
//
// The platform side calls this to wire announced solutions live. A bad manifest
// (unmarshal failure) is skipped, not fatal — announce-time validation is the
// platform's job before it acts on a manifest, never a watcher panic.
func WatchSolutions(ctx context.Context, kv jetstream.KeyValue, onPut func(contract.SolutionManifest), onDelete func(name string)) error {
	w, err := kv.WatchAll(ctx)
	if err != nil {
		return fmt.Errorf("watch solutions: %w", err)
	}
	go func() {
		defer w.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case e := <-w.Updates():
				if e == nil {
					continue // nil marks end of initial replay
				}
				switch e.Operation() {
				case jetstream.KeyValuePut:
					var m contract.SolutionManifest
					if err := json.Unmarshal(e.Value(), &m); err != nil {
						continue
					}
					if onPut != nil {
						onPut(m)
					}
				case jetstream.KeyValueDelete, jetstream.KeyValuePurge:
					if onDelete != nil {
						onDelete(e.Key())
					}
				}
			}
		}
	}()
	return nil
}
