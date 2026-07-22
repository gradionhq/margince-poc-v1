// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package webhooks

import (
	"encoding/base64"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
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
	require.NoError(t, err)
	// Hardcoded expected — the SW spec's published signature for this vector:
	require.Equal(t, "v1,g0hM9SsE+OTPJTGt/tmIKtSyZlE3uFJELVlNIOLJ1OE=", sig)
}

// TestSignIsSecretSensitive proves two distinct secrets never collide.
func TestSignIsSecretSensitive(t *testing.T) {
	id := "msg_test"
	ts := int64(1700000000)
	body := []byte("payload")
	a, err := Sign("whsec_"+encodeTestKey("secret-a-values"), id, ts, body)
	require.NoError(t, err)
	b, err := Sign("whsec_"+encodeTestKey("secret-b-values"), id, ts, body)
	require.NoError(t, err)
	require.NotEqual(t, a, b)
}

// TestSignIsContentSensitive proves the id, timestamp and body each
// contribute to the signature — none can be substituted without detection.
func TestSignIsContentSensitive(t *testing.T) {
	secret := "whsec_" + encodeTestKey("a-fixed-signing-key-value")
	base, err := Sign(secret, "msg_1", 1700000000, []byte("body-a"))
	require.NoError(t, err)

	diffID, err := Sign(secret, "msg_2", 1700000000, []byte("body-a"))
	require.NoError(t, err)
	require.NotEqual(t, base, diffID, "changing the delivery id must change the signature")

	diffTS, err := Sign(secret, "msg_1", 1700000001, []byte("body-a"))
	require.NoError(t, err)
	require.NotEqual(t, base, diffTS, "changing the timestamp must change the signature")

	diffBody, err := Sign(secret, "msg_1", 1700000000, []byte("body-b"))
	require.NoError(t, err)
	require.NotEqual(t, base, diffBody, "changing the body must change the signature")
}

// TestSignRejectsUndecodableSecret proves a corrupt (non-base64) secret is a
// real, surfaced error — never swallowed, never silently signed with the
// raw prefixed string as key material.
func TestSignRejectsUndecodableSecret(t *testing.T) {
	_, err := Sign("whsec_not-valid-base64!!!", "msg_1", 1700000000, []byte("body"))
	require.Error(t, err)
}

func TestGenerateSecretIsPrefixedAndUnique(t *testing.T) {
	a, err := generateSecret()
	require.NoError(t, err)
	b, err := generateSecret()
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(a, secretPrefix), "secret %q lacks the %q prefix", a, secretPrefix)
	require.NotEqual(t, a, b, "generateSecret returned the same secret twice")

	// The mint→decode round-trip (SW compatibility, A-1): the part after
	// whsec_ must be STANDARD base64, decodable straight into HMAC key bytes.
	_, err = Sign(a, "msg_roundtrip", 1700000000, []byte("body"))
	require.NoError(t, err, "a freshly minted secret must decode and sign without error")
}

// encodeTestKey turns an arbitrary test string into valid standard base64
// so callers can build ad-hoc whsec_ secrets without hardcoding base64
// literals inline at every call site.
func encodeTestKey(s string) string {
	return base64.StdEncoding.EncodeToString([]byte(s))
}
