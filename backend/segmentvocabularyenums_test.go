// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package backendarch

// The filter-vocabulary read tells a caller which operators a field admits and
// which type it has (LVS-EXT-8). Both answers travel as contract enums while the
// authority for both lives in the predicate engine, so the two can drift — and
// the drift is silent in the worst way: the handler casts the engine's string
// straight into the enum, so a newly admitted operator would reach a client as a
// value its generated types cannot represent, and a newly REMOVED one would sit
// in the contract advertising something the engine refuses.
//
// Neither side is restated here. The contract side is read out of api/crm.yaml,
// the engine side out of storekit's own matrix, and the test compares them.
//
// Textual extraction rather than a YAML library, for the reason
// TestContractRefsResolve gives: this package stays free of parser deps so the
// arch-lint boundary holds.

import (
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/ports/fieldcatalog"
)

var (
	schemaHeading  = regexp.MustCompile(`^    ([A-Za-z0-9_.-]+):[ \t]*$`)
	propertyName   = regexp.MustCompile(`^        ([a-z_]+):[ \t]*$`)
	inlineEnumList = regexp.MustCompile(`enum:\s*\[([^\]]*)\]`)
)

// segmentVocabularyFieldEnums reads the inline enums of the
// SegmentVocabularyField schema, keyed by the property they belong to. A
// property's enum may sit directly under it (`type`) or one level down under
// `items` (`operators`); both attribute to the last property seen, which is what
// makes this independent of the order the properties appear in.
func segmentVocabularyFieldEnums(t *testing.T) map[string][]string {
	t.Helper()
	raw, err := os.ReadFile("api/crm.yaml")
	if err != nil {
		t.Fatalf("read contract: %v", err)
	}
	enums := map[string][]string{}
	inSchema := false
	property := ""
	for _, line := range strings.Split(string(raw), "\n") {
		if heading := schemaHeading.FindStringSubmatch(line); heading != nil {
			// Any next schema heading ends this one, so a renamed or deleted
			// schema yields no enums and the assertions below report that
			// rather than silently passing on an empty map.
			inSchema = heading[1] == "SegmentVocabularyField"
			property = ""
			continue
		}
		if !inSchema {
			continue
		}
		if name := propertyName.FindStringSubmatch(line); name != nil {
			property = name[1]
			continue
		}
		if values := inlineEnumList.FindStringSubmatch(line); values != nil && property != "" {
			for _, v := range strings.Split(values[1], ",") {
				if v = strings.TrimSpace(v); v != "" {
					enums[property] = append(enums[property], v)
				}
			}
		}
	}
	return enums
}

func TestTheVocabularysOperatorEnumIsExactlyWhatTheEngineAdmits(t *testing.T) {
	contract := setOfStrings(segmentVocabularyFieldEnums(t)["operators"])
	if len(contract) == 0 {
		t.Fatal("no operators enum found on SegmentVocabularyField — the schema was renamed, moved, or the enum is no longer inline")
	}
	engine := map[string]bool{}
	for _, fieldType := range everyFilterableFieldType() {
		for _, op := range storekit.OperatorsFor(fieldType) {
			engine[op] = true
		}
	}
	compareSets(t, "operator", engine, contract,
		"the engine admits it and the contract cannot carry it, so the vocabulary would report a value no client can read",
		"the contract advertises it and no field type admits it, so no field can ever report it")
}

func TestTheVocabularysTypeEnumIsExactlyWhatIsFilterable(t *testing.T) {
	contract := setOfStrings(segmentVocabularyFieldEnums(t)["type"])
	if len(contract) == 0 {
		t.Fatal("no type enum found on SegmentVocabularyField — the schema was renamed, moved, or the enum is no longer inline")
	}
	engine := map[string]bool{}
	for _, fieldType := range everyFilterableFieldType() {
		engine[string(fieldType)] = true
	}
	compareSets(t, "field type", engine, contract,
		"it is filterable and the contract cannot spell it",
		"the contract advertises it and nothing filterable carries it")
}

// everyFilterableFieldType is the six custom-field types plus id, which only a
// core field carries. Derived from fieldcatalog.Types() so a seventh custom type
// joins these gates by existing.
func everyFilterableFieldType() []storekit.FieldType {
	types := []storekit.FieldType{storekit.FieldID}
	for _, declared := range fieldcatalog.Types() {
		types = append(types, storekit.FieldType(declared))
	}
	return types
}

func setOfStrings(values []string) map[string]bool {
	set := make(map[string]bool, len(values))
	for _, v := range values {
		set[v] = true
	}
	return set
}

// compareSets reports each direction with the consequence of that direction,
// because the two failures are not the same bug and are not fixed the same way.
func compareSets(t *testing.T, subject string, engine, contract map[string]bool, missingWhy, extraWhy string) {
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
