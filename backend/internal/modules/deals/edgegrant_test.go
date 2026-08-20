// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package deals

// A seat on a deal is a `relationship` edge, so reading one needs the edge's own
// grant on top of the deal's. The deal grant answers "may I see this deal"; the
// edge grant answers "may I learn who is on it", and the second does not follow
// from the first.
//
// Every test here passes a NIL TRANSACTION, and that is the assertion rather
// than a convenience. These reads carry their bound in an interpolated clause,
// so a version that queried first and filtered after would look correct to any
// test that inspected the returned rows — and would have read them. With no
// transaction to query, only a refusal resolved BEFORE the statement can return
// an error instead of panicking.

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// dealReaderWithoutTheEdgeGrant is the caller this file is about: every grant
// the coverage and health surfaces ask for EXCEPT the edge. It is the shape an
// operator produces by restricting relationship access on a role that still
// works deals, and the reason these reads cannot be admitted by their
// neighbours' grants.
func dealReaderWithoutTheEdgeGrant() context.Context {
	return principal.WithActor(context.Background(), principal.Principal{
		Type: principal.PrincipalHuman, ID: "human:test", UserID: ids.NewV7(),
		Permissions: principal.Permissions{
			RoleKeys: []string{"rep"},
			Objects: map[string]principal.ObjectGrant{
				"deal":   {Read: true},
				"person": {Read: true},
			},
			RowScope: principal.RowScopeAll,
		},
	})
}

func TestTheEngagedSeatReadRefusesBeforeItReachesAStatement(t *testing.T) {
	_, err := EngagedStakeholders(dealReaderWithoutTheEdgeGrant(), nil,
		ids.From[ids.DealKind](ids.NewV7()), time.Now().UTC())
	if !errors.Is(err, apperrors.ErrPermissionDenied) {
		t.Errorf("EngagedStakeholders(no edge grant) = %v, want ErrPermissionDenied", err)
	}
}

func TestTheSeatListRefusesBeforeItReachesAStatement(t *testing.T) {
	_, err := Stakeholders(dealReaderWithoutTheEdgeGrant(), nil,
		ids.From[ids.DealKind](ids.NewV7()), time.Now().UTC())
	if !errors.Is(err, apperrors.ErrPermissionDenied) {
		t.Errorf("Stakeholders(no edge grant) = %v, want ErrPermissionDenied", err)
	}
}

// The health composite's engagement factor is a COUNT of edges over a norm, so
// a caller refused the edge must get no score rather than a lower one. This is
// the assertion that the refusal propagates instead of being absorbed into a
// zero: a health score of 0.72 that would have been 0.87 is a wrong number on
// screen, and nothing downstream can tell it from a real one.
func TestTheHealthEvidenceReadRefusesRatherThanScoringWithoutTheEdge(t *testing.T) {
	in := dealHealthInputs{dealID: ids.From[ids.DealKind](ids.NewV7())}
	err := healthActivityEvidence(dealReaderWithoutTheEdgeGrant(), nil, time.Now().UTC(), &in)
	if !errors.Is(err, apperrors.ErrPermissionDenied) {
		t.Errorf("healthActivityEvidence(no edge grant) = %v, want ErrPermissionDenied", err)
	}
	if in.engagedStakeholderIDs != nil {
		t.Errorf("the refused read still filled the engagement factor with %v — a factor computed "+
			"from a withheld input yields a wrong score, which is worse than an absent one",
			in.engagedStakeholderIDs)
	}
}
