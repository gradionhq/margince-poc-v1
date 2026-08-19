// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

import (
	"errors"
	"testing"

	"github.com/gradionhq/margince/backend/internal/modules/deals"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/values"
)

// A field mask withholds one column of a readable row. A rep reads every deal
// in the workspace; the amount of another team's deal is null and named in
// masked_fields, their own team's amount is theirs, and a sort by the masked
// column is refused — ordering by a value is reading it. The mask is a row
// property the admin does not carry.
func TestARepReadsEveryDealButNotAnotherTeamsAmount(t *testing.T) {
	e := Setup(t)
	pipeline, open, _ := DealFixture(t, e)
	mine := e.SeedDeal(t, "Mine", pipeline, open, &e.Rep1)
	theirs := e.SeedDeal(t, "Theirs", pipeline, open, &e.Rep3)
	amount := int64(250000)
	for _, id := range []ids.UUID{mine, theirs} {
		e.WsExec(t, `UPDATE deal SET amount_minor = $2, currency = 'EUR' WHERE id = $1`, id, amount)
	}

	perms := activityLifecyclePerms
	perms.Objects = map[string]principal.ObjectGrant{"deal": {Read: true, Update: true}, "pipeline": {Read: true}}
	perms.FieldMasks = []principal.FieldMask{{Object: "deal", Field: "amount_minor", Condition: principal.MaskOutsideWriteAuthority}}
	rep := e.As(e.Rep1, []ids.UUID{e.Team1}, perms)

	got, err := e.Deals.GetDeal(rep, ids.From[ids.DealKind](theirs), 0)
	if err != nil {
		t.Fatalf("a rep reading another team's deal: %v", err)
	}
	if got.AmountMinor != nil || got.MaskedFields == nil || len(*got.MaskedFields) != 1 || (*got.MaskedFields)[0] != "amount_minor" {
		t.Errorf("another team's deal for a rep = amount %v masked %v, want amount withheld and named", got.AmountMinor, got.MaskedFields)
	}
	own, err := e.Deals.GetDeal(rep, ids.From[ids.DealKind](mine), 0)
	if err != nil || own.AmountMinor == nil || *own.AmountMinor != amount || own.MaskedFields != nil {
		t.Errorf("the rep's own deal = amount %v masked %v (%v), want the amount and no mask", own.AmountMinor, own.MaskedFields, err)
	}

	page, _, err := e.Deals.ListDeals(rep, deals.ListDealsInput{})
	if err != nil {
		t.Fatal(err)
	}
	seen := map[ids.UUID]bool{}
	for _, d := range page {
		seen[ids.UUID(d.Id)] = true
		switch ids.UUID(d.Id) {
		case mine:
			if d.AmountMinor == nil {
				t.Error("the list withheld the rep's own amount")
			}
		case theirs:
			if d.AmountMinor != nil || d.MaskedFields == nil {
				t.Error("the list handed the rep another team's amount")
			}
		}
	}
	if !seen[mine] || !seen[theirs] {
		t.Errorf("the list shows %v, want both deals", seen)
	}
	sort := "-amount_minor"
	var refused *values.ParseError
	if _, _, err := e.Deals.ListDeals(rep, deals.ListDealsInput{Sort: &sort}); !errors.As(err, &refused) || refused.Code != "field_masked" {
		t.Errorf("sorting by a masked column → %v, want the field_masked refusal", err)
	}

	// The admin carries no mask.
	full, err := e.Deals.GetDeal(e.Admin(), ids.From[ids.DealKind](theirs), 0)
	if err != nil || full.AmountMinor == nil || full.MaskedFields != nil {
		t.Errorf("the admin's read = amount %v masked %v (%v), want the amount", full.AmountMinor, full.MaskedFields, err)
	}
}
