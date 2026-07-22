// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package webhooks

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"
)

// The signed-delivery wire contract (B-E10.13b, data-model §12.5), on the
// Standard Webhooks scheme (standardwebhooks.com — the convention shared by
// Anthropic/OpenAI/Stripe/Svix). A receiver verifies HeaderWebhookSignature
// against "{webhook-id}.{webhook-timestamp}.{body}" using the subscription's
// shared secret; HeaderWebhookID (stable across retries) and
// HeaderWebhookTimestamp (fresh per attempt, replay defense) are the signed
// inputs; HeaderEvent is a non-authoritative convenience header.
const (
	HeaderEvent            = "X-Margince-Event"
	HeaderWebhookID        = "webhook-id"
	HeaderWebhookTimestamp = "webhook-timestamp"
	HeaderWebhookSignature = "webhook-signature"
	// signatureVersion names the scheme so a future rotation to another MAC
	// is distinguishable on the wire rather than an ambiguous base64 blob.
	// A single v1 entry is a valid Standard Webhooks signature list of
	// length one; multi-secret rotation grace is deferred (A4).
	signatureVersion = "v1"
	// secretPrefix marks the plaintext secret so a leaked string is
	// identifiable, mirroring the passport token convention.
	secretPrefix = "whsec_"
)

// generateSecret mints a fresh per-subscription signing secret. It is
// returned to the caller exactly once and sealed for storage. The part
// after secretPrefix is STANDARD base64 (Standard Webhooks compatibility —
// off-the-shelf SW verifiers and Sign's own decode step both expect
// base64.StdEncoding, not the URL-safe alphabet).
func generateSecret() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("webhooks: generating signing secret: %w", err)
	}
	return secretPrefix + base64.StdEncoding.EncodeToString(buf), nil
}

// Sign computes the Standard Webhooks `webhook-signature` value for one
// delivery attempt: HMAC-SHA256 over "{msgID}.{ts}.{body}", keyed by the
// secret's decoded bytes (NOT its whsec_-prefixed string form — Standard
// Webhooks keys the MAC with the raw bytes base64 encodes), rendered as
// "v1,<base64>". msgID is the delivery id (stable across retries so a
// receiver can dedupe); ts must be a fresh unix-seconds timestamp on every
// attempt (replay defense — a captured signature cannot be replayed against
// a receiver enforcing a timestamp tolerance window).
func Sign(secret string, msgID string, ts int64, body []byte) (string, error) {
	key, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(secret, secretPrefix))
	if err != nil {
		return "", fmt.Errorf("webhooks: decoding signing secret: %w", err)
	}
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(msgID))
	mac.Write([]byte("."))
	mac.Write([]byte(strconv.FormatInt(ts, 10)))
	mac.Write([]byte("."))
	mac.Write(body)
	return signatureVersion + "," + base64.StdEncoding.EncodeToString(mac.Sum(nil)), nil
}
