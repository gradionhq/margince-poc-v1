// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package datasource

import (
	"encoding/json"
	"errors"
	"fmt"
	"testing"
)

// domainInput mirrors the contract's OrganizationDomainInput: an array element
// that is an OBJECT, which is the shape two reported sessions sent as a string.
type domainInput struct {
	Domain    string `json:"domain"`
	IsPrimary *bool  `json:"is_primary,omitempty"`
}

type addressInput struct {
	Line1 *string `json:"line1,omitempty"`
	City  *string `json:"city,omitempty"`
}

// orgPatch stands in for a generated request struct. It carries the catch-all
// map oapi-codegen emits for `additionalProperties`, because that field is what
// makes the generator write a custom UnmarshalJSON — and that unmarshaler is why
// the decoder loses the field path in the first place.
type orgPatch struct {
	DisplayName          *string        `json:"display_name,omitempty"`
	Industry             *string        `json:"industry,omitempty"`
	Domains              *[]domainInput `json:"domains,omitempty"`
	Address              *addressInput  `json:"address,omitempty"`
	Count                *int           `json:"count,omitempty"`
	AdditionalProperties map[string]any `json:"-"`
}

// UnmarshalJSON reproduces oapi-codegen's per-field decode: every field goes
// through a FRESH json.Unmarshal, whose error context starts empty. That is the
// whole mechanism — json.UnmarshalTypeError.Field comes back "" and the path
// survives only in this wrapper's prose.
func (o *orgPatch) UnmarshalJSON(b []byte) error {
	object := make(map[string]json.RawMessage)
	if err := json.Unmarshal(b, &object); err != nil {
		return err
	}
	for key, target := range map[string]any{
		"display_name": &o.DisplayName,
		"industry":     &o.Industry,
		"domains":      &o.Domains,
		"address":      &o.Address,
		"count":        &o.Count,
	} {
		raw, found := object[key]
		if !found {
			continue
		}
		if err := json.Unmarshal(raw, target); err != nil {
			return fmt.Errorf("error reading '%s': %w", key, err)
		}
		delete(object, key)
	}
	return nil
}

// The refusal a caller reads names the field they sent and the shape it holds.
//
// Before localization every one of these answered "the payload must be a JSON
// object, not a string" — about a payload that IS an object — and a reported
// session read that as a transport bug and filed one.
func TestStrictDecode_namesTheFieldAndTheShapeItHolds(t *testing.T) {
	for _, tc := range []struct {
		name, raw, want string
	}{
		{
			name: "an array of strings where the items are objects",
			raw:  `{"domains":["openrouter.ai"]}`,
			want: "`domains` must be an array of objects, not an array of strings; " +
				`each item is {domain: string, is_primary?: boolean}`,
		},
		{
			name: "the bad field is found among good ones",
			raw:  `{"display_name":"OpenRouter","industry":"Software","domains":["openrouter.ai"]}`,
			want: "`domains` must be an array of objects, not an array of strings; " +
				`each item is {domain: string, is_primary?: boolean}`,
		},
		{
			name: "a string where an object belongs sketches the object",
			raw:  `{"address":"1 Main St"}`,
			want: "`address` must be an object, not a string; " +
				`it takes {line1?: string, city?: string}`,
		},
		{
			name: "a scalar of the wrong type names both types",
			raw:  `{"industry":123}`,
			want: "`industry` must be a string, not a number",
		},
		{
			name: "a string where an integer belongs",
			raw:  `{"count":"12"}`,
			want: "`count` must be an integer, not a string",
		},
		{
			name: "an array where a scalar belongs",
			raw:  `{"industry":["Software"]}`,
			want: "`industry` must be a string, not an array of strings",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var into orgPatch
			err := StrictDecode(json.RawMessage(tc.raw), &into)
			if err == nil {
				t.Fatalf("%s was accepted", tc.raw)
			}
			var refusal *FieldShapeError
			if !errors.As(err, &refusal) {
				t.Fatalf("err = %v, want a FieldShapeError", err)
			}
			if refusal.Error() != tc.want {
				t.Errorf("refusal =\n  %s\nwant\n  %s", refusal.Error(), tc.want)
			}
		})
	}
}

// The correct shape still decodes. A refusal that also refuses the fix is worse
// than the refusal it replaced.
func TestStrictDecode_acceptsTheShapeTheRefusalAsksFor(t *testing.T) {
	var into orgPatch
	if err := StrictDecode(json.RawMessage(`{"domains":[{"domain":"openrouter.ai","is_primary":true}]}`), &into); err != nil {
		t.Fatalf("the shape the refusal names was refused: %v", err)
	}
	if into.Domains == nil || len(*into.Domains) != 1 || (*into.Domains)[0].Domain != "openrouter.ai" {
		t.Errorf("domains = %+v, want the one item that was sent", into.Domains)
	}
}

// activityBody stands in for a contract type with NO additionalProperties (an
// activity carries no custom fields), so no custom unmarshaler is generated and
// DisallowUnknownFields reaches all the way into the array items.
type activityBody struct {
	Kind  string      `json:"kind"`
	Links *[]linkItem `json:"links,omitempty"`
}

