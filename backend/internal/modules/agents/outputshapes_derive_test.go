// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package agents

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// The deriver's refusals. Each is a type that would otherwise be advertised as
// something looser than it is, and the walk has to carry the refusal OUT of
// whatever it was nested in — a bad element type inside a slice or a map is
// still a schema this surface cannot honestly publish.
func TestTheDeriverRefusesATypeItCannotDescribe(t *testing.T) {
	for name, typ := range map[string]reflect.Type{
		"a bare channel":      reflect.TypeOf(make(chan int)),
		"a slice of channels": reflect.TypeOf([]chan int(nil)),
		"a map of channels":   reflect.TypeOf(map[string]chan int(nil)),
		"a struct holding one": reflect.TypeOf(struct {
			Ch chan int `json:"ch"`
		}{}),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := describeType(typ); err == nil {
				t.Error("described a type with no JSON rendering rather than refusing it")
			}
		})
	}
}

// A field with no json tag would go on the wire under its GO name, which is a
// name nothing else on this surface uses. That is a defect in the result type,
// and the deriver says so rather than publishing it.
func TestAnUntaggedExportedFieldIsRefused(t *testing.T) {
	_, err := describeType(reflect.TypeOf(struct {
		Untagged string
	}{}))
	if err == nil || !strings.Contains(err.Error(), "no json tag") {
		t.Errorf("err = %v, want a refusal naming the untagged field", err)
	}
}

// A field the wire never carries is not part of the shape: an unexported one,
// and one tagged out. Describing either would advertise a member no result has.
func TestFieldsTheWireNeverCarriesAreNotDescribed(t *testing.T) {
	schema, err := describeType(reflect.TypeOf(struct {
		Kept    string `json:"kept"`
		Skipped string `json:"-"`
		hidden  string
	}{}))
	if err != nil {
		t.Fatalf("describing the struct: %v", err)
	}
	if _, named := schema.Properties["kept"]; !named {
		t.Error("the tagged field was not described")
	}
	for _, absent := range []string{"Skipped", "-", "hidden"} {
		if _, named := schema.Properties[absent]; named {
			t.Errorf("%q was described, but it never reaches the wire", absent)
		}
	}
}

// json.RawMessage holds a document this surface did not build, so the honest
// schema for it says "an object" and stops — describing it as the []byte it is
// would advertise an array of integers.
func TestARawMessageIsDescribedAsAnObjectAndNotAsItsBytes(t *testing.T) {
	schema, err := describeType(reflect.TypeOf(json.RawMessage(nil)))
	if err != nil {
		t.Fatalf("describing a raw message: %v", err)
	}
	if schema.Type != schemaObject || schema.Items != nil {
		t.Errorf("schema = %+v, want a bare object", schema)
	}
}

// The number kinds, because a result carrying a count and one carrying an
// amount must not be advertised the same way as one carrying a rate.
func TestScalarKindsAreDescribedAsTheirWireTypes(t *testing.T) {
	for want, value := range map[string]any{
		schemaInteger: struct {
			V int64 `json:"v"`
		}{},
		schemaNumber: struct {
			V float64 `json:"v"`
		}{},
		schemaBoolean: struct {
			V bool `json:"v"`
		}{},
		schemaString: struct {
			V string `json:"v"`
		}{},
	} {
		schema, err := describeType(reflect.TypeOf(value))
		if err != nil {
			t.Fatalf("describing %s: %v", want, err)
		}
		if got := schema.Properties["v"].Type; got != want {
			t.Errorf("described as %q, want %q", got, want)
		}
	}
}
