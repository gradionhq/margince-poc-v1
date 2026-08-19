// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The filter-vocabulary read tells a caller which operators a field admits and
// which type it has (LVS-EXT-8). Both answers travel as contract enums while the
// authority for both lives in the predicate engine, so the two can drift — and
// the drift is silent in the worst way. The handler casts the engine's string
// straight into the enum, so a newly admitted operator would reach a client as a
// value its generated types cannot represent, and a newly REMOVED one would sit
// in the contract advertising something the engine refuses.
//
// Neither side is restated. The contract side is read out of the authoritative
// document, the engine side out of storekit's own matrix, and the test compares
// them in both directions — each with the consequence of that direction, because
// the two failures are different bugs with different fixes.
//
// It lives in compose rather than beside the handler because this is the one
// place that may hold both halves: the root fitness package may not import
// platform (go-arch-lint says so, and widening that for a test would trade an
// architectural boundary for a convenience), while compose already reads this
// contract and already depends on the engine.

import (
	"testing"

	"github.com/getkin/kin-openapi/openapi3"

	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/ports/fieldcatalog"
)

// vocabularyFieldEnum reads one inline enum off the FilterVocabularyField
// schema. property names the property; itemsLevel says whether the enum sits on
// the property itself (`type`) or on its array items (`operators`).
func vocabularyFieldEnum(t *testing.T, property string, itemsLevel bool) map[string]bool {
	t.Helper()
	doc, err := openapi3.NewLoader().LoadFromFile(contractFile)
	if err != nil {
		t.Fatalf("load contract: %v", err)
	}
	schema := doc.Components.Schemas["FilterVocabularyField"]
	if schema == nil || schema.Value == nil {
		t.Fatal("FilterVocabularyField is absent from the contract — the vocabulary read has no response shape")
	}
	prop := schema.Value.Properties[property]
	if prop == nil || prop.Value == nil {
		t.Fatalf("FilterVocabularyField has no %q property", property)
	}
	carrier := prop.Value
	if itemsLevel {
		if carrier.Items == nil || carrier.Items.Value == nil {
			t.Fatalf("FilterVocabularyField.%s declares no items, so it carries no enum", property)
		}
		carrier = carrier.Items.Value
	}
	if len(carrier.Enum) == 0 {
		t.Fatalf("FilterVocabularyField.%s is not a closed enum, so a client cannot know what to expect", property)
	}
	values := make(map[string]bool, len(carrier.Enum))
	for _, raw := range carrier.Enum {
		value, ok := raw.(string)
		if !ok {
			t.Fatalf("FilterVocabularyField.%s has a non-string enum member %#v", property, raw)
		}
		values[value] = true
	}
	return values
}

func TestTheVocabularysOperatorEnumIsExactlyWhatTheEngineAdmits(t *testing.T) {
	engine := map[string]bool{}
	for _, fieldType := range everyFilterableFieldType() {
		// Base-table fields: the contract enum has to carry every operator ANY
		// field can report, and a linked field only ever reports a subset of
		// what its own type admits.
		for _, op := range storekit.OperatorsFor(storekit.Field{Type: fieldType}) {
			engine[op] = true
		}
	}
	compareVocabularySets(t, "operator", engine, vocabularyFieldEnum(t, "operators", true),
		"the engine admits it and the contract cannot carry it, so the vocabulary would report a value no client can read",
		"the contract advertises it and no field type admits it, so no field can ever report it")
}

func TestTheVocabularysTypeEnumIsExactlyWhatIsFilterable(t *testing.T) {
	engine := map[string]bool{}
	for _, fieldType := range everyFilterableFieldType() {
		engine[string(fieldType)] = true
	}
	compareVocabularySets(t, "field type", engine, vocabularyFieldEnum(t, "type", false),
		"it is filterable and the contract cannot spell it",
		"the contract advertises it and nothing filterable carries it")
}

// everyFilterableFieldType is the six custom-field types plus id, which only a
// core field carries. Derived from fieldcatalog.Types() so a seventh custom type
// joins these gates by existing rather than by somebody remembering them.
func everyFilterableFieldType() []storekit.FieldType {
	types := []storekit.FieldType{storekit.FieldID}
	for _, declared := range fieldcatalog.Types() {
		types = append(types, storekit.FieldType(declared))
	}
	return types
}

func compareVocabularySets(t *testing.T, subject string, engine, contract map[string]bool, missingWhy, extraWhy string) {
	t.Helper()
	for value := range engine {
		if !contract[value] {
			t.Errorf("%s %q: %s", subject, value, missingWhy)
		}
	}
	for value := range contract {
		if !engine[value] {
			t.Errorf("%s %q: %s", subject, value, extraWhy)
		}
	}
}
