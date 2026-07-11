// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package deals

import (
	"slices"
	"testing"
	"time"

	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

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
			wantArgs:    []any{partnerOrg},
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
			wantClauses: []string{"archived_at IS NULL", "partner_org_id IS NULL"},
			wantArgs:    []any{},
		},
		{
			name:        "both partner filters compose",
			in:          ListDealsInput{PartnerOrgID: &partnerOrg, PartnerSourced: &sourced},
			wantClauses: []string{"archived_at IS NULL", "partner_org_id = $1", "partner_org_id IS NOT NULL"},
			wantArgs:    []any{partnerOrg},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var args []any
			arg := func(v any) int { args = append(args, v); return len(args) }
			got := appendDealFilters(nil, tc.in, arg)
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
	got := appendDealFilters(nil, ListDealsInput{PartnerOrgID: &partnerOrg}, arg)
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
