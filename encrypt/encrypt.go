// Package encrypt is a minimal symmetric secret-at-rest helper for solution
// programs that hold their OWN credentials — e.g. the standalone portal's LLM
// API key. It is "a little safe": not plaintext on disk, decrypted at startup
// with a master passphrase taken from the environment.
//
// Scheme: AES-256-GCM (authenticated — a tampered or wrong-key ciphertext fails
// to open, unlike v4's AES-CFB credential store) with a 32-byte key derived from
// the passphrase via SHA-256. The at-rest form is "enc:<base64(nonce|sealed)>";
// a value WITHOUT the enc: prefix is treated as plaintext (passthrough), so a
// dev can paste a raw key during bring-up and encrypt it later with no code
// change.
//
// v0 — the scheme may change; treat stored ciphertext as disposable across SDK
// versions. This is the SDK home for "a consultant's program keeps its own
// secret a little safer than plaintext", not a managed credential vault.
package encrypt

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"io"
	"os"
	"strings"
)

// Prefix marks an encrypted value. A value carrying it is GCM-sealed; a value
// without it is plaintext (Decrypt/Resolve return it unchanged).
const Prefix = "enc:"

// KeyEnv is the environment variable Resolve reads the master passphrase from.
const KeyEnv = "SOLID_SECRET_KEY"

// Key derives a 32-byte AES-256 key from a passphrase via SHA-256. An empty
// passphrase is refused — there is no "encrypt with no secret".
func Key(passphrase string) ([]byte, error) {
	if passphrase == "" {
		return nil, fmt.Errorf("encrypt: empty passphrase (set %s)", KeyEnv)
	}
	sum := sha256.Sum256([]byte(passphrase))
	return sum[:], nil
}

// Encrypt seals plaintext under key and returns "enc:<base64(nonce|sealed)>".
// Empty plaintext returns "" (nothing to protect). key must be 32 bytes (use
// Key to derive one).
func Encrypt(plaintext string, key []byte) (string, error) {
	if plaintext == "" {
		return "", nil
	}
	gcm, err := newGCM(key)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", fmt.Errorf("encrypt: nonce: %w", err)
	}
	sealed := gcm.Seal(nonce, nonce, []byte(plaintext), nil)
	return Prefix + base64.StdEncoding.EncodeToString(sealed), nil
}

// Decrypt opens a value Encrypt produced. A value WITHOUT Prefix is returned as
// is (plaintext passthrough). A tampered or wrong-key ciphertext returns an
// error (GCM authentication), never silent garbage.
func Decrypt(value string, key []byte) (string, error) {
	if value == "" {
		return "", nil
	}
	if !strings.HasPrefix(value, Prefix) {
		return value, nil // plaintext passthrough
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(value, Prefix))
	if err != nil {
		return "", fmt.Errorf("decrypt: base64: %w", err)
	}
	gcm, err := newGCM(key)
	if err != nil {
		return "", err
	}
	if len(raw) < gcm.NonceSize() {
		return "", fmt.Errorf("decrypt: ciphertext too short")
	}
	nonce, sealed := raw[:gcm.NonceSize()], raw[gcm.NonceSize():]
	plain, err := gcm.Open(nil, nonce, sealed, nil)
	if err != nil {
		return "", fmt.Errorf("decrypt: open (wrong key or tampered): %w", err)
	}
	return string(plain), nil
}

// Resolve is the one-call convenience a solution program uses to load a secret:
// if value is enc:-prefixed, decrypt it with the passphrase from KeyEnv;
// otherwise return it unchanged. So a deploy may set a secret as plaintext (dev)
// or as enc: ciphertext (a-little-safe) and the program reads it the same way.
// An enc: value with KeyEnv unset is an error (a secret was meant to be sealed).
func Resolve(value string) (string, error) {
	if !strings.HasPrefix(value, Prefix) {
		return value, nil
	}
	key, err := Key(os.Getenv(KeyEnv))
	if err != nil {
		return "", fmt.Errorf("resolve: %w", err)
	}
	return Decrypt(value, key)
}

func newGCM(key []byte) (cipher.AEAD, error) {
	if len(key) != 32 {
		return nil, fmt.Errorf("encrypt: key must be 32 bytes, got %d (use encrypt.Key)", len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("encrypt: cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("encrypt: gcm: %w", err)
	}
	return gcm, nil
}
