// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package webhooks

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
)

// The signed-delivery wire contract (B-E10.13b, data-model §12.5). A
// receiver verifies HeaderSignature against the raw body using the
// subscription's shared secret; HeaderEvent/HeaderDelivery let it dedupe
// and correlate.
const (
	HeaderEvent     = "X-Margince-Event"
	HeaderDelivery  = "X-Margince-Delivery"
	HeaderSignature = "X-Margince-Signature"
	// signaturePrefix names the scheme so a future rotation to another MAC
	// is distinguishable on the wire rather than an ambiguous hex blob.
	signaturePrefix = "sha256="
	// secretPrefix marks the plaintext secret so a leaked string is
	// identifiable, mirroring the passport token convention.
	secretPrefix = "whsec_"
)

// generateSecret mints a fresh per-subscription signing secret. It is
// returned to the caller exactly once and sealed for storage; the HMAC at
// delivery time is computed over the raw body with this value.
func generateSecret() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("webhooks: generating signing secret: %w", err)
	}
	return secretPrefix + base64.RawURLEncoding.EncodeToString(buf), nil
}

// Sign computes the X-Margince-Signature value: HMAC-SHA256 of the raw
// body under secret, hex-encoded, scheme-prefixed.
func Sign(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return signaturePrefix + hex.EncodeToString(mac.Sum(nil))
}
