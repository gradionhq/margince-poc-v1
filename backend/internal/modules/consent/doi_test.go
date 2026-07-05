// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package consent

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
)

// The token is a bearer secret carried in a confirmation URL: it must
// be high-entropy, URL-safe, and only its hash may ever be compared.
func TestDOITokenShapeAndHashing(t *testing.T) {
	a, err := newDOIToken()
	if err != nil {
		t.Fatal(err)
	}
	b, err := newDOIToken()
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Fatal("two mints yielded the same token")
	}
	for _, token := range []string{a, b} {
		if !strings.HasPrefix(token, "doi_") {
			t.Errorf("token %q lacks the doi_ prefix that names its kind in logs", token)
		}
		// 32 bytes of entropy → 43 chars of raw-URL base64 after the prefix.
		if got := len(token); got != len("doi_")+43 {
			t.Errorf("token length = %d, want %d", got, len("doi_")+43)
		}
		if strings.ContainsAny(token, "+/=") {
			t.Errorf("token %q is not URL-safe", token)
		}
	}

	sum := sha256.Sum256([]byte(a))
	if hashDOIToken(a) != hex.EncodeToString(sum[:]) {
		t.Error("hashDOIToken must be plain sha256-hex — the stored value the verifier recomputes")
	}
	if hashDOIToken(a) == hashDOIToken(b) {
		t.Error("distinct tokens hashed identically")
	}
}
