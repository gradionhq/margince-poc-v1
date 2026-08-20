// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package people

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

func edgeGrantCtx(objects map[string]principal.ObjectGrant) context.Context {
	return principal.WithActor(context.Background(), principal.Principal{
		Type: principal.PrincipalHuman, ID: "human:test", UserID: ids.NewV7(),
		Permissions: principal.Permissions{
			RoleKeys: []string{"rep"}, Objects: objects, RowScope: principal.RowScopeAll,
		},
	})
}

// Filtering the people list by employer answers "who works at Acme" one page at
// a time, which is a stronger disclosure than the count on the account itself —
// a listing beats a count. So the FILTER is refused rather than answered with
// an empty page: an empty page would answer the question with "nobody", which
// is false.
func TestTheEmployerFilterIsRefusedWithoutTheEdgeGrant(t *testing.T) {
	ctx := edgeGrantCtx(map[string]principal.ObjectGrant{
		"person": {Read: true}, "organization": {Read: true},
	})
	orgID := ids.From[ids.OrganizationKind](ids.NewV7())
	clause, err := personEmployerClause(ctx, &orgID, func(any) int { return 1 })
	if !errors.Is(err, apperrors.ErrPermissionDenied) {
		t.Errorf("personEmployerClause(no edge grant) = %v, want ErrPermissionDenied", err)
	}
	if clause != "" {
		t.Errorf("a refused caller was handed a filter to run: %q", clause)
	}
}

// No filter asked for, no gate to fail: a caller who never names an employer is
// not asking about the pairs at all, and must not be refused the people list
// for want of a grant the request does not use.
func TestTheEmployerFilterAsksNoGateWhenNoEmployerIsNamed(t *testing.T) {
	ctx := edgeGrantCtx(map[string]principal.ObjectGrant{"person": {Read: true}})
	clause, err := personEmployerClause(ctx, nil, func(any) int { return 1 })
	if err != nil {
		t.Errorf("personEmployerClause(no employer named) = %v, want no gate at all", err)
	}
	if clause != "" {
		t.Errorf("an unfiltered list got a filter clause: %q", clause)
	}
}

// The positive control, which also pins that the edge bound reaches the SQL:
// without it the filter would select employment rows whose endpoints this
// caller cannot see.
func TestTheEmployerFilterBoundsTheEdgeWithTheGrant(t *testing.T) {
	ctx := edgeGrantCtx(map[string]principal.ObjectGrant{
		"person": {Read: true}, "relationship": {Read: true},
	})
	orgID := ids.From[ids.OrganizationKind](ids.NewV7())
	var args []any
	clause, err := personEmployerClause(ctx, &orgID, func(v any) int {
		args = append(args, v)
		return len(args)
	})
	if err != nil {
		t.Fatalf("personEmployerClause(edge grant) = %v, want a filter", err)
	}
	if !strings.Contains(clause, "FROM relationship rel") {
		t.Errorf("the filter does not read the edge table: %s", clause)
	}
	if !strings.Contains(clause, "rel.organization_id") {
		t.Errorf("the filter does not pin the employer: %s", clause)
	}
}

// grantVisible is the object half the contact count asks. The count is a fact
// about the employment PAIRS, so a caller refused the edge gets no count —
// absent, which the field's contract description already specifies for the
// person grant, rather than a zero that would be a wrong number on screen.
func TestTheContactCountNeedsBothThePersonAndTheEdgeGrant(t *testing.T) {
	cases := map[string]struct {
		objects map[string]principal.ObjectGrant
		want    bool
	}{
		"person and edge": {map[string]principal.ObjectGrant{
			"person": {Read: true}, "relationship": {Read: true},
		}, true},
		"person only": {map[string]principal.ObjectGrant{"person": {Read: true}}, false},
		"edge only":   {map[string]principal.ObjectGrant{"relationship": {Read: true}}, false},
		"neither":     {map[string]principal.ObjectGrant{"organization": {Read: true}}, false},
	}
	for name, tc := range cases {
		ctx := edgeGrantCtx(tc.objects)
		got := grantVisible(ctx, "person") && grantVisible(ctx, "relationship")
		if got != tc.want {
			t.Errorf("%s: contact count visible = %v, want %v", name, got, tc.want)
		}
	}
}

// The account roster is drawn from employment edges, so a caller refused the
// edge is refused the roster — and org360 names `people` in sections_omitted
// rather than drawing an account with nobody at it.
//
// The nil transaction is the assertion, not an oversight: the gate is resolved
// before the statement, so a refused read never reaches a database. A version
// that filtered rows after the read would panic here.
func TestTheOrgRosterIsRefusedBeforeItReachesAStatement(t *testing.T) {
	ctx := edgeGrantCtx(map[string]principal.ObjectGrant{"person": {Read: true}})
	if _, err := StrengthForOrgContacts(ctx, nil, ids.From[ids.OrganizationKind](ids.NewV7()),
		time.Now().UTC()); !errors.Is(err, apperrors.ErrPermissionDenied) {
		t.Errorf("StrengthForOrgContacts(no edge grant) = %v, want ErrPermissionDenied", err)
	}
}

// A withheld edge is not a dormant account. AccountStrengthFor swallows a
// PERSON denial into a dormant roll-up — true, over the empty set of contacts
// that caller may read — but must REFUSE an edge denial: a caller who cannot
// tell who works there has an unknown roll-up, and answering "none" would state
// as a fact about the account something they were refused the means to compute.
func TestAWithheldEdgeIsRefusedRatherThanReportedAsDormant(t *testing.T) {
	ctx := edgeGrantCtx(map[string]principal.ObjectGrant{"person": {Read: true}})
	got, err := AccountStrengthFor(ctx, nil, ids.From[ids.OrganizationKind](ids.NewV7()),
		time.Now().UTC())
	if !errors.Is(err, apperrors.ErrPermissionDenied) {
		t.Errorf("AccountStrengthFor(no edge grant) = (%+v, %v), want ErrPermissionDenied — a dormant "+
			"verdict here is a wrong answer, not a withheld one", got.RelationshipStrength, err)
	}
}
