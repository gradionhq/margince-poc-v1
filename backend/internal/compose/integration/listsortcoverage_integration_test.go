// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// Every column the record lists put a sortable header on is answered by the
// store behind it. These lists used to order every page by creation time
// whatever the request asked for, so a header could be offered that changed
// nothing; each case here orders a seeded set by one of those columns and
// checks the row that should come first actually does.

import (
	"context"
	"testing"

	"github.com/gradionhq/margince/backend/internal/modules/deals"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// asCatalogueReader grants read on the catalogue objects this file needs.
// The shared fixture grants the record types its own suites reach, and
// widening that would hand every suite riding it permissions none of them
// asked for.
func asCatalogueReader(ws ids.UUID, objects ...string) context.Context {
	grants := map[string]principal.ObjectGrant{}
	for _, object := range objects {
		grants[object] = principal.ObjectGrant{Read: true}
	}
	ctx := principal.WithWorkspaceID(context.Background(), ws)
	ctx = principal.WithCorrelationID(ctx, ids.NewV7())
	return principal.WithActor(ctx, principal.Principal{
		Type: principal.PrincipalHuman, ID: "human:" + ids.NewV7().String(), UserID: ids.NewV7(),
		Permissions: principal.Permissions{Objects: grants, RowScope: principal.RowScopeAll},
	})
}

func TestProductListSortsByEveryOfferedColumn(t *testing.T) {
	e := SetupSearch(t)
	ctx := asCatalogueReader(e.WS, "product")

	// Seeded so that creation order disagrees with every sort below: a list
	// still ordering by created_at would return "Middle" first each time.
	e.Seed(t, `INSERT INTO product (id, workspace_id, name, sku, unit, unit_price_minor, currency, default_tax_rate, active, source, captured_by)
	           VALUES ($1, $2, 'Middle', 'SKU-M', 'day', 5000, 'EUR', 0, true, 'ui', 'human:x')`)
	e.Seed(t, `INSERT INTO product (id, workspace_id, name, sku, unit, unit_price_minor, currency, default_tax_rate, active, source, captured_by)
	           VALUES ($1, $2, 'Apex', 'SKU-A', 'day', 9000, 'EUR', 0, true, 'ui', 'human:x')`)
	e.Seed(t, `INSERT INTO product (id, workspace_id, name, sku, unit, unit_price_minor, currency, default_tax_rate, active, source, captured_by)
	           VALUES ($1, $2, 'Zenith', 'SKU-Z', 'day', 1000, 'EUR', 0, true, 'ui', 'human:x')`)

	store := deals.NewStore(e.Pool)
	for _, tc := range []struct {
		sort  string
		first string
	}{
		{sort: "name", first: "Apex"},
		{sort: "-name", first: "Zenith"},
		{sort: "sku", first: "SKU-A"},
		{sort: "-unit_price_minor", first: "Apex"},
	} {
		sortField := tc.sort
		one := 1
		page, _, err := store.ListProducts(ctx, deals.ListProductsInput{Sort: &sortField, Limit: &one})
		if err != nil {
			t.Fatalf("ListProducts sort=%s: %v", tc.sort, err)
		}
		if len(page) != 1 {
			t.Fatalf("ListProducts sort=%s returned %d rows, want 1", tc.sort, len(page))
		}
		got := page[0].Name
		if tc.sort == "sku" {
			got = deref(page[0].Sku)
		}
		if got != tc.first {
			t.Fatalf("ListProducts sort=%s first = %q, want %q", tc.sort, got, tc.first)
		}
	}
}

func TestOfferTemplateListSortsByName(t *testing.T) {
	e := SetupSearch(t)
	ctx := asCatalogueReader(e.WS, "offer_template")

	e.Seed(t, `INSERT INTO offer_template (id, workspace_id, name, locale, is_default, layout)
	           VALUES ($1, $2, 'Middle', 'de-DE', false, '{}'::jsonb)`)
	e.Seed(t, `INSERT INTO offer_template (id, workspace_id, name, locale, is_default, layout)
	           VALUES ($1, $2, 'Apex', 'en-GB', false, '{}'::jsonb)`)

	store := deals.NewStore(e.Pool)
	sortField := "name"
	one := 1
	page, _, err := store.ListOfferTemplates(ctx, deals.ListOfferTemplatesInput{Sort: &sortField, Limit: &one})
	if err != nil {
		t.Fatalf("ListOfferTemplates sort=name: %v", err)
	}
	if len(page) != 1 || page[0].Name != "Apex" {
		t.Fatalf("ListOfferTemplates sort=name first = %+v, want Apex", page)
	}
}

// deref reads an optional contract string, which a seeded row always has.
func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
