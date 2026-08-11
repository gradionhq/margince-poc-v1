// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package identity

// The role editor's DB-free half: the document shape it writes, the decode it
// reads back with, and the two 404s it has to keep apart.

import (
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"github.com/gradionhq/margince/backend/internal/modules/identity/internal/policy"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
)

// The one that matters most, and the reason storedGrant exists at all: what the
// editor WRITES has to be what the authorization path READS. principal.
// ObjectGrant carries the same four booleans with no json tags, so a grant
// marshalled from it lands as {"Create":true,…} and policy.Parse — which decodes
// lower-case — reads four falses. The write would succeed, the audit row would
// look right, and the member would still be denied.
func TestAStoredGrantRoundTripsThroughThePolicyParserThatEnforcesIt(t *testing.T) {
	raw, err := json.Marshal(storedGrant{Create: true, Read: true, Update: false, Delete: true})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	doc, err := policy.Parse([]byte(`{"row_scope":"team","objects":{"deal":` + string(raw) + `}}`))
	if err != nil {
		t.Fatalf("policy.Parse: %v", err)
	}
	got := doc.Objects["deal"]
	// Read through the merge, which is the shape the gate actually consults.
	merged := policy.Merge(map[string]policy.Document{"rep": doc}).Objects["deal"]
	if !merged.Create || !merged.Read || merged.Update || !merged.Delete {
		t.Fatalf("the grant the editor wrote resolved to %+v after Parse+Merge; "+
			"want create+read+delete and no update (%+v was parsed)", merged, got)
	}
}

func TestDecodeRoleObjectsAnswersAnEmptyMapRatherThanNilForADocumentWithNoGrants(t *testing.T) {
	for name, raw := range map[string][]byte{
		"absent column": nil,
		"empty object":  []byte(`{}`),
		"no objects":    []byte(`{"row_scope":"own"}`),
		"empty objects": []byte(`{"objects":{}}`),
	} {
		got, err := decodeRoleObjects(raw)
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		// Nil would reach the wire as `null`, which reads as "unknown". What is
		// known is that this role grants nothing.
		if got == nil {
			t.Errorf("%s: decoded nil, want an empty non-nil map", name)
		}
	}
}

// A grant on an object this installation cannot name SURVIVES the read. It is
// what an operator has to see in order to clear it — policy.Parse drops it at
// authentication time so it authorizes nothing, and hiding it here would leave
// the operator with a document they cannot reconcile with what the screen shows.
func TestDecodeRoleObjectsKeepsAGrantThisInstallationCannotName(t *testing.T) {
	got, err := decodeRoleObjects([]byte(`{"objects":{"ext_gone_thing":{"read":true},"deal":{"read":true}}}`))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if _, ok := got["ext_gone_thing"]; !ok {
		t.Errorf("the removed unit's grant was dropped; got %v", got)
	}
	if !got["ext_gone_thing"].Read {
		t.Error("the removed unit's grant lost its read verb")
	}
}

// Unreadable bytes are an ERROR, not an empty map. The caller is about to be
// shown, or to edit, what the row says; answering "no grants" for a document
// nobody can read would be a lie in the safe direction on the one screen whose
// job is to report the document honestly.
func TestDecodeRoleObjectsRefusesADocumentItCannotRead(t *testing.T) {
	if _, err := decodeRoleObjects([]byte(`not json`)); err == nil {
		t.Fatal("an unreadable permissions document decoded without error")
	}
}

// The two 404s this surface answers must stay distinguishable: an admin who
// mistyped a ROLE and one who mistyped an OBJECT have to look in different
// places, and a client that has to say which needs the code, not the prose.
func TestTheTwoNotFoundRefusalsCarryDistinctCodes(t *testing.T) {
	cases := map[string]struct {
		err      error
		wantCode string
	}{
		"unknown role":   {errUnknownRole, "unknown_role"},
		"unknown object": {errUnknownObject, "unknown_object"},
	}
	for name, c := range cases {
		// Applied in the same sequence the handler applies them, so the test
		// covers the composition rather than each refusal in isolation.
		rendered := unknownObjectRefusal(unknownRoleRefusal(c.err))
		var detailed *httperr.DetailedError
		if !errors.As(rendered, &detailed) {
			t.Errorf("%s: rendered as %T, want a DetailedError carrying a code", name, rendered)
			continue
		}
		if detailed.Status != http.StatusNotFound || detailed.Code != c.wantCode {
			t.Errorf("%s: %d/%q, want 404/%q", name, detailed.Status, detailed.Code, c.wantCode)
		}
	}
	// Anything else passes through untouched — the pair must not swallow an
	// unrelated failure into a 404 that says the role does not exist.
	other := errors.New("connection reset")
	if got := unknownObjectRefusal(unknownRoleRefusal(other)); !errors.Is(got, other) {
		t.Errorf("an unrelated error was rewritten to %v", got)
	}
	// Both still read as ErrNotFound for anything matching on the taxonomy.
	if !errors.Is(errUnknownObject, apperrors.ErrNotFound) {
		t.Error("errUnknownObject does not wrap ErrNotFound and would not render 404 unrendered")
	}
}

func TestWireRoleCarriesTheStoredGrantsAndNeverANullMap(t *testing.T) {
	wire := wireRole(roleRow{
		Key: "rep", Name: "Rep", IsSystem: true, Version: 7,
		Objects: map[string]storedGrant{"ext_notes_note": {Read: true, Create: true}},
	})
	if wire.Key != "rep" || wire.Name != "Rep" || !wire.IsSystem {
		t.Errorf("identity fields lost: %+v", wire)
	}
	// The version has to ride the row: it is what the client echoes in If-Match,
	// and a response that dropped it would leave every subsequent write
	// unguarded while looking like it had a guard available.
	if wire.Version != 7 {
		t.Errorf("version = %d, want 7", wire.Version)
	}
	got := wire.Objects["ext_notes_note"]
	if !got.Read || !got.Create || got.Update || got.Delete {
		t.Errorf("grant = %+v, want read+create only", got)
	}
	// A role with no grants maps to `{}`, not `null` — see decodeRoleObjects.
	if empty := wireRole(roleRow{Key: "k"}).Objects; empty == nil {
		t.Error("a role with no grants wired a nil objects map")
	}
}
