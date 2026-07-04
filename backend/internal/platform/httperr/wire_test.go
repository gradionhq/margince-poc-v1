// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package httperr

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func decodeInto(t *testing.T, body string) (ok bool, status int) {
	t.Helper()
	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/v1/things", strings.NewReader(body))
	var into map[string]any
	ok = Decode(w, r, &into)
	return ok, w.Code
}

func TestDecodeAcceptsExactlyOneJSONValue(t *testing.T) {
	if ok, _ := decodeInto(t, `{"a":1}`); !ok {
		t.Fatal("plain object refused")
	}
	// Trailing tokens are malformed, not silently ignored — two values
	// in one body is an ambiguous payload.
	if ok, status := decodeInto(t, `{"a":1}{"b":2}`); ok || status != 422 {
		t.Fatalf("trailing JSON accepted (ok=%v status=%d)", ok, status)
	}
	if ok, status := decodeInto(t, `{`); ok || status != 422 {
		t.Fatalf("malformed JSON accepted (ok=%v status=%d)", ok, status)
	}
}

func TestDecodeCapsTheBody(t *testing.T) {
	oversized := `{"pad":"` + strings.Repeat("x", MaxBodyBytes) + `"}`
	ok, status := decodeInto(t, oversized)
	if ok || status != 413 {
		t.Fatalf("oversized body → ok=%v status=%d, want refusal 413", ok, status)
	}
}
