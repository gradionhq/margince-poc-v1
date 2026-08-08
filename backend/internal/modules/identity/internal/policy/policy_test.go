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
	// An unknown OBJECT is deliberately absent from this table; see
	// TestParseDropsAnObjectThisInstallationDoesNotKnow. What is left is the
	// pair that genuinely has no safe reading: bytes that are not a document,
	// and a row_scope that decides how far every grant reaches.
	cases := map[string]string{
		"invalid row_scope": `{"objects":{"person":{"read":true}},"row_scope":"everything"}`,
		"malformed json":    `{"objects":`,
	}
	for name, raw := range cases {
		if _, err := Parse([]byte(raw)); err == nil {
			t.Errorf("Parse accepted a document with %s", name)
		}
	}
}

// TestParseDropsAnObjectThisInstallationDoesNotKnow is the regression test for
// the UAT's F4, and the defect it guards is not a parsing detail.
//
// Parse reads STORED data. The grantable vocabulary is a property of the
// compiled-in core set plus whichever extensions this process composes — so it
// changes when a unit is removed, while the stored document does not. Rejecting
// the document made that mismatch fatal at the only place it is read: the login
// path fails the user's whole identity resolution, so removing a composed
// extension locked out every user in a workspace whose role still carried its
// object. No endpoint and no migration existed to clear it.
//
// Dropping grants nothing, which is the strictest available reading, and it is
// the one that cannot lock anybody out of anything.
func TestParseDropsAnObjectThisInstallationDoesNotKnow(t *testing.T) {
	// `ext_departed_note` is exactly the shape a removed unit leaves behind: a
	// well-formed extension object name that no installation composes.
	doc, err := Parse([]byte(`{"objects":{"person":{"read":true,"update":true},` +
		`"ext_departed_note":{"create":true,"read":true,"update":true,"delete":true}},` +
		`"row_scope":"team"}`))
	if err != nil {
		t.Fatalf("a stored document naming a departed unit's object must still parse — "+
			"refusing it fails the LOGIN, not the screen: %v", err)
	}
	if _, present := doc.Objects["ext_departed_note"]; present {
		t.Error("the unknown object survived into the parsed document, so it could still reach a grant")
	}
	// The rest of the document is untouched: a leftover grant must degrade
	// exactly itself and nothing else.
	if got := doc.Objects["person"]; !got.Read || !got.Update {
		t.Errorf("person grant = %+v, want the document's own read+update — the drop took more than it should", got)
	}
	if doc.RowScope != principal.RowScopeTeam {
		t.Errorf("row_scope = %q, want team", doc.RowScope)
	}
	// And it grants nothing, which is the whole reason dropping is safe.
	perms := Merge(map[string]Document{"admin": doc})
	if perms.Allows("ext_departed_note", principal.ActionRead) {
		t.Error("the dropped object still allows an action")
	}
}

// TestParseDropsAnUnknownCoreLikeObjectToo: the leniency is about the
// VOCABULARY, not about extensions. A typo'd core object is the case the old
// strictness was written for, and it is still answered — the grant does
// nothing — but by a log line rather than by refusing to authenticate the user.
func TestParseDropsAnUnknownCoreLikeObjectToo(t *testing.T) {
	doc, err := Parse([]byte(`{"objects":{"invoice":{"read":true}},"row_scope":"all"}`))
	if err != nil {
		t.Fatalf("a typo'd object must not fail a login: %v", err)
	}
	if len(doc.Objects) != 0 {
		t.Errorf("objects = %v, want the typo dropped", doc.Objects)
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
		if merged.Allows("embedding_reindex", principal.ActionRead) {
			t.Errorf("role %q must not be able to read embedding_reindex (admin/ops-only)", key)
		}
		if merged.Allows("embedding_reindex", principal.ActionUpdate) {
			t.Errorf("role %q must not be able to trigger a reindex", key)
		}
	}
}

func TestRateObjectsAreAdminOnly(t *testing.T) {
	for _, obj := range []string{"fx_rate", "ai_model_rate"} {
		// Admin/ops may create, read, and same-day-correct (update) — but the
		// sheets are strict append-forward, so NO role holds delete.
		for _, key := range []string{"admin", "ops"} {
			doc, err := Parse(MustDefaultJSON(key))
			if err != nil {
				t.Fatalf("role %q: %v", key, err)
			}
			merged := Merge(map[string]Document{key: doc})
			for _, act := range []principal.Action{
				principal.ActionCreate, principal.ActionRead, principal.ActionUpdate,
			} {
				if !merged.Allows(obj, act) {
					t.Errorf("role %q should have %s on %q", key, act, obj)
				}
			}
			if merged.Allows(obj, principal.ActionDelete) {
				t.Errorf("role %q must not delete %q (append-forward, no delete surface)", key, obj)
			}
		}
		// Every non-admin/ops role is denied ALL four actions — the editor is
		// org-gated in the SPA, so these roles have no legitimate consumer.
		for _, key := range []string{"manager", "rep", "read_only"} {
			doc, err := Parse(MustDefaultJSON(key))
			if err != nil {
				t.Fatalf("role %q: %v", key, err)
			}
			merged := Merge(map[string]Document{key: doc})
			for _, act := range []principal.Action{
				principal.ActionCreate, principal.ActionRead,
				principal.ActionUpdate, principal.ActionDelete,
			} {
				if merged.Allows(obj, act) {
					t.Errorf("role %q must not %s %q (admin/ops-only surface)", key, act, obj)
				}
			}
		}
	}
}

// Every seeded role that may WRITE an object may also READ it. The grant
// booleans are independent — Parse accepts a write-without-read document and
// Merge unions the four flags separately — so this is a property of the seeds,
// not of the shape, and it is the property the surfaces downstream lean on: a
// record read, a list, and the approvals inbox all gate on <object>.read before
// they show anything, so a seeded role holding create/update/delete without read
// could act on rows it can never be shown. Derived from the seeds themselves, so
// a role edited or added later is held to it without extending a list.
func TestNoSeededRoleGrantsAWriteWithoutRead(t *testing.T) {
	for roleKey, doc := range defaults {
		for _, object := range coreObjects {
			g := doc.Objects[object]
			writes := g.Create || g.Update || g.Delete
			if g.Read || !writes {
				continue
			}
			t.Errorf("role %q grants %q create=%v update=%v delete=%v with read=false — a write it can never "+
				"see the result of, and a staged change to it that no inbox may disclose",
				roleKey, object, g.Create, g.Update, g.Delete)
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
