// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package webhooks

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
)

func TestSignMatchesIndependentHMAC(t *testing.T) {
	secret := "whsec_test-secret" //nolint:gosec // G101: test fixture, not a real credential
	body := []byte(`{"event_id":"abc","type":"deal.created"}`)

	got := Sign(secret, body)

	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	want := "sha256=" + hex.EncodeToString(mac.Sum(nil))
	if got != want {
		t.Fatalf("Sign = %q, want %q", got, want)
	}
}

func TestSignIsSecretSensitive(t *testing.T) {
	body := []byte("payload")
	if Sign("whsec_a", body) == Sign("whsec_b", body) {
		t.Fatal("Sign produced the same signature under two different secrets")
	}
}

func TestGenerateSecretIsPrefixedAndUnique(t *testing.T) {
	a, err := generateSecret()
	if err != nil {
		t.Fatal(err)
	}
	b, err := generateSecret()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(a, secretPrefix) {
		t.Errorf("secret %q lacks the %q prefix", a, secretPrefix)
	}
	if a == b {
		t.Fatal("generateSecret returned the same secret twice")
	}
}
