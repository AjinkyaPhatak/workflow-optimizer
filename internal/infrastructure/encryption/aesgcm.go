package encryption

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"workflow-optimizer/internal/credential"
)

// KeySize is the AES-256 key length in bytes.
const KeySize = 32

var (
	// ErrInvalidKey rejects a master key that is not 32 bytes.
	ErrInvalidKey = errors.New("encryption: invalid key")
	// ErrDecrypt is the only decryption failure reported: wrong key,
	// tampered, truncated or foreign ciphertext all look the same, and the
	// error never contains the ciphertext.
	ErrDecrypt = errors.New("encryption: decryption failed")
)

// AESGCM is the credential.SecretEncryptor: AES-256-GCM from the standard
// library (authenticated encryption, no custom cryptography). Ciphertext is
// nonce (12 bytes, random per encryption) || sealed data || tag (16 bytes).
// It is safe for concurrent use.
type AESGCM struct {
	aead cipher.AEAD
}

var _ credential.SecretEncryptor = (*AESGCM)(nil)

// NewAESGCM builds the encryptor from a 32-byte master key.
func NewAESGCM(key []byte) (*AESGCM, error) {
	if len(key) != KeySize {
		return nil, fmt.Errorf("%w: need %d bytes, got %d", ErrInvalidKey, KeySize, len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidKey, err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidKey, err)
	}
	return &AESGCM{aead: aead}, nil
}

// ParseKey decodes CREDENTIAL_ENCRYPTION_KEY: 32 random bytes, base64
// (standard or URL alphabet, padded or not). Generate one with e.g.
// `openssl rand -base64 32`.
func ParseKey(encoded string) ([]byte, error) {
	encoded = strings.TrimSpace(encoded)
	for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		if key, err := enc.DecodeString(encoded); err == nil {
			if len(key) != KeySize {
				return nil, fmt.Errorf("%w: CREDENTIAL_ENCRYPTION_KEY must decode to %d bytes, got %d", ErrInvalidKey, KeySize, len(key))
			}
			return key, nil
		}
	}
	return nil, fmt.Errorf("%w: CREDENTIAL_ENCRYPTION_KEY must be base64", ErrInvalidKey)
}

// Encrypt seals plaintext under a fresh random nonce.
func (a *AESGCM) Encrypt(_ context.Context, plaintext []byte) ([]byte, error) {
	nonce := make([]byte, a.aead.NonceSize(), a.aead.NonceSize()+len(plaintext)+a.aead.Overhead())
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("encryption: generate nonce: %w", err)
	}
	return a.aead.Seal(nonce, nonce, plaintext, nil), nil
}

// Decrypt opens nonce || sealed data || tag. Any failure is ErrDecrypt.
func (a *AESGCM) Decrypt(_ context.Context, ciphertext []byte) ([]byte, error) {
	n := a.aead.NonceSize()
	if len(ciphertext) < n+a.aead.Overhead() {
		return nil, ErrDecrypt
	}
	plain, err := a.aead.Open(nil, ciphertext[:n], ciphertext[n:], nil)
	if err != nil {
		return nil, ErrDecrypt
	}
	return plain, nil
}
