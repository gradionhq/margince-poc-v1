// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/platform/httperr"
	"github.com/gradionhq/margince/backend/pkg/extension"
)

// Every published refusal class reaches the wire as the refusal it IS.
//
// The defect this pins is the one a status assertion catches and an error-text
// assertion never does: all four are the extension surface's own sentinels, so
// httperr — whose table is the core's fixed §0 registry — classified none of
// them and answered `500 internal` to a caller who had simply mistyped an
// input.
func TestAUnitsRefusalReachesTheWireAsItsOwnClass(t *testing.T) {
	for _, tc := range []struct {
		class  error
		status int
		code   string
	}{
		{extension.ErrInvalid, http.StatusUnprocessableEntity, "validation_error"},
		{extension.ErrForbidden, http.StatusForbidden, "permission_denied"},
		{extension.ErrNotFound, http.StatusNotFound, "not_found"},
		{extension.ErrConflict, http.StatusConflict, "conflict"},
	} {
		t.Run(tc.code, func(t *testing.T) {
			raised := fmt.Errorf("%w: paste the token from your profile", tc.class)
			rec := httptest.NewRecorder()
			httperr.Write(rec, httptest.NewRequest(http.MethodPost, "/v1/ext/thing", nil), unitRefusal(raised))

			if rec.Code != tc.status {
				t.Errorf("status = %d, want %d — body %s", rec.Code, tc.status, rec.Body)
			}
			if !strings.Contains(rec.Body.String(), `"code":"`+tc.code+`"`) {
				t.Errorf("body carries no %q code: %s", tc.code, rec.Body)
			}
			// The unit's own sentence is the only part of the answer that says
			// what to DO, so it has to survive the mapping.
			if !strings.Contains(rec.Body.String(), "paste the token from your profile") {
				t.Errorf("the unit's sentence did not reach the caller: %s", rec.Body)
			}
			// And it says it ONCE: the class is already on the wire as the
			// status and the code, so repeating its text in the detail would
			// be the third copy of a thing the caller can already read.
			if strings.Contains(rec.Body.String(), tc.class.Error()) {
				t.Errorf("the class text is repeated inside the detail: %s", rec.Body)
			}
		})
	}
}

// A refusal the CORE raised passes through untouched — the extension route's
// standing rule that a unit's refusal reads like the core route's beside it
// cuts both ways, and re-mapping something already in the product's vocabulary
// is how the two shapes drift apart.
func TestARefusalTheCoreRaisedIsNotRemapped(t *testing.T) {
	core := errors.New("compose: something the core said")
	if got := unitRefusal(core); !errors.Is(got, core) {
		t.Fatalf("unitRefusal rewrote a core error: %v", got)
	}
	if unitRefusal(nil) != nil {
		t.Fatal("unitRefusal invented an error out of nil")
	}
}
