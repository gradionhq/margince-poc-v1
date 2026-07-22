// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package webhooks

import (
	"bytes"
	"testing"
)

func testKey(t *testing.T, fill byte) []byte {
	t.Helper()
	key := bytes.Repeat([]byte{fill}, WebhookKeyBytes)
	return key
}

func TestCipherSealOpenRoundTrip(t *testing.T) {
	c, err := NewCipher(testKey(t, 0x01))
	if err != nil {
		t.Fatal(err)
	}
	secret := "whsec_round-trip"
	sealed, err := c.seal(secret)
	if err != nil {
		t.Fatal(err)
	}
	if sealed == secret {
		t.Fatal("sealed value equals the plaintext — the secret is stored in the clear")
	}
	got, err := c.open(sealed)
	if err != nil {
		t.Fatal(err)
	}
	if got != secret {
		t.Fatalf("open = %q, want %q", got, secret)
	}
}

func TestCipherSealIsNondeterministic(t *testing.T) {
	c, err := NewCipher(testKey(t, 0x02))
	if err != nil {
		t.Fatal(err)
	}
	a, err := c.seal("same")
	if err != nil {
		t.Fatal(err)
	}
	b, err := c.seal("same")
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Fatal("two seals of the same secret are identical — the nonce is not fresh")
	}
}

func TestCipherWrongKeyCannotOpen(t *testing.T) {
	sealer, err := NewCipher(testKey(t, 0x03))
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := sealer.seal("whsec_secret")
	if err != nil {
		t.Fatal(err)
	}
	other, err := NewCipher(testKey(t, 0x04))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := other.open(sealed); err == nil {
		t.Fatal("a secret sealed under one key opened under another — GCM authentication is not enforced")
	}
}

func TestNewCipherRejectsWrongLengthKey(t *testing.T) {
	if _, err := NewCipher([]byte("too-short")); err == nil {
		t.Fatal("NewCipher accepted a short key; a guessable webhook signing key is a security defect")
	}
}
