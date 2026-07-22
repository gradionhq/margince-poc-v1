// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package policy

import (
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

func TestEverySystemRoleHasAValidDefaultDocument(t *testing.T) {
	for _, key := range []string{"admin", "manager", "rep", "read_only", "ops"} {
		doc, err := Parse(MustDefaultJSON(key))
		if err != nil {
			t.Errorf("seeded default for %q does not pass its own validator: %v", key, err)
			continue
		}
		if len(doc.Objects) != len(coreObjects) {
			t.Errorf("role %q covers %d objects, want all %d (an unnamed object silently denies)",
				key, len(doc.Objects), len(coreObjects))
		}
	}
}

func TestParseRejectsDishonestDocuments(t *testing.T) {
	cases := map[string]string{
		"unknown object":    `{"objects":{"invoice":{"read":true}},"row_scope":"all"}`,
		"invalid row_scope": `{"objects":{"person":{"read":true}},"row_scope":"everything"}`,
		"malformed json":    `{"objects":`,
	}
	for name, raw := range cases {
		if _, err := Parse([]byte(raw)); err == nil {
			t.Errorf("Parse accepted a document with %s", name)
		}
	}
}

func TestParseDefaultsAnUnsetScopeToNarrowest(t *testing.T) {
	doc, err := Parse([]byte(`{"objects":{"person":{"read":true}}}`))
	if err != nil {
		t.Fatal(err)
	}
	if doc.RowScope != principal.RowScopeOwn {
		t.Errorf("unset row_scope resolved to %q, must fail closed to own", doc.RowScope)
	}
}

func TestMergeUnionsGrantsAndWidensScope(t *testing.T) {
	rep, _ := Parse(MustDefaultJSON("rep"))
	readonly, _ := Parse(MustDefaultJSON("read_only"))
	merged := Merge(map[string]Document{"rep": rep, "read_only": readonly})

	// Union: rep's writes survive the read-only role being added.
	if !merged.Allows("person", principal.ActionCreate) {
		t.Error("merge lost rep's person.create")
	}
	// Neither role deletes people; the union must not invent it.
	if merged.Allows("person", principal.ActionDelete) {
		t.Error("merge invented person.delete that no role grants")
	}
	// Widest scope wins: read_only's `all` over rep's `team`.
	if merged.RowScope != principal.RowScopeAll {
		t.Errorf("merged row scope %q, want all (the widest held)", merged.RowScope)
	}
	if len(merged.RoleKeys) != 2 {
		t.Errorf("attribution lists %v, want both roles", merged.RoleKeys)
	}
}

func TestEmbeddingReindexGrants(t *testing.T) {
	for _, key := range []string{"admin", "ops"} {
		doc, err := Parse(MustDefaultJSON(key))
		if err != nil {
			t.Fatalf("role %q: %v", key, err)
		}
		merged := Merge(map[string]Document{key: doc})
		if !merged.Allows("embedding_reindex", principal.ActionUpdate) {
			t.Errorf("role %q should be able to update embedding_reindex (trigger a reindex)", key)
		}
		if !merged.Allows("embedding_reindex", principal.ActionRead) {
			t.Errorf("role %q should be able to read embedding_reindex", key)
		}
	}
	for _, key := range []string{"manager", "rep", "read_only"} {
		doc, err := Parse(MustDefaultJSON(key))
		if err != nil {
			t.Fatalf("role %q: %v", key, err)
		}
		merged := Merge(map[string]Document{key: doc})
		if !merged.Allows("embedding_reindex", principal.ActionRead) {
			t.Errorf("role %q should be able to read embedding_reindex (the reindex-needed banner is wide-read)", key)
		}
		if merged.Allows("embedding_reindex", principal.ActionUpdate) {
			t.Errorf("role %q must not be able to trigger a reindex", key)
		}
	}
}

func TestZeroRolesDenyEverything(t *testing.T) {
	merged := Merge(nil)
	for _, object := range coreObjects {
		for _, a := range []principal.Action{principal.ActionCreate, principal.ActionRead, principal.ActionUpdate, principal.ActionDelete} {
			if merged.Allows(object, a) {
				t.Errorf("a user with no roles was granted %s.%s", object, a)
			}
		}
	}
}
