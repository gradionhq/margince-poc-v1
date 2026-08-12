// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// A binding that claims the mirror carries a field is a claim about what a
// CALLER RECEIVES, not about what the ingest layer stored. The two came apart
// once already — address and owner_id were mapped into the mirror and picked
// up by nothing — and the distance was invisible because every layer was
// correct on its own: the mapping landed the value, the mirror held it, and
// the wire assembly simply never named it. This gate closes that distance by
// asserting behaviour rather than declarations: a record the real mapping
// pipeline produced must yield a response body where every mapped slot
// carries what the mirror holds.
//
// The record is seeded THROUGH the pipeline (hubspot.Mapping → overlay.Apply)
// rather than hand-written in canonical shape, so the payload under test is
// the one production writes — a hand-built canonical fixture proves only that
// the wire reads the fixture's own author.

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/gradionhq/margince/backend/internal/modules/overlay"
	"github.com/gradionhq/margince/backend/internal/modules/overlay/hubspot"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
)

// personIncumbentFixture is one plausible HubSpot contact, in the INCUMBENT's
// own property vocabulary — the only vocabulary this gate hand-writes. Every
// property a mapped person binding names is present, and each value is
// distinguishable from what the wire assembles for a record that carries
// nothing, which is what lets a dropped pick be told apart from a fallback
// standing in for it.
func personIncumbentFixture() map[string]any {
	return map[string]any{
		"hs_object_id":     "100214862042",
		"firstname":        "Ada",
		"lastname":         "Overlay",
		"email":            "Ada@Example.DE",
		"jobtitle":         "CTO",
		"phone":            "+4930111",
		"mobilephone":      "+4917622",
		"address":          "Hauptstrasse 1",
		"city":             "Munich",
		"state":            "Bayern",
		"zip":              "80331",
		"country":          "DE",
		"createdate":       "2024-11-15T13:27:49.194Z",
		"lastmodifieddate": "2026-05-13T06:44:38.727Z",
	}
}

// organizationIncumbentFixture is personIncumbentFixture's counterpart for a
// HubSpot company, held to the same two obligations: every property a mapped
// organization binding names is present, and no value coincides with what the
// unmirrored assembly produces for that slot ("Unnamed", the mirror's own sync
// instant, absent).
func organizationIncumbentFixture() map[string]any {
	return map[string]any{
		"hs_object_id":        "61655665850",
		"name":                "Overlay GmbH",
		"industry":            "COMPUTER_SOFTWARE",
		"numberofemployees":   "75",
		"domain":              "Overlay.DE",
		"address":             "Hauptstrasse 1",
		"city":                "Munich",
		"state":               "Bayern",
		"zip":                 "80331",
		"country":             "DE",
		"createdate":          "2024-11-15T13:27:49.194Z",
		"hs_lastmodifieddate": "2026-05-13T06:44:38.727Z",
	}
}

func TestEveryMappedPersonBindingReachesItsWireSlot(t *testing.T) {
	entity, ok := overlay.BindingsFor("person")
	if !ok {
		t.Fatal("the registry declares no person bindings; the source this gate derives from has moved")
	}
	incumbent := personIncumbentFixture()
	canonical := canonicalFromMapping(t, "contacts", "personIncumbentFixture", incumbent)
	mirrored, unmirrored := wireBodyPair(t, datasource.EntityPerson, canonical, overlayWirePerson)
	wireGate{
		entity:    entity,
		assembler: "overlayWirePerson",
		fixture:   "personIncumbentFixture",
		incumbent: incumbent, canonical: canonical,
		mirrored: mirrored, unmirrored: unmirrored,
	}.check(t)
}

func TestEveryMappedOrganizationBindingReachesItsWireSlot(t *testing.T) {
	entity, ok := overlay.BindingsFor("organization")
	if !ok {
		t.Fatal("the registry declares no organization bindings; the source this gate derives from has moved")
	}
	incumbent := organizationIncumbentFixture()
	canonical := canonicalFromMapping(t, "companies", "organizationIncumbentFixture", incumbent)
	mirrored, unmirrored := wireBodyPair(t, datasource.EntityOrganization, canonical, overlayWireOrganization)
	wireGate{
		entity:    entity,
		assembler: "overlayWireOrganization",
		fixture:   "organizationIncumbentFixture",
		incumbent: incumbent, canonical: canonical,
		mirrored: mirrored, unmirrored: unmirrored,
	}.check(t)
}

// wireGate is one entity's inputs to the shared reachability check. The
// assembler and the fixture are carried by name so that one shared check still
// fails with the entity's OWN address in the message: a person failure names
// overlayWirePerson and personIncumbentFixture, never "the wire".
type wireGate struct {
	entity     overlay.EntityBinding
	assembler  string
	fixture    string
	incumbent  map[string]any
	canonical  map[string]any
	mirrored   map[string]any
	unmirrored map[string]any
}

// check holds every binding the registry calls mapped to reaching a caller. An
// entity that declares none is a failure rather than a pass: this gate would
// otherwise report green while checking nothing.
func (g wireGate) check(t *testing.T) {
	t.Helper()
	mapped := 0
	for _, b := range g.entity.Bindings {
		if b.Disposition != overlay.DispositionMapped {
			continue
		}
		mapped++
		g.checkBinding(t, b)
	}
	if mapped == 0 {
		t.Fatalf("%s declares no mapped bindings; this gate would pass while checking nothing", g.entity.Entity)
	}
}

