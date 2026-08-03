// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package customfields

// A custom field this engine accepts must be one the wire can actually carry.
//
// FieldObjects used to be datasource.EntityTypes(), and that derivation was
// wrong in a way no test noticed: `object=activity` has been accepted since it
// shipped and served by nobody, because a cf_* value travels in the contract
// schema's additionalProperties bag and Activity has none. The field could be
// created, the ALTER ran, the column existed — and every read dropped it. Only
// the SPA's picker hid it, which is a screen, not a gate.
//
// So the list is explicit now, and this is what stops it over-promising again.
// The PROPERTY is derived (reflection over the generated contract structs); only
// the object → shape pairing is written down, and a member missing from that
// pairing fails rather than being skipped.

import (
	"reflect"
	"testing"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
)

// carriageShapes binds each object to the contract types a cf_* value has to
// survive: the body it is WRITTEN in and the schema it is READ back from. Both,
// because carriage on only one half is the shape of the activity defect —
// accepted on the way in, gone on the way out.
var carriageShapes = map[string]struct{ create, read reflect.Type }{
	string(datasource.EntityPerson): {
		reflect.TypeFor[crmcontracts.CreatePersonRequest](), reflect.TypeFor[crmcontracts.Person](),
	},
	string(datasource.EntityOrganization): {
		reflect.TypeFor[crmcontracts.CreateOrganizationRequest](), reflect.TypeFor[crmcontracts.Organization](),
	},
	string(datasource.EntityDeal): {
		reflect.TypeFor[crmcontracts.CreateDealRequest](), reflect.TypeFor[crmcontracts.Deal](),
	},
	string(datasource.EntityLead): {
		reflect.TypeFor[crmcontracts.CreateLeadRequest](), reflect.TypeFor[crmcontracts.Lead](),
	},
	string(datasource.EntityProject): {
		reflect.TypeFor[crmcontracts.CreateProjectRequest](), reflect.TypeFor[crmcontracts.Project](),
	},
	// The two exclusions, carried here so the assertion below can state WHY
	// each is excluded rather than assert an absence from FieldObjects.
	string(datasource.EntityActivity): {
		reflect.TypeFor[crmcontracts.CreateActivityRequest](), reflect.TypeFor[crmcontracts.Activity](),
	},
	string(datasource.EntityRelationship): {
		reflect.TypeFor[crmcontracts.CreateRelationshipRequest](), reflect.TypeFor[crmcontracts.Relationship](),
	},
}

// carriesCustomFieldValues reports whether a generated contract struct declares
// the additionalProperties catch-all a cf_* key travels in. oapi-codegen renders
// it as a map field tagged `json:"-"`, populated by the type's own
// marshal/unmarshal — so its presence is exactly the question "can this shape
// hold a key the schema does not name".
func carriesCustomFieldValues(t reflect.Type) bool {
	field, ok := t.FieldByName("AdditionalProperties")
	return ok && field.Type.Kind() == reflect.Map
}

func TestEveryFieldObjectCarriesItsValuesOnTheWire(t *testing.T) {
	for _, object := range FieldObjects {
		shapes, paired := carriageShapes[object]
		if !paired {
			t.Errorf("FieldObjects names %q and carriageShapes has no entry for it, so this gate "+
				"skipped the one member it could not check. Add the pair.", object)
			continue
		}
		for label, shape := range map[string]reflect.Type{"create": shapes.create, "read": shapes.read} {
			if !carriesCustomFieldValues(shape) {
				t.Errorf("object %q is a custom-field target but its %s shape %s declares no "+
					"additionalProperties — a value written there cannot be carried, so the field "+
					"would be creatable and never served", object, label, shape.Name())
			}
		}
	}
}

// The exclusions are excluded for a REASON, and the reason is checkable. When
// upstream gives one of these shapes carriage, this goes red and says so —
// which is the moment to revisit the exclusion rather than to discover months
// later that the gap closed and nobody noticed.
func TestTheExcludedObjectsStillLackTheCarriageThatWouldAdmitThem(t *testing.T) {
	admitted := map[string]bool{}
	for _, object := range FieldObjects {
		admitted[object] = true
	}
	for _, object := range []string{string(datasource.EntityActivity), string(datasource.EntityRelationship)} {
		if admitted[object] {
			continue // it is a target now; the gate above governs it
		}
		shapes := carriageShapes[object]
		if carriesCustomFieldValues(shapes.create) && carriesCustomFieldValues(shapes.read) {
			t.Errorf("%q is excluded from FieldObjects for want of wire carriage, and both its "+
				"contract shapes now declare additionalProperties. The reason for the exclusion has "+
				"expired: either wire the store's cf_* columns and admit it, or restate why it stays "+
				"out. For relationship, the privacy question comes first — an edge is excluded from "+
				"piiTables on the premise that its columns are a closed set of business facts.", object)
		}
	}
}

// FieldObjects is no longer the CHECK's set, and that is deliberate: the catalog
// CHECK (migrations/core/0171) is WIDER. This pins the direction of that
// difference, because the safe asymmetry is only one way round — this engine is
// the table's sole writer, so an allowlist narrower than the CHECK refuses
// cleanly, while one wider than the CHECK would fail at INSERT with a
// constraint violation no caller can act on.
func TestEveryFieldObjectIsSpelledTheWayTheCatalogCheckSpellsIt(t *testing.T) {
	// The CHECK's vocabulary is the entity vocabulary; membership in it is what
	// makes a spelling insertable at all.
	vocabulary := map[string]bool{}
	for _, entity := range datasource.EntityTypes() {
		vocabulary[string(entity)] = true
	}
	for _, object := range FieldObjects {
		if !vocabulary[object] {
			t.Errorf("FieldObjects names %q, which is not in the entity vocabulary the "+
				"custom_field.object CHECK mirrors — a field for it would be refused by the "+
				"database at INSERT, after the ALTER had already run", object)
		}
	}
}
