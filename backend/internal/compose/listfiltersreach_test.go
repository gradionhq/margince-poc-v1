// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// list_records' filter vocabulary is assembled from two halves that live in
// different repositories of truth — the contract's declared list parameters and
// what a module's store can actually bind — and this is the only layer that can
// see both ends of the wire the halves travel down.
//
// The gate below is about the WIRE, not about either half. Each half has its own
// check where it lives (the generator reads crm.yaml; each module proves every
// filter it declares narrows something). What neither can see is a break
// between them: a provider that stopped delegating, a record type dropped from
// the enumeration set, a registration that never happened. Every one of those
// fails the same way — quietly, with a tool that still answers and simply never
// narrows.

import (
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
)

// Every filter a store binds reaches the tool's published schema.
//
// A nil pool is enough: the vocabulary is a property of the code, resolved at
// registration, and no part of answering it touches the database.
func TestEveryFilterAStoreBindsReachesTheAgentSurface(t *testing.T) {
	registry := NewRegistry(nil, SendPath{})
	spec, ok := registry.Spec("list_records")
	if !ok {
		t.Fatal("list_records is not registered, so the surface has no enumeration verb at all")
	}
	published := string(spec.InputSchema)

	for _, recordType := range []datasource.EntityType{
		datasource.EntityPerson, datasource.EntityOrganization,
		datasource.EntityDeal, datasource.EntityLead, datasource.EntityProject,
	} {
		bound := NewProvider(nil).ListFilters(recordType)
		if len(bound) == 0 {
			t.Errorf("%s binds no filters at all, so it can only be listed whole — "+
				"if that is a wiring break rather than the truth, it is invisible from the tool",
				recordType)
			continue
		}
		for _, name := range bound {
			if !strings.Contains(published, name) {
				t.Errorf("%s binds %q and list_records does not publish it: capability nobody can reach. "+
					"A filter is published only when crm.yaml's list operation declares it too, so the "+
					"fix is usually the missing parameter there.", recordType, name)
			}
		}
	}
}

// The one record type the enumeration deliberately excludes stays excluded. A
// timeline is reached through the record it hangs off, and sweeping it is what
// search_records' own copy tells a caller it will not do.
func TestTheEnumerationDoesNotOfferActivities(t *testing.T) {
	registry := NewRegistry(nil, SendPath{})
	spec, _ := registry.Spec("list_records")
	if strings.Contains(string(spec.InputSchema), `"activity"`) {
		t.Errorf("list_records offers activity in its record_type enum:\n%s", spec.InputSchema)
	}
}
