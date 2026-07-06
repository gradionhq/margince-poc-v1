// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package datasource

import (
	"encoding/json"
	"errors"
	"testing"
)

// A patch field key must match a contract field name byte-for-byte.
// encoding/json matches struct fields case-insensitively, so without this
// gate `{"FULL_NAME":…}` would decode into `full_name` and write the
// column — while the human-edit-precedence probe, matching audit keys
// case-sensitively in jsonb, cleared the same patch as touching no
// human-owned field. That gap let an agent overwrite a human-typed value
// through the 🟢 path under a differently-cased key.
func TestStrictDecodeRefusesCaseVariantFieldKeys(t *testing.T) {
	type leadLike struct {
		FullName *string `json:"full_name,omitempty"`
		Score    *int    `json:"score,omitempty"`
	}

	var exact leadLike
	if err := StrictDecode(json.RawMessage(`{"full_name":"Ada"}`), &exact); err != nil {
		t.Fatalf("exact-case key must decode: %v", err)
	}
	if exact.FullName == nil || *exact.FullName != "Ada" {
		t.Fatalf("exact-case key did not populate the field: %+v", exact)
	}

	for _, variant := range []string{
		`{"FULL_NAME":"attacker"}`,
		`{"Full_Name":"attacker"}`,
		`{"Score":9}`,
	} {
		var got leadLike
		err := StrictDecode(json.RawMessage(variant), &got)
		if err == nil {
			t.Fatalf("case-variant key %s was accepted; the store would write it while the probe cleared it", variant)
		}
		var decErr *FieldDecodeError
		if !errors.As(err, &decErr) {
			t.Fatalf("case-variant key %s: want FieldDecodeError (maps to 422), got %T: %v", variant, err, err)
		}
	}
}

// A target that carries an AdditionalProperties catch-all owns its own key
// policy (it routes and drops non-exact keys), so the exact-key gate must
// leave it alone — enforcing here would double-reject or fight the type's
// own unmarshaler. The catch-all is a `json:"-"` MAP; a plain `json:"-"`
// ignored field (a common Go idiom) must NOT disable the backstop.
func TestStrictDecodeDefersOnlyToMapCatchAll(t *testing.T) {
	type withMapCatchAll struct {
		FullName             *string                `json:"full_name,omitempty"`
		AdditionalProperties map[string]interface{} `json:"-"`
	}
	var lax withMapCatchAll
	if err := RejectNonCanonicalKeys(json.RawMessage(`{"anything":1}`), &lax); err != nil {
		t.Fatalf("a map catch-all target must accept arbitrary keys: %v", err)
	}

	type withIgnoredField struct {
		FullName *string `json:"full_name,omitempty"`
		internal int     `json:"-"`
	}
	var strict withIgnoredField
	_ = strict.internal
	if err := RejectNonCanonicalKeys(json.RawMessage(`{"FULL_NAME":"x"}`), &strict); err == nil {
		t.Fatal("a plain json:\"-\" ignored field must not turn off case-variant rejection")
	}
}

// encoding/json promotes an embedded struct's fields to the parent, so the
// exact-key gate must walk into anonymous struct fields — otherwise a
// promoted key is falsely rejected as unknown, and an embedded catch-all
// is missed. No contract type embeds today; this pins the behavior before
// one does.
func TestRejectNonCanonicalKeysWalksEmbeddedFields(t *testing.T) {
	type base struct {
		OwnerId *string `json:"owner_id,omitempty"`
	}
	type child struct {
		base
		FullName *string `json:"full_name,omitempty"`
	}
	var c child
	if err := RejectNonCanonicalKeys(json.RawMessage(`{"owner_id":"u1","full_name":"Ada"}`), &c); err != nil {
		t.Fatalf("promoted embedded key must be accepted: %v", err)
	}
	if err := RejectNonCanonicalKeys(json.RawMessage(`{"OWNER_ID":"u1"}`), &c); err == nil {
		t.Fatal("a case-variant of a promoted embedded key must be rejected")
	}
}
