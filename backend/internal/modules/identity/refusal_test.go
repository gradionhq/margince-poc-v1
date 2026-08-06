// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package identity

// The admin surface's refusals are sentinels the handler renders into typed
// problems. Two properties hold this together and neither is visible from an
// end-to-end status assertion: the mapping fires for its OWN cause only, and an
// error that is not that cause passes through with the status it already had.

import (
	"errors"
	"net/http"
	"testing"

	"github.com/gradionhq/margince/backend/internal/platform/httperr"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
)

func TestRefusalsRenderTheirOwnCauseOnly(t *testing.T) {
	for _, c := range []struct {
		name   string
		err    error
		want   string
		status int
	}{
		{"duplicate email", errEmailTaken, "email_taken", http.StatusConflict},
		{"last active admin", errLastActiveAdmin, "last_active_admin", http.StatusConflict},
		{"suspended, not deactivated", errNotDeactivated, "not_deactivated", http.StatusConflict},
	} {
		got := conflictIf(c.err, c.err, c.want, "actionable prose")
		var refusal *httperr.DetailedError
		if !errors.As(got, &refusal) {
			t.Errorf("%s: got %v, want a rendered refusal", c.name, got)
			continue
		}
		if refusal.Code != c.want || refusal.Status != c.status {
			t.Errorf("%s: got %d/%q, want %d/%q", c.name, refusal.Status, refusal.Code, c.status, c.want)
		}
	}
}

// The pass-through is what keeps one endpoint's wording from swallowing every
// other failure it can hit: a permission denial rendered as somebody else's 409
// would be both the wrong status and a lie about what happened.
func TestRefusalLeavesAnUnrelatedErrorAlone(t *testing.T) {
	for _, other := range []error{
		apperrors.ErrPermissionDenied,
		apperrors.ErrNotFound,
		errNotDeactivated,
	} {
		if got := conflictIf(other, errLastActiveAdmin, "last_active_admin", "prose"); !errors.Is(got, other) {
			t.Errorf("conflictIf rewrote %v into %v", other, got)
		}
	}
}

// errUnknownRole wraps ErrNotFound so it keeps the 404, which makes the
// direction of the match load-bearing: a plain missing MEMBER must never be
// relabelled as a mistyped role, or the admin is sent to fix the wrong thing.
func TestUnknownRoleNeverClaimsAMissingMember(t *testing.T) {
	if !errors.Is(errUnknownRole, apperrors.ErrNotFound) {
		t.Error("errUnknownRole must stay a 404; it wraps ErrNotFound for exactly that")
	}
	// The load-bearing direction, asserted through the behaviour rather than
	// through a reversed errors.Is: a MISSING MEMBER reaches this mapping as a
	// bare ErrNotFound and must come out untouched. If the match ran the other
	// way, every missing member would be reported as a mistyped role key.
	if got := unknownRoleRefusal(apperrors.ErrNotFound); !errors.Is(got, apperrors.ErrNotFound) {
		t.Errorf("a missing member was rendered as %v, want the untouched 404", got)
	}
	var relabelled *httperr.DetailedError
	if errors.As(unknownRoleRefusal(apperrors.ErrNotFound), &relabelled) {
		t.Errorf("a missing member was relabelled %q; only a real unknown role may be", relabelled.Code)
	}

	var refusal *httperr.DetailedError
	if !errors.As(unknownRoleRefusal(errUnknownRole), &refusal) {
		t.Fatal("an unknown role key was not rendered as a refusal")
	}
	if refusal.Status != http.StatusNotFound || refusal.Code != "unknown_role" {
		t.Errorf("got %d/%q, want 404/unknown_role", refusal.Status, refusal.Code)
	}
}
