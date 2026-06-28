package encrypt_test

import (
	"strings"
	"testing"

	"github.com/riclib/solid-sdk/encrypt"
)

func TestRoundTrip(t *testing.T) {
	key, err := encrypt.Key("a master passphrase")
	if err != nil {
		t.Fatalf("key: %v", err)
	}
	secret := "sk-ant-fake-portal-key-0123456789"
	sealed, err := encrypt.Encrypt(secret, key)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	if !strings.HasPrefix(sealed, encrypt.Prefix) {
		t.Fatalf("sealed value missing %q prefix: %q", encrypt.Prefix, sealed)
	}
	if strings.Contains(sealed, secret) {
		t.Fatal("ciphertext contains the plaintext secret")
	}
	got, err := encrypt.Decrypt(sealed, key)
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	if got != secret {
		t.Fatalf("decrypt = %q, want %q", got, secret)
	}
}

func TestWrongKeyFails(t *testing.T) {
	k1, _ := encrypt.Key("right")
	k2, _ := encrypt.Key("wrong")
	sealed, _ := encrypt.Encrypt("secret", k1)
	if _, err := encrypt.Decrypt(sealed, k2); err == nil {
		t.Fatal("decrypt with wrong key succeeded, want authentication failure")
	}
}

func TestPlaintextPassthrough(t *testing.T) {
	key, _ := encrypt.Key("k")
	// A value without the enc: prefix is returned unchanged (dev bring-up).
	got, err := encrypt.Decrypt("sk-ant-raw-key", key)
	if err != nil {
		t.Fatalf("decrypt passthrough: %v", err)
	}
	if got != "sk-ant-raw-key" {
		t.Fatalf("passthrough = %q, want the raw value", got)
	}
}

func TestEmpty(t *testing.T) {
	key, _ := encrypt.Key("k")
	if s, _ := encrypt.Encrypt("", key); s != "" {
		t.Fatalf("encrypt empty = %q, want empty", s)
	}
	if s, _ := encrypt.Decrypt("", key); s != "" {
		t.Fatalf("decrypt empty = %q, want empty", s)
	}
}

func TestKeyRequiresPassphrase(t *testing.T) {
	if _, err := encrypt.Key(""); err == nil {
		t.Fatal("Key(\"\") succeeded, want error")
	}
}

func TestResolvePlaintext(t *testing.T) {
	// No enc: prefix → returned as-is without needing the env key.
	got, err := encrypt.Resolve("plain-value")
	if err != nil {
		t.Fatalf("resolve plaintext: %v", err)
	}
	if got != "plain-value" {
		t.Fatalf("resolve = %q, want plain-value", got)
	}
}

func TestResolveEncryptedNeedsKey(t *testing.T) {
	t.Setenv(encrypt.KeyEnv, "")
	if _, err := encrypt.Resolve(encrypt.Prefix + "deadbeef"); err == nil {
		t.Fatal("resolve of enc: value with no key succeeded, want error")
	}
}

func TestResolveEncryptedWithKey(t *testing.T) {
	t.Setenv(encrypt.KeyEnv, "the-master")
	key, _ := encrypt.Key("the-master")
	sealed, _ := encrypt.Encrypt("hello", key)
	got, err := encrypt.Resolve(sealed)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got != "hello" {
		t.Fatalf("resolve = %q, want hello", got)
	}
}
