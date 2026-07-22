// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

import (
	"encoding/base64"
	"testing"

	"github.com/gradionhq/margince/backend/internal/modules/webhooks"
)

// TestWithWebhookKey covers the key-string → Option decode: a valid 32-byte
// base64 key yields an Option; a non-base64 or wrong-length key fails the
// boot loudly rather than leaving the surface silently at 503.
func TestWithWebhookKey(t *testing.T) {
	valid := base64.StdEncoding.EncodeToString(make([]byte, webhooks.WebhookKeyBytes))
	opt, err := WithWebhookKey(valid)
	if err != nil || opt == nil {
		t.Fatalf("WithWebhookKey(valid) opt=%v err=%v", opt, err)
	}

	if _, err := WithWebhookKey("not base64!!!"); err == nil {
		t.Fatal("WithWebhookKey must reject a non-base64 key")
	}

	shortKey := base64.StdEncoding.EncodeToString(make([]byte, 16))
	if _, err := WithWebhookKey(shortKey); err == nil {
		t.Fatal("WithWebhookKey must reject a key that is not 32 bytes")
	}
}
