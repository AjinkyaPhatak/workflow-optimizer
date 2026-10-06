package encryption_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"strings"
	"testing"

	"workflow-optimizer/internal/infrastructure/encryption"
)

func newKey(t *testing.T) []byte {
	t.Helper()
	key := make([]byte, encryption.KeySize)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	return key
}

func newAESGCM(t *testing.T, key []byte) *encryption.AESGCM {
	t.Helper()
	a, err := encryption.NewAESGCM(key)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func TestRoundTripAndFreshNonces(t *testing.T) {
	ctx := context.Background()
	a := newAESGCM(t, newKey(t))
	secret := []byte("sk-test-1234567890")
	c1, err := a.Encrypt(ctx, secret)
	if err != nil {
		t.Fatal(err)
	}
	c2, _ := a.Encrypt(ctx, secret)
	if bytes.Equal(c1, c2) {
		t.Fatal("two encryptions of the same plaintext are identical (nonce reuse)")
	}
	if bytes.Contains(c1, secret) {
		t.Fatal("ciphertext contains the plaintext")
	}
	for _, c := range [][]byte{c1, c2} {
		got, err := a.Decrypt(ctx, c)
		if err != nil || !bytes.Equal(got, secret) {
			t.Fatalf("round trip = %q, %v", got, err)
		}
	}
}

func TestTamperingAndWrongKeyFail(t *testing.T) {
	ctx := context.Background()
	key := newKey(t)
	a := newAESGCM(t, key)
	c, _ := a.Encrypt(ctx, []byte("sk-secret"))
	for i := range c {
		tampered := append([]byte(nil), c...)
		tampered[i] ^= 0x01
		if _, err := a.Decrypt(ctx, tampered); !errors.Is(err, encryption.ErrDecrypt) {
			t.Fatalf("byte %d flipped: err = %v", i, err)
		}
	}
	if _, err := a.Decrypt(ctx, c[:len(c)-1]); !errors.Is(err, encryption.ErrDecrypt) {
		t.Fatalf("truncated: %v", err)
	}
	if _, err := a.Decrypt(ctx, nil); !errors.Is(err, encryption.ErrDecrypt) {
		t.Fatalf("empty: %v", err)
	}
	other := newAESGCM(t, newKey(t))
	_, err := other.Decrypt(ctx, c)
	if !errors.Is(err, encryption.ErrDecrypt) {
		t.Fatalf("wrong key: %v", err)
	}
	if strings.Contains(err.Error(), base64.StdEncoding.EncodeToString(c)) || strings.Contains(err.Error(), string(c)) {
		t.Fatal("decryption error exposes the ciphertext")
	}
}

func TestKeyValidation(t *testing.T) {
	if _, err := encryption.NewAESGCM(make([]byte, 16)); !errors.Is(err, encryption.ErrInvalidKey) {
		t.Fatalf("16-byte key: %v", err)
	}
	key := newKey(t)
	for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.RawURLEncoding} {
		got, err := encryption.ParseKey(" " + enc.EncodeToString(key) + "\n")
		if err != nil || !bytes.Equal(got, key) {
			t.Fatalf("ParseKey: %v", err)
		}
	}
	if _, err := encryption.ParseKey(base64.StdEncoding.EncodeToString(key[:31])); !errors.Is(err, encryption.ErrInvalidKey) {
		t.Fatalf("short key: %v", err)
	}
	if _, err := encryption.ParseKey("not base64 !!"); !errors.Is(err, encryption.ErrInvalidKey) {
		t.Fatalf("garbage key: %v", err)
	}
}