// checkBinding walks one mapped binding across the three layers it claims to
// span, stopping at the first LAYER that breaks so the later message cannot
// blame the wrong one — every property the fixture omits is named before it
// stops, since those are all one layer's problem. The Incumbent check comes
// first: it is the only part of the registry a fixture cannot contradict
// silently, since a claim about a property the fixture never sends is proven by
// nothing that follows.
func (g wireGate) checkBinding(t *testing.T, b overlay.FieldBinding) {
	t.Helper()
	fixtureComplete := true
	for _, property := range b.Incumbent {
		if _, sent := g.incumbent[property]; !sent {
			fixtureComplete = false
			t.Errorf("%s.%s is declared mapped from %v, but %s sends no %q, so nothing below proves that "+
				"property reaches anywhere. Add it to the fixture, or drop it from the binding — Incumbent "+
				"is a claim about where the value comes from, not a wish list.",
				g.entity.Entity, b.WireSlot, b.Incumbent, g.fixture, property)
		}
	}
	if !fixtureComplete {
		return
	}
	if _, landed := g.canonical[b.CanonicalKey]; !landed {
		t.Errorf("%s.%s is declared mapped from %v, but the mapping pipeline landed no %q in the canonical "+
			"payload. Either %s omits those properties, or the HubSpot mapping does not write that canonical "+
			"key and the binding names one that never exists.",
			g.entity.Entity, b.WireSlot, b.Incumbent, b.CanonicalKey, g.fixture)
		return
	}
	value, present := g.mirrored[b.WireSlot]
	if !present || value == nil {
		t.Errorf("%s.%s is declared mapped from %v, but the assembled body leaves it empty. Either %s does "+
			"not pick up %q, or the binding overstates what the mirror carries.",
			g.entity.Entity, b.WireSlot, b.Incumbent, g.assembler, b.CanonicalKey)
		return
	}
	if reflect.DeepEqual(value, g.unmirrored[b.WireSlot]) {
		t.Errorf("%s.%s is declared mapped from %v, but the assembled body carries %v — the very value a "+
			"record with an EMPTY payload produces, so the mirror's %q never reached the caller. Have %s read "+
			"%q, or, if the value is genuinely the mirror's, give %s one that differs from the fallback.",
			g.entity.Entity, b.WireSlot, b.Incumbent, value, b.CanonicalKey, g.assembler, b.CanonicalKey, g.fixture)
	}
}

// canonicalFromMapping projects an incumbent fixture through the REAL HubSpot
// mapping for its object class, so the canonical payload under test is the one
// the ingest path writes. A property the mapping does not consume is a fixture
// typo — every property the bindings name is consumed — and it would otherwise
// read as a wire defect two assertions later.
func canonicalFromMapping(t *testing.T, objectClass, fixture string, incumbent map[string]any) map[string]any {
	t.Helper()
	m, ok := hubspot.Mapping(objectClass)
	if !ok {
		t.Fatalf("Mapping(%s): want a declared mapping", objectClass)
	}
	canonical, unmapped, err := overlay.Apply(m, incumbent)
	if err != nil {
		t.Fatalf("Apply(%s): %v", objectClass, err)
	}
	if len(unmapped) != 0 {
		t.Fatalf("unmapped = %v: %s names properties the %s mapping does not consume, so they reach no "+
			"canonical key", unmapped, fixture, objectClass)
	}
	return canonical
}

// wireBodyPair assembles ONE mirror record twice — once carrying the canonical
// payload, once carrying nothing — and renders both the way a client receives
// them. The unmirrored body is the baseline every mapped slot is compared
// against: each of its slots is what the wire produces WITHOUT the mirror (a
// nil, a placeholder name, the mirror's own sync instant), so a mapped slot
// that still reads the same is a slot the mirror's value never reached, however
// non-empty the fallback looks. The comparison is only sound because the two
// runs share an identity and a request context: a child row's synthetic id is
// derived from the parent id and the workspace stamp is read from the context,
// so two independently generated ones would differ in slots the mirror never
// touched.
func wireBodyPair[T any](t *testing.T, et datasource.EntityType, fields map[string]any,
	assemble func(context.Context, datasource.Record) (T, error),
) (mirrored, unmirrored map[string]any) {
	t.Helper()
	ctx := wireCtx()
	filled := wireRecord(t, et, fields)
	empty := wireRecord(t, et, map[string]any{})
	empty.Ref = filled.Ref

	withMirror, err := assemble(ctx, filled)
	if err != nil {
		t.Fatalf("assembling the mirrored %s: %v", et, err)
	}
	withoutMirror, err := assemble(ctx, empty)
	if err != nil {
		t.Fatalf("assembling the unmirrored %s: %v", et, err)
	}
	return marshalToMap(t, withMirror), marshalToMap(t, withoutMirror)
}

// marshalToMap renders an assembled wire struct the way a client receives it.
// Asserting on the JSON rather than the Go struct is what makes an omitempty
// pointer left nil indistinguishable from a slot never filled — which is
// exactly the defect being watched for.
//
//craft:ignore naked-any v is any of the five assembled wire structs on their way through encoding/json — the untyped boundary itself
func marshalToMap(t *testing.T, v any) map[string]any {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshaling the assembled wire struct: %v", err)
	}
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("decoding the assembled wire body: %v", err)
	}
	return body
}
