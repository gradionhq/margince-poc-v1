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
	}
}

// checkDisposition asserts one binding carries exactly what its disposition
// obliges: a mapped field names its source, and every other kind states why
// it carries nothing — an unexplained absence is how a gap becomes permanent.
func checkDisposition(t *testing.T, entity string, b overlay.FieldBinding) {
	t.Helper()
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
