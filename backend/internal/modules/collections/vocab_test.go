// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package collections

// The segment vocabulary's own obligations: every type that can be tagged can be
// filtered by tag, and the tag leaf reaches the join the same way for each.

import (
	"strings"
	"testing"
)

// Derived, not listed: a fifth taggable type fails here rather than shipping
// without a tag filter, which is the failure nobody would notice.
func TestEveryTaggableTypeCanBeFilteredByTag(t *testing.T) {
	for _, entity := range taggableEntityTypes() {
		engine, ok := segmentEngines[entity]
		if !ok {
			t.Fatalf("%s is taggable but has no segment engine", entity)
		}
		field, ok := engine.Fields["tag"]
		if !ok {
			t.Fatalf("%s is taggable but carries no tag filter field", entity)
		}
		if field.Link == "" {
			t.Errorf("%s's tag field is not a link leaf; it cannot reach taggable", entity)
		}
		if !strings.Contains(field.Link, "tg.entity_type = '"+entity+"'") {
			t.Errorf("%s's tag field does not bind its own entity_type: %q", entity, field.Link)
		}
		if strings.Contains(field.Link, "workspace_id") {
			t.Errorf("%s's tag field names taggable.workspace_id, which migration 0228 dropped", entity)
		}
		if count := strings.Count(field.Link, "%s"); count != 1 {
			t.Errorf("%s's tag field has %d %%s verbs in its Link template, want exactly 1: %q", entity, count, field.Link)
		}
	}
}

// A project is a taggable record (taggable's own CHECK admits it) and its
// list membership must offer the same filter every other taggable type does.
func TestProjectIsFilterableByTag(t *testing.T) {
	field, ok := segmentEngines[projectEntity].Fields["tag"]
	if !ok {
		t.Fatal("project is taggable but carries no tag filter field")
	}
	if !strings.Contains(field.Link, "tg.entity_type = '"+projectEntity+"'") {
		t.Errorf("project's tag field does not bind its own entity_type: %q", field.Link)
	}
}
