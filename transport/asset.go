package transport

import (
	"bytes"
	"context"
	"fmt"

	"github.com/nats-io/nats.go/jetstream"
)

// AssetBucket is the JetStream object-store bucket partner solutions publish
// binary assets into (today: partner logos). It is SEPARATE from the solutions
// KV bucket — the KV tree carries the small declarative manifest + leaves
// (bounded text), while genuine binary blobs (images, documents) belong in the
// object store. The manifest references an asset by KEY (see AssetKey), never by
// inlining the bytes.
const AssetBucket = "solid-assets"

// AssetContentTypeKey is the object-metadata key the asset helpers stash the
// content-type under. The object store carries no first-class content-type
// field, so PutAsset writes it into ObjectMeta.Metadata and GetAsset reads it
// back — the platform serves it as the HTTP Content-Type for an <img>.
const AssetContentTypeKey = "content-type"

// AssetKey is the object-store key for a named asset of a solution, e.g.
// AssetKey("revassure", "partner-logo") → "revassure/partner-logo". Hierarchical
// so a solution's assets share a key prefix.
func AssetKey(solution, name string) string {
	return solution + "/" + name
}

// EnsureAssetBucket creates-or-gets the shared asset object store. Mirrors
// EnsureSolutionsBucket (get-then-create, FileStorage). Idempotent — safe to
// call from every announce.
func EnsureAssetBucket(ctx context.Context, js jetstream.JetStream) (jetstream.ObjectStore, error) {
	if os, err := js.ObjectStore(ctx, AssetBucket); err == nil {
		return os, nil
	}
	os, err := js.CreateObjectStore(ctx, jetstream.ObjectStoreConfig{
		Bucket:      AssetBucket,
		Description: "Partner solution binary assets (logos, future documents/attachments)",
		Storage:     jetstream.FileStorage,
	})
	if err != nil {
		return nil, fmt.Errorf("ensure asset bucket: %w", err)
	}
	return os, nil
}

// PutAsset stores data under key with its content-type carried in object
// metadata (the object store has no first-class content-type field). Overwrites
// an existing object at the same key.
func PutAsset(ctx context.Context, os jetstream.ObjectStore, key string, data []byte, contentType string) error {
	if os == nil {
		return fmt.Errorf("put asset %q: nil object store", key)
	}
	if key == "" {
		return fmt.Errorf("put asset: empty key")
	}
	meta := jetstream.ObjectMeta{
		Name:     key,
		Metadata: map[string]string{AssetContentTypeKey: contentType},
	}
	if _, err := os.Put(ctx, meta, bytes.NewReader(data)); err != nil {
		return fmt.Errorf("put asset %q: %w", key, err)
	}
	return nil
}

// GetAsset reads the bytes + content-type stored at key. The content-type comes
// from the object's metadata (written by PutAsset); a missing entry yields "".
func GetAsset(ctx context.Context, os jetstream.ObjectStore, key string) (data []byte, contentType string, err error) {
	if os == nil {
		return nil, "", fmt.Errorf("get asset %q: nil object store", key)
	}
	data, err = os.GetBytes(ctx, key)
	if err != nil {
		return nil, "", fmt.Errorf("get asset %q: %w", key, err)
	}
	if info, ierr := os.GetInfo(ctx, key); ierr == nil && info.Metadata != nil {
		contentType = info.Metadata[AssetContentTypeKey]
	}
	return data, contentType, nil
}
