// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package extension_test

// The four documents encoding/json accepts that a published schema does not,
// and the one it accepts that is not an object at all. Each is a way a caller
// puts a value past both the schema and the reviewer reading the first copy.

import (
	"encoding/json"
	"testing"

	"github.com/gradionhq/margince/backend/pkg/extension"
)

type args struct {
	Body   string `json:"body"`
	Kind   string `json:"kind"`
	Nested nested `json:"nested"`
}

type nested struct {
	Token string   `json:"token"`
	Tags  []string `json:"tags"`
}

func TestDecodeArgsReadsTheDeclaredDocument(t *testing.T) {
	got, err := extension.DecodeArgs[args](json.RawMessage(`{"body":"a note","kind":"note"}`))
	if err != nil {
		t.Fatalf("a declared document was refused: %v", err)
	}
	if got.Body != "a note" || got.Kind != "note" {
		t.Fatalf("decoded %+v, want the document's own values", got)
	}
}

func TestDecodeArgsRefusesWhatTheSchemaDoesNot(t *testing.T) {
	for name, document := range map[string]string{
		"an unknown member":              `{"bdy":"a note"}`,
		"a case-variant member":          `{"Body":"a note"}`,
		"a member twice":                 `{"body":"first","body":"second"}`,
		"a null document":                `null`,
		"an array":                       `["body"]`,
		"a second value after the first": `{"body":"first"} {"body":"second"}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := extension.DecodeArgs[args](json.RawMessage(document)); err == nil {
				t.Fatalf("the decoder accepted %s", name)
			}
		})
	}
}

// A member twice is the one worth stating on its own: unmarshalling into a map
// COLLAPSES the pair, so a check written that way would see one member and pass
// while the decoder quietly kept the last — which is how a value gets past a
// reviewer who read the first.
func TestDecodeArgsRefusesARepeatedMemberRatherThanKeepingOne(t *testing.T) {
	got, err := extension.DecodeArgs[args](json.RawMessage(`{"body":"first","body":"second"}`))
	if err == nil {
		t.Fatalf("a repeated member decoded to %+v", got)
	}
	if got.Body != "" {
		t.Errorf("the refusal still returned a value: %+v", got)
	}
}

// The same hole one level down, which the type-driven scan cannot see: a
// nested value is skipped as raw bytes, and encoding/json quietly keeps the
// last copy. That is exactly how a value gets past a reviewer who read the
// first one.
func TestDecodeArgsRefusesARepeatedMemberAtAnyDepth(t *testing.T) {
	for name, document := range map[string]string{
		"nested twice":            `{"nested":{"token":"reviewed","token":"attacker"}}`,
		"inside an array element": `{"nested":{"tags":["a"]},"body":"x","kind":"y"}`,
		"deep inside an array":    `{"nested":{"token":"t"},"body":"x"}`,
	} {
		t.Run(name, func(t *testing.T) {
			_, err := extension.DecodeArgs[args](json.RawMessage(document))
			// Only the first document is a repetition; the other two are
			// well-formed and must still be accepted, or this check would be
			// refusing ordinary nesting.
			if name == "nested twice" && err == nil {
				t.Fatal("a member repeated inside a nested object was accepted")
			}
			if name != "nested twice" && err != nil {
				t.Fatalf("an ordinary nested document was refused: %v", err)
			}
		})
	}
}

// An empty document is the decoder's to refuse or accept by its own rules —
// what matters here is that this check does not invent an answer for it, since
// an operation taking no arguments is served the empty object.
func TestDecodeArgsAcceptsAnEmptyObjectForAnOperationWithNoArguments(t *testing.T) {
	if _, err := extension.DecodeArgs[struct{}](json.RawMessage(`{}`)); err != nil {
		t.Fatalf("the empty object was refused: %v", err)
	}
}

func TestIsCanonicalUUID(t *testing.T) {
	for value, want := range map[string]bool{
		"7c9e6679-7425-40de-944b-e07fc1f90ae7":   true,
		"7C9E6679-7425-40DE-944B-E07FC1F90AE7":   true,
		"7c9e6679742540de944be07fc1f90ae7":       false,
		"{7c9e6679-7425-40de-944b-e07fc1f90ae7}": false,
		"urn:uuid:7c9e6679-7425-40de-944b-e07f":  false,
		"7c9e6679-7425-40de-944b-e07fc1f90ae":    false,
		"":                                       false,
		"zzzzzzzz-7425-40de-944b-e07fc1f90ae7":   false,
	} {
		if got := extension.IsCanonicalUUID(value); got != want {
			t.Errorf("IsCanonicalUUID(%q) = %v, want %v", value, got, want)
		}
	}
}

// The scan walks arrays as well as objects, so a repetition inside an array
// ELEMENT is caught too — and an array of plain values is not mistaken for a
// set of member names.
func TestDecodeArgsWalksArrayElements(t *testing.T) {
	type element struct {
		Name string `json:"name"`
	}
	type withList struct {
		Items []element `json:"items"`
	}
	if _, err := extension.DecodeArgs[withList](json.RawMessage(`{"items":[{"name":"a"},{"name":"b"}]}`)); err != nil {
		t.Fatalf("an ordinary array of objects was refused: %v", err)
	}
	if _, err := extension.DecodeArgs[withList](json.RawMessage(`{"items":[{"name":"a","name":"b"}]}`)); err == nil {
		t.Fatal("a member repeated inside an array element was accepted")
	}
	type withStrings struct {
		Tags []string `json:"tags"`
	}
	// Both arrangements, because only the second one can fail: a scan that
	// reads a string as a member name whenever a value follows it accepts
	// ["a","a"] — the repeat is LAST, so nothing follows it — and refuses the
	// same repeat with an element after it. One case is not this rule.
	for _, doc := range []string{`{"tags":["a","a"]}`, `{"tags":["a","a","b"]}`} {
		if _, err := extension.DecodeArgs[withStrings](json.RawMessage(doc)); err != nil {
			t.Errorf("a repeated VALUE in an array was refused: %s: %v — only member names may not repeat", doc, err)
		}
	}
}

// A member's VALUE may spell a member name, in its own object or a sibling's,
// because a name and a value arrive as the same token and only the position
// they arrive in tells them apart.
func TestDecodeArgsAcceptsAValueThatSpellsAMemberName(t *testing.T) {
	type message struct {
		Kind string `json:"kind"`
		Body string `json:"body"`
	}
	if _, err := extension.DecodeArgs[message](json.RawMessage(`{"kind":"body","body":"x"}`)); err != nil {
		t.Fatalf("a value equal to a declared name was read as a second copy of that name: %v", err)
	}
	type nested struct {
		Config json.RawMessage `json:"config"`
		Label  string          `json:"label"`
	}
	if _, err := extension.DecodeArgs[nested](json.RawMessage(`{"config":{"label":"label"},"label":"config"}`)); err != nil {
		t.Fatalf("a nested value repeating an outer name was refused: %v", err)
	}
	// And the property that costs: a name genuinely repeated one level down is
	// still refused, in either position within its object.
	for _, doc := range []string{
		`{"config":{"a":"1","a":"2"},"label":"x"}`,
		`{"config":{"a":"1","a":"2","b":"3"},"label":"x"}`,
	} {
		if _, err := extension.DecodeArgs[nested](json.RawMessage(doc)); err == nil {
			t.Errorf("a nested member repeated was accepted: %s", doc)
		}
	}
}
