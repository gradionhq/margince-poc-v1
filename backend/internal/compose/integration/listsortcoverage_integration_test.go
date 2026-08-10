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

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
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

// The ties below are resolved by the ORDER BY's trailing created_at DESC, id
// DESC, so a boolean column with two rows on the same side still has one right
// answer. Every seeded row therefore carries an explicit creation instant
// rather than whatever clock the insert ran under. The house updated_at
// trigger fires BEFORE UPDATE only, so a seeded modification instant survives
// the insert — and each row is given one that disagrees with its creation
// instant, so a list reading the wrong timestamp column fails rather than
// coincidentally agreeing.
const (
	seedEarliest = "2026-01-01T00:00:00Z"
	seedMiddle   = "2026-01-02T00:00:00Z"
	seedLatest   = "2026-01-03T00:00:00Z"
)

func TestProductListSortsByEveryOfferedColumn(t *testing.T) {
	e := SetupSearch(t)
	ctx := asCatalogueReader(e.WS, "product")

	// Seeded so that creation order disagrees with every sort below: a list
	// still ordering by created_at would return the same page each time.
	const seedProduct = `INSERT INTO product (id, workspace_id, name, sku, unit, unit_price_minor, currency, default_tax_rate, active, source, captured_by, created_at, updated_at)
	                     VALUES ($1, $2, $3, $4, 'day', $5, 'EUR', 0, $6, 'ui', 'human:x', $7, $8)`
	e.Seed(t, seedProduct, "Middle", "SKU-M", 5000, true, seedMiddle, seedEarliest)
	e.Seed(t, seedProduct, "Apex", "SKU-A", 9000, false, seedLatest, seedMiddle)
	e.Seed(t, seedProduct, "Zenith", "SKU-Z", 1000, true, seedEarliest, seedLatest)

	store := deals.NewStore(e.Pool, harnessInstallation())
	for _, tc := range []struct {
		sort  string
		order []string
	}{
		{sort: "name", order: []string{"Apex", "Middle", "Zenith"}},
		{sort: "-name", order: []string{"Zenith", "Middle", "Apex"}},
		{sort: "sku", order: []string{"Apex", "Middle", "Zenith"}},
		{sort: "unit_price_minor", order: []string{"Zenith", "Middle", "Apex"}},
		{sort: "-unit_price_minor", order: []string{"Apex", "Middle", "Zenith"}},
		// Apex is the only inactive row; the two active ones fall back to
		// newest-first.
		{sort: "active", order: []string{"Apex", "Middle", "Zenith"}},
		{sort: "created_at", order: []string{"Zenith", "Middle", "Apex"}},
		{sort: "-created_at", order: []string{"Apex", "Middle", "Zenith"}},
		{sort: "updated_at", order: []string{"Middle", "Apex", "Zenith"}},
		{sort: "-updated_at", order: []string{"Zenith", "Apex", "Middle"}},
	} {
		sortField := tc.sort
		all := len(tc.order)
		page, _, err := store.ListProducts(ctx, deals.ListProductsInput{Sort: &sortField, Limit: &all})
		if err != nil {
			t.Fatalf("ListProducts sort=%s: %v", tc.sort, err)
		}
		assertOrder(t, "ListProducts sort="+tc.sort, tc.order, productNames(page))
	}
}

func TestOfferTemplateListSortsByEveryOfferedColumn(t *testing.T) {
	e := SetupSearch(t)
	ctx := asCatalogueReader(e.WS, "offer_template")

	const seedTemplate = `INSERT INTO offer_template (id, workspace_id, name, locale, is_default, layout, created_at, updated_at)
	                      VALUES ($1, $2, $3, $4, $5, '{}'::jsonb, $6, $7)`
	e.Seed(t, seedTemplate, "Middle", "de-DE", false, seedMiddle, seedEarliest)
	e.Seed(t, seedTemplate, "Apex", "en-GB", false, seedLatest, seedMiddle)
	e.Seed(t, seedTemplate, "Zenith", "fr-FR", true, seedEarliest, seedLatest)

	store := deals.NewStore(e.Pool, harnessInstallation())
	for _, tc := range []struct {
		sort  string
		order []string
	}{
		{sort: "name", order: []string{"Apex", "Middle", "Zenith"}},
		{sort: "-name", order: []string{"Zenith", "Middle", "Apex"}},
		{sort: "locale", order: []string{"Middle", "Apex", "Zenith"}},
		// Zenith is the only default template; the other two fall back to
		// newest-first.
		{sort: "is_default", order: []string{"Apex", "Middle", "Zenith"}},
		{sort: "created_at", order: []string{"Zenith", "Middle", "Apex"}},
		{sort: "-created_at", order: []string{"Apex", "Middle", "Zenith"}},
		{sort: "updated_at", order: []string{"Middle", "Apex", "Zenith"}},
		{sort: "-updated_at", order: []string{"Zenith", "Apex", "Middle"}},
	} {
		sortField := tc.sort
		all := len(tc.order)
		page, _, err := store.ListOfferTemplates(ctx, deals.ListOfferTemplatesInput{Sort: &sortField, Limit: &all})
		if err != nil {
			t.Fatalf("ListOfferTemplates sort=%s: %v", tc.sort, err)
		}
		assertOrder(t, "ListOfferTemplates sort="+tc.sort, tc.order, templateNames(page))
	}
}

// assertOrder compares a page's names against the whole expected sequence, not
// only its head: a column that orders the first row and then falls back to
// creation time for the rest is still an unanswered header.
func assertOrder(t *testing.T, what string, want, got []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s returned %d rows (%v), want %d", what, len(got), got, len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("%s order = %v, want %v", what, got, want)
		}
	}
}

func productNames(page []crmcontracts.Product) []string {
	names := make([]string, 0, len(page))
	for _, p := range page {
		names = append(names, p.Name)
	}
	return names
}

func templateNames(page []crmcontracts.OfferTemplate) []string {
	names := make([]string, 0, len(page))
	for _, tpl := range page {
		names = append(names, tpl.Name)
	}
	return names
}
