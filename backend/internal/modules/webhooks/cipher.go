// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package webhooks

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
)

// Cipher seals a per-subscription signing secret at rest. The data model
// names the column a "vault ref" and mandates the secret is never stored
// plaintext; without a vault in the PoC, the deployment key IS the vault:
// an AES-256-GCM envelope over the secret, keyed by MARGINCE_WEBHOOK_KEY.
// The sealed value lives in signing_secret_ref; the plaintext exists in
// the create/rotate response and, transiently, in the delivery signer.
type Cipher struct {
	aead cipher.AEAD
}

// WebhookKeyBytes is the AES-256 key length the deployment key must decode
// to — a shorter key is a boot-time configuration error, never silently
// padded.
const WebhookKeyBytes = 32

// NewCipher builds the sealer from a 32-byte key. A wrong-length key is
// rejected loudly: a webhook subscription whose secret cannot be sealed
// (or, worse, is sealed under a guessable key) is a security defect, not a
// degraded feature.
func NewCipher(key []byte) (*Cipher, error) {
	if len(key) != WebhookKeyBytes {
		return nil, fmt.Errorf("webhooks: signing key must be %d bytes, got %d", WebhookKeyBytes, len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("webhooks: building cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("webhooks: building GCM: %w", err)
	}
	return &Cipher{aead: aead}, nil
}

// DecodeKey parses the base64 deployment key configured for the process.
func DecodeKey(encoded string) ([]byte, error) {
	key, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, fmt.Errorf("webhooks: signing key is not valid base64: %w", err)
	}
	return key, nil
}

// seal returns the base64 ciphertext (nonce || GCM output) for storage.
func (c *Cipher) seal(plaintext string) (string, error) {
	nonce := make([]byte, c.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", fmt.Errorf("webhooks: generating nonce: %w", err)
	}
	sealed := c.aead.Seal(nonce, nonce, []byte(plaintext), nil)
	return base64.StdEncoding.EncodeToString(sealed), nil
}

// open reverses seal. A ciphertext that fails to open (wrong key, tamper)
// is surfaced, never treated as an empty secret — signing with an empty
// secret would ship an attacker-forgeable signature.
func (c *Cipher) open(sealed string) (string, error) {
	raw, err := base64.StdEncoding.DecodeString(sealed)
	if err != nil {
		return "", fmt.Errorf("webhooks: sealed secret is not valid base64: %w", err)
	}
	ns := c.aead.NonceSize()
	if len(raw) < ns {
		return "", errors.New("webhooks: sealed secret is truncated")
	}
	nonce, ct := raw[:ns], raw[ns:]
	plaintext, err := c.aead.Open(nil, nonce, ct, nil)
	if err != nil {
		return "", fmt.Errorf("webhooks: opening sealed secret: %w", err)
	}
	return string(plaintext), nil
}
