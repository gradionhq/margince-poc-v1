// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// Where each archive question goes, asked without a database.
//
// The composite answers three questions by routing them to the module that
// performs the write, and the routing is the whole of what can be wrong here:
// a type sent to the wrong module archives the wrong table, and a type sent
// nowhere must say so rather than answer a zero value. Neither needs a row.

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
)

// The composite archives exactly what its modules archive, each named once.
//
// Pinned as a set rather than a count: a count agrees with a routing table that
// has swapped two types, and the refusal a caller reads is built from these
// names.
func TestTheCompositeArchivesWhatItsModulesArchive(t *testing.T) {
	types, err := NewProvider(nil).ArchivableTypes(context.Background())
	if err != nil {
		t.Fatalf("asking the composite what it archives answered %v", err)
	}

	want := []datasource.EntityType{
		datasource.EntityActivity, datasource.EntityDeal, datasource.EntityOrganization,
		datasource.EntityPerson, datasource.EntityProject, datasource.EntityRelationship,
	}
	if !slices.Equal(types, want) {
		t.Errorf("the composite archives %v, want %v — a staging refusal is built from this list, so a "+
			"type missing here is refused and a type over-claimed is admitted and then refused by the store",
			types, want)
	}
}

// A type no module archives is refused by NAME, on every question.
//
// All three are asserted because they are three separate switches to get
// wrong, and the two that answer an error rather than a bool are the ones
// where a missed default returns a usable zero value instead.
func TestATypeNoModuleArchivesIsRefusedOnEveryQuestion(t *testing.T) {
	p := NewProvider(nil)
	ctx := context.Background()
	ref := datasource.EntityRef{Type: datasource.EntityLead, ID: ids.NewV7()}

	if err := p.RefuseArchive(ctx, ref); !isUnsupportedEntity(err) {
		t.Errorf("RefuseArchive(lead) answered %v, want the unsupported-entity refusal — a lead leaves "+
			"through its own lifecycle verbs, and staging one must not read as permitted", err)
	}
	if _, err := p.ArchiveAt(ctx, datasource.ArchiveInput{Ref: ref}); !isUnsupportedEntity(err) {
		t.Errorf("ArchiveAt(lead) answered %v, want the unsupported-entity refusal", err)
	}
	if _, err := p.Archive(ctx, ref); !isUnsupportedEntity(err) {
		t.Errorf("Archive(lead) answered %v, want the unsupported-entity refusal — the v1 verb delegates "+
			"to ArchiveAt and must not have picked up a different answer on the way", err)
	}
}

// Every type routes to the module that CLAIMS it.
//
// Asserting the identity of the module, not merely that one was found: a
// non-nil check passes for a routing table with two arms swapped, and a
// swapped arm archives the wrong table under the right name. Both sides are
// derived — each module is asked what it archives, and the router is asked who
// gets it — because the defect is the two disagreeing, and a hand-written
// third list is how they come to.
func TestEveryTypeRoutesToTheModuleThatClaimsIt(t *testing.T) {
	p := NewProvider(nil)
	ctx := context.Background()
	for _, module := range []datasource.RecordArchiverV2{p.people, p.deals, p.activities} {
		claimed, err := module.ArchivableTypes(ctx)
		if err != nil {
			t.Fatalf("asking a module what it archives answered %v", err)
		}
		if len(claimed) == 0 {
			t.Fatal("a module claims no archivable type at all — every arm of the router below would " +
				"then be unasserted, and this test would pass having compared nothing")
		}
		for _, entity := range claimed {
			if routed := p.archiverFor(entity); routed != module {
				t.Errorf("%s is archived by %T and routed to %T — the write lands on another module's "+
					"table under this type's name", entity, module, routed)
			}
		}
	}
}

func isUnsupportedEntity(err error) bool {
	var unsupported *datasource.UnsupportedEntityError
	return errors.As(err, &unsupported)
}
