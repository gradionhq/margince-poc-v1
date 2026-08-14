// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// NewCollectionsStore is the ONE place a catalogue-wired collections store
// is built — every surface that needs one (the lists/tags/saved-views
// transport, filtered export) calls this constructor rather than building
// its own, so gating this one function covers every caller; a store built
// any other way is a bug at the call site, not a second wiring path this
// test would need to separately catch. A test that re-derived "what the
// wiring should look like" from its own copy of customfields.NewService
// would prove nothing about production (rule 6): it has to run the actual
// function and read what came out.
//
// What it reads is the seam itself, not a request outcome: collections.Store
// keeps its catalog field unexported (no production code ever needs to ask a
// store whether it was wired), so reflection is the only way to see it from
// this package. Reflect.Value.IsNil() does not require CanInterface(), so it
// never trips the unexported-field panic — it is reading a Kind, not a value.
// A behavioural version of this test — POST /v1/lists naming a real cf_*
// column and asserting 201 rather than 422 — needs a live custom-field
// catalog behind a real Postgres pool (FilterableColumns issues a query), so
// it belongs in the integration lane, not here.

import (
	"reflect"
	"testing"
)

// TestNewCollectionsStoreCarriesTheFieldCatalogSeam proves the store behind
// every collections-store caller — dynamic-list create validation, the
// members endpoint, and filtered export alike — carries a non-nil
// custom-field catalog. Without it, a cf_* filter one of those surfaces
// accepts is refused as an unknown field by another, which is exactly the
// divergence this resolver's own SegmentEngine doc promises cannot happen.
func TestNewCollectionsStoreCarriesTheFieldCatalogSeam(t *testing.T) {
	store := NewCollectionsStore(nil)
	catalog := reflect.ValueOf(store).Elem().FieldByName("catalog")
	if catalog.IsNil() {
		t.Fatal("NewCollectionsStore built a store with no field catalog seam at all — " +
			"every caller of this constructor would refuse a cf_* filter another accepts")
	}
}
