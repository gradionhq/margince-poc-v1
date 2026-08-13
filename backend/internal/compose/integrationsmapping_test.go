// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gradionhq/margince/backend/internal/platform/httperr"
)

// The provider-connection PATCH reads its precondition through
// httperr.IfMatchVersion, the one place that decides what an If-Match may say
// (data-model §1.3a: a bare integer, never a quoted ETag). This test exists
// because the first two attempts at this surface got it wrong in both
// directions: one accepted `If-Match: 0` and silently promoted a conditional
// write to an unconditional one, and the next accepted `"7"`, codifying a
// quoted form the contract does not permit.
//
// A conditional write that is silently made unconditional applies exactly the
// overwrite the caller was guarding against, so every value that cannot name a
// real row has to be refused rather than parsed into the zero that means
// "no precondition".
func TestIfMatchRefusesEveryValueThatCannotNameARow(t *testing.T) {
	refused := []struct {
		raw, why string
	}{
		{"0", "no row carries version zero — the column starts at 1, so zero can only mean absent"},
		{`"7"`, "a quoted ETag is not this contract's shape (data-model §1.3a)"},
		{"-1", "no row carries a negative version"},
		{"abc", "not a version at all"},
		{"1.5", "a version is an integer"},
	}
	for _, tc := range refused {
		rec := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodPatch, "/v1/provider-connections/surfe", nil)
		r.Header.Set("If-Match", tc.raw)
		if _, ok := httperr.IfMatchVersion(rec, r); ok {
			t.Errorf("If-Match %q was accepted: %s", tc.raw, tc.why)
		}
	}

	// A real version passes through unchanged.
	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPatch, "/v1/provider-connections/surfe", nil)
	r.Header.Set("If-Match", "7")
	v, ok := httperr.IfMatchVersion(rec, r)
	if !ok || v == nil || *v != 7 {
		t.Errorf("If-Match \"7\" -> (%v, %v); a bare integer version must pass", v, ok)
	}

	// No header at all is the contract's unconditional write, and the only
	// legal way to reach the store's zero.
	rec = httptest.NewRecorder()
	r = httptest.NewRequest(http.MethodPatch, "/v1/provider-connections/surfe", nil)
	if v, ok := httperr.IfMatchVersion(rec, r); !ok || v != nil {
		t.Errorf("absent header -> (%v, %v); absent is the one legal unconditional write", v, ok)
	}
}
