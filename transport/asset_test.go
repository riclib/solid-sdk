package transport_test

import (
	"bytes"
	"context"
	"testing"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/riclib/solid-sdk/transport"
)

// TestAssetRoundTrip is the object-store proof: EnsureAssetBucket →
// PutAsset → GetAsset returns the same bytes and the content-type that rode in
// the object metadata. This is the partner-logo path (images-by-reference: the
// manifest carries only the AssetKey, the bytes live here).
func TestAssetRoundTrip(t *testing.T) {
	nc := startEmbeddedNATS(t)
	js, err := jetstream.New(nc)
	if err != nil {
		t.Fatalf("jetstream: %v", err)
	}
	ctx := context.Background()

	os, err := transport.EnsureAssetBucket(ctx, js)
	if err != nil {
		t.Fatalf("ensure asset bucket: %v", err)
	}
	// Idempotent: a second ensure returns the same bucket, no error.
	if _, err := transport.EnsureAssetBucket(ctx, js); err != nil {
		t.Fatalf("ensure asset bucket (second): %v", err)
	}

	key := transport.AssetKey("revassure", "partner-logo")
	if want := "revassure/partner-logo"; key != want {
		t.Fatalf("AssetKey = %q, want %q", key, want)
	}

	want := []byte(`<svg xmlns="http://www.w3.org/2000/svg"><rect/></svg>`)
	const wantCT = "image/svg+xml"
	if err := transport.PutAsset(ctx, os, key, want, wantCT); err != nil {
		t.Fatalf("put asset: %v", err)
	}

	got, gotCT, err := transport.GetAsset(ctx, os, key)
	if err != nil {
		t.Fatalf("get asset: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("asset bytes did not round-trip: got %d bytes, want %d", len(got), len(want))
	}
	if gotCT != wantCT {
		t.Fatalf("content-type = %q, want %q", gotCT, wantCT)
	}
}

// TestGetAsset_Missing proves a missing key surfaces as an error (the consumer's
// nil/err-safe path: a missing logo never fails the announce).
func TestGetAsset_Missing(t *testing.T) {
	nc := startEmbeddedNATS(t)
	js, err := jetstream.New(nc)
	if err != nil {
		t.Fatalf("jetstream: %v", err)
	}
	ctx := context.Background()
	os, err := transport.EnsureAssetBucket(ctx, js)
	if err != nil {
		t.Fatalf("ensure asset bucket: %v", err)
	}
	if _, _, err := transport.GetAsset(ctx, os, transport.AssetKey("nope", "partner-logo")); err == nil {
		t.Fatal("expected error getting a missing asset, got nil")
	}
}
