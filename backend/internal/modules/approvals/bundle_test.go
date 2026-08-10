// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package approvals

// The half of a bundle decision that is settled before any row is read: who may
// ask for one at all, what a bundle id has to be, and how a member's stored
// status becomes the outcome the caller is told. The transactional half — N
// verdicts, N audit rows, the members deliberately untouched — is proven against
// a real database in bundle_integration_test.go.

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// Deciding is human work, and bundling does not make it less so. An agent that
// staged a whole bundle must not be able to release it by asking for all of it
// at once — the refusal has to come from this entry point too, not only from
// the single-approval one.
//
// The service holds a nil pool on purpose: a refusal that reached the database
// would be a refusal that arrived too late.
func TestABundleIsDecidedByAHumanOrNotAtAll(t *testing.T) {
	svc := NewService(nil)
	ctx := principal.WithActor(context.Background(), principal.Principal{
		Type: principal.PrincipalAgent, ID: "agent:staged-this-bundle",
	})
	_, err := svc.DecideBundle(ctx, ids.NewV7(), true, nil)
	if !errors.Is(err, apperrors.ErrPermissionDenied) {
		t.Fatalf("err = %v, want ErrPermissionDenied — an agent may stage a bundle, never decide one", err)
	}
}

// The zero uuid is not a bundle, and it must not become the query that matches
// every unbundled proposal in the workspace. `bundle_id IS NULL` is what an
// unbundled row carries, so a zero id reaching the WHERE clause would answer
// nothing — but reaching the DECISION as "no members" is the shape that starts
// looking like a wildcard the day the column gains a default. It reads as absent
// here, before the transaction opens.
func TestAZeroBundleIDReadsAsAbsent(t *testing.T) {
	svc := NewService(nil)
	ctx := principal.WithActor(context.Background(), principal.Principal{
		Type: principal.PrincipalHuman, ID: "human:rep", UserID: ids.NewV7(),
	})
	_, err := svc.DecideBundle(ctx, ids.Nil, true, nil)
	if !errors.Is(err, apperrors.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

// Expiry is not a verdict. A member that lapsed undecided has to read as its own
// outcome, because "already_decided" would tell a human somebody answered it —
// and the answer to an expired proposal is to propose it again, not to look for
// who rejected it.
func TestAnExpiredMemberIsNotReportedAsDecided(t *testing.T) {
	for status, want := range map[string]BundleOutcome{
		"expired":              BundleExpired,
		approvalStatusApproved: BundleAlreadyDecided,
		approvalStatusRejected: BundleAlreadyDecided,
	} {
		if got := outcomeOf(status); got != want {
			t.Errorf("outcomeOf(%q) = %s, want %s", status, got, want)
		}
	}
}

// A refusal has to say what to do next. A bundle past the cap is still
// decidable one member at a time, and a message that only said "too large"
// would leave a human with a queue they cannot clear.
func TestTheOversizedBundleRefusalNamesTheWayOut(t *testing.T) {
	err := &BundleTooLargeError{Cap: bundleDecisionCap}
	if !strings.Contains(err.Error(), "individually") {
		t.Errorf("refusal = %q, want it to name deciding the members individually", err.Error())
	}
}