type linkItem struct {
	EntityID   string `json:"entity_id"`
	EntityType string `json:"entity_type"`
}

// A value of the RIGHT shape refused for another reason says so, rather than
// naming a shape the caller already got right. The caller still learns which
// field and what it holds, which is the half they can act on.
//
// This is the second reported failure: an activity's `links` sent as an array of
// objects whose KEYS were guessed. The array of objects was an array of objects,
// and "not an array of objects" would tell the caller their value is wrong for
// being right.
func TestStrictDecode_separatesAWrongShapeFromARefusedValue(t *testing.T) {
	var into activityBody
	err := StrictDecode(json.RawMessage(`{"kind":"email","links":[{"organization_id":"019f"}]}`), &into)
	if err == nil {
		t.Fatal("a key the link item does not declare was accepted")
	}
	var refusal *FieldShapeError
	if !errors.As(err, &refusal) {
		t.Fatalf("err = %v, want a FieldShapeError", err)
	}
	if refusal.Got != "" {
		t.Errorf("Got = %q, want it empty — the array of objects WAS an array of objects", refusal.Got)
	}
	want := "`links` must be an array of objects but the value sent was not accepted; " +
		`each item is {entity_id: string, entity_type: string}`
	if refusal.Error() != want {
		t.Errorf("refusal =\n  %s\nwant\n  %s", refusal.Error(), want)
	}
}

// A payload that is genuinely not an object keeps the sentence that is exactly
// right for it. Localization fills a gap; it does not take over.
func TestStrictDecode_leavesAWholePayloadShapeFailureAlone(t *testing.T) {
	var into orgPatch
	err := StrictDecode(json.RawMessage(`"not an object"`), &into)
	if err == nil {
		t.Fatal("a string payload was accepted")
	}
	var refusal *FieldShapeError
	if errors.As(err, &refusal) {
		t.Errorf("refusal = %v, want no field named — no field was at fault", refusal)
	}
}

// The decoder sometimes keeps a path this walk cannot reach, because it reaches
// INSIDE a nested value. Its answer is the more precise one and must survive.
func TestLocalizeFieldFault_defersToAPathTheDecoderKept(t *testing.T) {
	// A plain struct (no catch-all, so no generated unmarshaler) is where
	// encoding/json still fills Field, and it fills it with a dotted path.
	type link struct {
		EntityID   int    `json:"entity_id"`
		EntityType string `json:"entity_type"`
	}
	type body struct {
		Links []link `json:"links"`
	}
	raw := json.RawMessage(`{"links":[{"entity_id":"nope","entity_type":"deal"}]}`)
	var into body
	err := StrictDecode(raw, &into)
	if err == nil {
		t.Fatal("a string where an integer belongs was accepted")
	}
	var typeErr *json.UnmarshalTypeError
	if !errors.As(err, &typeErr) || typeErr.Field == "" {
		t.Fatalf("this case is only meaningful while the decoder keeps a path; err = %v", err)
	}
	if got := LocalizeFieldFault(raw, &into, err, strictProbe); got != nil {
		t.Errorf("localized to %q, want nil — the decoder already knew %q, which is deeper",
			got.Field, typeErr.Field)
	}
}

// Two bad fields must produce the SAME refusal on every identical request. Map
// iteration is not ordered, so without sorting a caller fixing one mistake
// cannot tell progress from churn.
func TestLocalizeFieldFault_namesTheSameFieldEveryTime(t *testing.T) {
	raw := json.RawMessage(`{"industry":123,"count":"12"}`)
	first := ""
	for range 20 {
		var into orgPatch
		err := StrictDecode(raw, &into)
		var refusal *FieldShapeError
		if !errors.As(err, &refusal) {
			t.Fatalf("err = %v, want a FieldShapeError", err)
		}
		if first == "" {
			first = refusal.Field
		}
		if refusal.Field != first {
			t.Fatalf("named %q after naming %q for the same payload", refusal.Field, first)
		}
	}
	if first != "count" {
		t.Errorf("named %q, want the first key in sorted order", first)
	}
}

// A nested array names itself once. `[][]string` pluralized only its tail, so
// the refusal read "an array of array of strings" — a shape no caller could map
// onto what they sent.
func TestStrictDecode_namesANestedArrayWithoutRepeatingItself(t *testing.T) {
	type nested struct {
		Grid *[][]string `json:"grid,omitempty"`
	}
	var into nested
	err := StrictDecode(json.RawMessage(`{"grid":"not a grid"}`), &into)
	if err == nil {
		t.Fatal("a string where a nested array belongs was accepted")
	}
	var refusal *FieldShapeError
	if !errors.As(err, &refusal) {
		t.Fatalf("err = %v, want a FieldShapeError", err)
	}
	if want := "`grid` must be an array of arrays of strings, not a string"; refusal.Error() != want {
		t.Errorf("refusal = %q, want %q", refusal.Error(), want)
	}
}
