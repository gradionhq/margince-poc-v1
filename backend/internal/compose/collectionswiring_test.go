// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// newCollectionsHandlers is the ONE place collections.NewHandlers is built
// for the lists/tags/saved-views surface — server.go calls only this. A test
// that re-derived "what the wiring should look like" from its own copy of
// customfields.NewService would prove nothing about production (rule 6): it
// has to run the actual function and read what came out.
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

// TestNewCollectionsHandlersCarriesTheFieldCatalogSeam proves the store
// behind dynamic-list create validation and the members endpoint carries a
// non-nil custom-field catalog — the same seam wireExportSurface wires for
// filtered export. Without it, a cf_* filter a filtered export of a list
// accepts is refused as an unknown field by the create and members
// endpoints for that very list, which is exactly the divergence this
// resolver's own SegmentEngine doc promises cannot happen.
func TestNewCollectionsHandlersCarriesTheFieldCatalogSeam(t *testing.T) {
	h := newCollectionsHandlers(nil)
	store := reflect.ValueOf(h).FieldByName("store")
	if store.IsNil() {
		t.Fatal("newCollectionsHandlers built a store with no field catalog seam at all")
	}
	catalog := store.Elem().FieldByName("catalog")
	if catalog.IsNil() {
		t.Fatal("newCollectionsHandlers did not wire a field catalog into the collections store — " +
			"dynamic-list create validation and the members endpoint would refuse every cf_* " +
			"filter a filtered export of the same list accepts")
	}
}
