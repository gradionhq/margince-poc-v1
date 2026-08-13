// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package overlay_test

import (
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/modules/overlay"
)

// The registry is the single place three layers agree about a field. A
// contradiction inside it — one wire slot bound twice, a canonical key
// claimed by two slots, an excuse with no reason — would propagate to all
// three, so it is rejected here rather than discovered downstream.
func TestFieldBindingsAreInternallyConsistent(t *testing.T) {
	entities := overlay.FieldBindings()
	if len(entities) == 0 {
		t.Fatal("FieldBindings() is empty; the registry every overlay gate derives from has moved or been deleted")
	}
	for _, entity := range entities {
		slots := map[string]bool{}
		keys := map[string]bool{}
		for _, b := range entity.Bindings {
			if b.WireSlot == "" {
				t.Errorf("%s: a binding declares no wire slot", entity.Entity)
				continue
			}
			if slots[b.WireSlot] {
				t.Errorf("%s: wire slot %q is bound more than once; two writers for one target", entity.Entity, b.WireSlot)
			}
			slots[b.WireSlot] = true
			if b.CanonicalKey != "" {
				if keys[b.CanonicalKey] {
					t.Errorf("%s: canonical key %q is claimed by more than one wire slot", entity.Entity, b.CanonicalKey)
				}
				keys[b.CanonicalKey] = true
			}
			checkDisposition(t, entity.Entity, b)
		}
		checkDerivedFrom(t, entity)
	}
}

// checkDerivedFrom holds every derived binding to the invariant that gives the
// disposition its meaning: a slot the wire computes must be computed from
// slots the mirror really carries. Unenforced, "derived" would be a way to
// claim mirrored data for a field no mirrored value ever reaches — native_only
// with a friendlier name. It is checked per entity rather than per binding
// because the sources are named in the same entity's vocabulary; a slot mapped
// on a sibling entity says nothing about this one's mirror.
func checkDerivedFrom(t *testing.T, entity overlay.EntityBinding) {
	t.Helper()
	mapped := map[string]bool{}
	for _, b := range entity.Bindings {
		if b.Disposition == overlay.DispositionMapped {
			mapped[b.WireSlot] = true
		}
	}
	for _, b := range entity.Bindings {
		if b.Disposition != overlay.DispositionDerived {
			continue
		}
		for _, source := range b.DerivedFrom {
			if mapped[source] {
				continue
			}
			t.Errorf("%s.%s is derived from %s.%s, which is not a mapped binding on this entity — so the mirror "+
				"carries no input the derivation could run over. Map %q, or point %q's DerivedFrom at the slots "+
				"the mirror really supplies.",
				entity.Entity, b.WireSlot, entity.Entity, source, source, b.WireSlot)
		}
	}
}

// checkDisposition asserts one binding carries exactly what its disposition
// obliges: a mapped field names its source, and every other kind states why
// it carries nothing — an unexplained absence is how a gap becomes permanent.
func checkDisposition(t *testing.T, entity string, b overlay.FieldBinding) {
	t.Helper()
	if b.Disposition != overlay.DispositionDerived && len(b.DerivedFrom) > 0 {
		t.Errorf("%s.%s is %s but names DerivedFrom %v; only a derived slot is computed from other slots, so the "+
			"list would be read by nothing", entity, b.WireSlot, b.Disposition, b.DerivedFrom)
	}
	switch b.Disposition {
	case overlay.DispositionMapped:
		if b.CanonicalKey == "" || len(b.Incumbent) == 0 {
			t.Errorf("%s.%s is mapped but names no canonical key or no incumbent property", entity, b.WireSlot)
		}
	case overlay.DispositionDeferred:
		if !strings.HasPrefix(b.IssueURL, "https://") {
			t.Errorf("%s.%s is deferred but carries no issue URL; a deferral without a tracked issue is a TODO that never returns", entity, b.WireSlot)
		}
		if b.CanonicalKey != "" || len(b.Incumbent) > 0 || b.Transform != "" {
			t.Errorf("%s.%s is deferred but names a canonical key, an incumbent property or a transform; a slot nothing fills must claim no source, or the registry reads as a working mapping", entity, b.WireSlot)
		}
	case overlay.DispositionDerived:
		if len(b.DerivedFrom) == 0 {
			t.Errorf("%s.%s is derived but names no source slot; set DerivedFrom to the wire slots it is computed "+
				"from, or say plainly which of the other four dispositions it really is", entity, b.WireSlot)
		}
		if b.CanonicalKey != "" || len(b.Incumbent) > 0 {
			t.Errorf("%s.%s is derived but claims canonical key %q and incumbent properties %v; a derived slot "+
				"reads no source of its own — the ones it depends on belong to the DerivedFrom slots %v",
				entity, b.WireSlot, b.CanonicalKey, b.Incumbent, b.DerivedFrom)
		}
	case overlay.DispositionUnmappable, overlay.DispositionNativeOnly:
		if strings.TrimSpace(b.Reason) == "" {
			t.Errorf("%s.%s is %s but states no reason", entity, b.WireSlot, b.Disposition)
		}
		if b.CanonicalKey != "" {
			t.Errorf("%s.%s is %s but claims canonical key %q; a field the mirror does not carry must claim no key", entity, b.WireSlot, b.Disposition, b.CanonicalKey)
		}
	default:
		t.Errorf("%s.%s declares an unknown disposition %q", entity, b.WireSlot, b.Disposition)
	}
}

// BindingsFor is how both the mapping gate and the wire gate reach one
// entity's bindings; an entity in the registry must be findable by name.
func TestBindingsForFindsEveryDeclaredEntity(t *testing.T) {
	for _, entity := range overlay.FieldBindings() {
		got, ok := overlay.BindingsFor(entity.Entity)
		if !ok {
			t.Errorf("BindingsFor(%q) = ok false, but FieldBindings() declares it", entity.Entity)
			continue
		}
		if len(got.Bindings) != len(entity.Bindings) {
			t.Errorf("BindingsFor(%q) returned %d bindings, want %d", entity.Entity, len(got.Bindings), len(entity.Bindings))
		}
	}
	if _, ok := overlay.BindingsFor("no_such_entity"); ok {
		t.Error("BindingsFor answered ok for an entity the registry never declared; an unknown name must be an honest miss, not an empty success")
	}
}
