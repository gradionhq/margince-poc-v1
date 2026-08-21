// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package deals

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// systemFilterCtx is the caller for whom no reference filter needs narrowing:
// the system principal reads every organization and project, so
// referenceFilterClause renders the bare equality these cases are about.
// TestAReferenceFilterIsNarrowedToTargetsTheCallerReads covers the other side.
func systemFilterCtx() context.Context {
	return principal.WithActor(context.Background(), principal.Principal{
		Type: principal.PrincipalSystem, ID: "system:test",
	})
}

// The partner attribution filters on the deals list: partner_org_id is a
// column equality match, partner_sourced is attribution PRESENCE — true
// is the partner-sourced slice (IS NOT NULL), false its direct
// complement (IS NULL) — and both compose with the other filters.
func TestAppendDealFiltersPartnerAttribution(t *testing.T) {
	partnerOrg := ids.New[ids.OrganizationKind]()
	sourced, direct := true, false

	cases := []struct {
		name        string
		in          ListDealsInput
		wantClauses []string
		wantArgs    []any
	}{
		{
			name:        "partner_org_id is an equality match",
			in:          ListDealsInput{PartnerOrgID: &partnerOrg},
			wantClauses: []string{"archived_at IS NULL", "partner_org_id = $1"},
			wantArgs:    []any{partnerOrg.UUID},
		},
		{
			name:        "partner_sourced true selects attributed deals",
			in:          ListDealsInput{PartnerSourced: &sourced},
			wantClauses: []string{"archived_at IS NULL", "partner_org_id IS NOT NULL"},
			wantArgs:    []any{},
		},
		{
			name:        "partner_sourced false selects direct deals",
			in:          ListDealsInput{PartnerSourced: &direct},
			wantClauses: []string{"archived_at IS NULL", "NOT partner_org_id IS NOT NULL"},
			wantArgs:    []any{},
		},
		{
			name:        "both partner filters compose",
			in:          ListDealsInput{PartnerOrgID: &partnerOrg, PartnerSourced: &sourced},
			wantClauses: []string{"archived_at IS NULL", "partner_org_id = $1", "partner_org_id IS NOT NULL"},
			wantArgs:    []any{partnerOrg.UUID},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var args []any
			arg := func(v any) int { args = append(args, v); return len(args) }
			got, err := appendDealFilters(systemFilterCtx(), nil, tc.in, arg)
			if err != nil {
				t.Fatalf("appendDealFilters: %v", err)
			}
			if !slices.Equal(got, tc.wantClauses) {
				t.Fatalf("clauses = %q, want %q", got, tc.wantClauses)
			}
			if len(args) != len(tc.wantArgs) {
				t.Fatalf("args = %v, want %v", args, tc.wantArgs)
			}
			for i := range args {
				if args[i] != tc.wantArgs[i] {
					t.Fatalf("arg %d = %v, want %v", i+1, args[i], tc.wantArgs[i])
				}
			}
		})
	}
}

// partner filters must not disturb the keyset cursor's placeholder
// numbering — the cursor clause (built from the validated sort, the
// composition ListDeals runs) binds AFTER the filter args it follows.
func TestAppendDealFiltersPartnerBeforeCursorKeepsPlaceholderOrder(t *testing.T) {
	partnerOrg := ids.New[ids.OrganizationKind]()
	cursor := storekit.EncodeCursor(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC), ids.NewV7())
	var args []any
	arg := func(v any) int { args = append(args, v); return len(args) }
	got, err := appendDealFilters(systemFilterCtx(), nil, ListDealsInput{PartnerOrgID: &partnerOrg}, arg)
	if err != nil {
		t.Fatalf("appendDealFilters: %v", err)
	}
	var defaultSort *storekit.ListSort
	clause, err := defaultSort.KeysetClause(cursor, arg)
	if err != nil {
		t.Fatalf("KeysetClause: %v", err)
	}
	got = append(got, clause)
	want := []string{"archived_at IS NULL", "partner_org_id = $1", "(created_at, id) < ($2, $3)"}
	if !slices.Equal(got, want) {
		t.Fatalf("clauses = %q, want %q", got, want)
	}
	if len(args) != 3 {
		t.Fatalf("expected 3 bound args (org + cursor pair), got %d: %v", len(args), args)
	}
}

// Filtering by an id is asking whether it is there. A bounded caller's filter
// on a reference therefore carries the target's own visibility predicate, so
// `?organization_id=<an org I cannot open>` cannot confirm the binding the
// projection withholds — it returns the empty page a company with no deals
// returns.
//
// A project is deliberately not among the narrowed arms: every seat reads
// every project (platform/auth tableclass.go), so its predicate renders
// nothing and there is no existence to hide. The narrowing is derived from
// that predicate rather than listed here, so a project that ever goes back to
// a scoped read picks the clause up without this test being edited.
func TestAReferenceFilterIsNarrowedToTargetsTheCallerReads(t *testing.T) {
	org := ids.New[ids.OrganizationKind]()
	project := ids.New[ids.ProjectKind]()
	rep := principal.Principal{
		Type: principal.PrincipalHuman, ID: "human:test",
		UserID: ids.NewV7(),
		Permissions: principal.Permissions{
			RoleKeys: []string{"rep"},
			Objects:  map[string]principal.ObjectGrant{"deal": {Read: true}},
			RowScope: principal.RowScopeOwn,
		},
	}
	bounded := principal.WithActor(context.Background(), rep)
	var args []any
	arg := func(v any) int { args = append(args, v); return len(args) }
	got, err := appendDealFilters(bounded, nil,
		ListDealsInput{OrganizationID: &org, ProjectID: &project, PartnerOrgID: &org}, arg)
	if err != nil {
		t.Fatalf("appendDealFilters: %v", err)
	}
	if want := "EXISTS (SELECT 1 FROM organization ref WHERE ref.id = $1"; !slices.ContainsFunc(got,
		func(c string) bool { return strings.Contains(c, want) }) {
		t.Errorf("clauses = %q, want one carrying %q — an unnarrowed filter is an existence oracle", got, want)
	}
	// The project arm is unnarrowed only for as long as a project stays
	// workspace-readable. Ask auth rather than assuming, so a project that
	// goes back to a scoped read fails HERE instead of shipping an oracle.
	projectNarrowed := slices.ContainsFunc(got, func(c string) bool {
		return strings.Contains(c, "EXISTS (SELECT 1 FROM project ref WHERE ref.id = $")
	})
	if wantNarrowed := !auth.UnboundedFor(rep, "project"); projectNarrowed != wantNarrowed {
		t.Errorf("project filter narrowed = %v, want %v — the reference filter and the row-scope "+
			"class disagree, so the filter is either an existence oracle or a needless join: %q",
			projectNarrowed, wantNarrowed, got)
	}
	// partner_org_id is the third arm and points at the same table; it must be
	// narrowed too, or the oracle simply moves one column across.
	if !slices.ContainsFunc(got, func(c string) bool {
		return strings.HasPrefix(c, filterPartnerOrgID+" = $") && strings.Contains(c, "FROM organization ref")
	}) {
		t.Errorf("clauses = %q, want the partner arm narrowed as well", got)
	}
}
