// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package webhooks

import (
	"encoding/base64"
	"reflect"
	"strings"
	"testing"
)

// TestSignStandardWebhooks pins Sign against the Standard Webhooks spec's
// own published example (standardwebhooks.com / the standard-webhooks repo
// README) — NOT a value recomputed with this package's code, which would be
// circular and hide an algorithm bug. Independently verified with a
// from-scratch Python HMAC before being pinned here.
func TestSignStandardWebhooks(t *testing.T) {
	// SW reference vector (standard-webhooks spec example): whsec_ + STANDARD base64.
	secret := "whsec_MfKQ9r8GKYqrTwjUPD8ILPZIo2LaLaSw"
	id := "msg_p5jXN8AQM9LWM0D4loKWxJek"
	ts := int64(1614265330)
	body := []byte(`{"test": 2432232314}`)
	sig, err := Sign(secret, id, ts, body)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Hardcoded expected — the SW spec's published signature for this vector:
	if !reflect.DeepEqual(sig, "v1,g0hM9SsE+OTPJTGt/tmIKtSyZlE3uFJELVlNIOLJ1OE=") {
		t.Errorf("got %v, want %v", sig, "v1,g0hM9SsE+OTPJTGt/tmIKtSyZlE3uFJELVlNIOLJ1OE=")
	}
}

// TestSignIsSecretSensitive proves two distinct secrets never collide.
func TestSignIsSecretSensitive(t *testing.T) {
	id := "msg_test"
	ts := int64(1700000000)
	body := []byte("payload")
	a, err := Sign("whsec_"+encodeTestKey("secret-a-values"), id, ts, body)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	b, err := Sign("whsec_"+encodeTestKey("secret-b-values"), id, ts, body)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if reflect.DeepEqual(b, a) {
		t.Errorf("got %v, want a value different from %v", b, a)
	}
}

// TestSignIsContentSensitive proves the id, timestamp and body each
// contribute to the signature — none can be substituted without detection.
func TestSignIsContentSensitive(t *testing.T) {
	secret := "whsec_" + encodeTestKey("a-fixed-signing-key-value")
	base, err := Sign(secret, "msg_1", 1700000000, []byte("body-a"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	diffID, err := Sign(secret, "msg_2", 1700000000, []byte("body-a"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if reflect.DeepEqual(diffID, base) {
		t.Errorf("changing the delivery id must change the signature: got %v, want a value different from %v", diffID, base)
	}

	diffTS, err := Sign(secret, "msg_1", 1700000001, []byte("body-a"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if reflect.DeepEqual(diffTS, base) {
		t.Errorf("changing the timestamp must change the signature: got %v, want a value different from %v", diffTS, base)
	}

	diffBody, err := Sign(secret, "msg_1", 1700000000, []byte("body-b"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if reflect.DeepEqual(diffBody, base) {
		t.Errorf("changing the body must change the signature: got %v, want a value different from %v", diffBody, base)
	}
}

// TestSignRejectsUndecodableSecret proves a corrupt (non-base64) secret is a
// real, surfaced error — never swallowed, never silently signed with the
// raw prefixed string as key material.
func TestSignRejectsUndecodableSecret(t *testing.T) {
	_, err := Sign("whsec_not-valid-base64!!!", "msg_1", 1700000000, []byte("body"))
	if err == nil {
		t.Fatal("expected an error, got nil")
	}
}

func TestGenerateSecretIsPrefixedAndUnique(t *testing.T) {
	a, err := generateSecret()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	b, err := generateSecret()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.HasPrefix(a, secretPrefix) {
		t.Errorf("secret %q lacks the %q prefix", a, secretPrefix)
	}
	if reflect.DeepEqual(b, a) {
		t.Errorf("generateSecret returned the same secret twice: got %v, want a value different from %v", b, a)
	}

	// The mint→decode round-trip (SW compatibility, A-1): the part after
	// whsec_ must be STANDARD base64, decodable straight into HMAC key bytes.
	_, err = Sign(a, "msg_roundtrip", 1700000000, []byte("body"))
	if err != nil {
		t.Fatalf("a freshly minted secret must decode and sign without error: %v", err)
	}
}

// encodeTestKey turns an arbitrary test string into valid standard base64
// so callers can build ad-hoc whsec_ secrets without hardcoding base64
// literals inline at every call site.
func encodeTestKey(s string) string {
	return base64.StdEncoding.EncodeToString([]byte(s))
}
