// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package dealrooms

// A required id the caller omitted must be named as a missing argument, not
// discovered as a missing row.
//
// oapi-codegen renders a REQUIRED body id as a non-pointer openapi_types.UUID,
// and encoding/json leaves an absent key at the zero value with no error. A
// create that omitted deal_id would therefore reach the deal lookup with a zero
// UUID, match nothing, and answer 404 — telling the caller a deal they never
// named does not exist, which sends them looking for the wrong problem.
//
// The check belongs in the mapping rather than the handler because both
// transports decode the same shapes through it.

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/httperr/faulttest"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// requiredIDFields reports the wire names of every non-pointer UUID field on a
// contract request body. Non-pointer IS the generator's spelling of "required",
// which is what makes this derived rather than a list that rots.
func requiredIDFields(t reflect.Type) []string {
	uuidType := reflect.TypeFor[openapi_types.UUID]()
	var names []string
	for i := range t.NumField() {
		field := t.Field(i)
		if field.Type != uuidType {
			continue
		}
		name, _, _ := strings.Cut(field.Tag.Get("json"), ",")
		if name == "" || name == "-" {
			continue
		}
		names = append(names, name)
	}
	return names
}

// completeBody renders a body satisfying every required field except the one id
// under test, so a refusal can only be about that id.
func completeBody(t *testing.T, shape reflect.Type, omit string) map[string]any {
	t.Helper()
	body := map[string]any{}
	for i := range shape.NumField() {
		field := shape.Field(i)
		name, opts, _ := strings.Cut(field.Tag.Get("json"), ",")
		if name == "" || name == "-" || strings.Contains(opts, "omitempty") || name == omit {
			continue
		}
		switch field.Type {
		case reflect.TypeFor[openapi_types.UUID]():
			body[name] = ids.NewV7().String()
		case reflect.TypeFor[string]():
			body[name] = "probe"
		case reflect.TypeFor[int]():
			body[name] = 1
		default:
			t.Fatalf("%s.%s is a required %s this probe cannot populate — extend completeBody",
				shape.Name(), field.Name, field.Type)
		}
	}
	return body
}

func TestEveryRequiredBodyIDIsNamedWhenAbsent(t *testing.T) {
	shape := reflect.TypeFor[crmcontracts.CreateDealRoomRequest]()
	fields := requiredIDFields(shape)
	if len(fields) == 0 {
		t.Fatal("CreateDealRoomRequest declares no required id — the probe is asserting nothing")
	}
	for _, field := range fields {
		t.Run(field, func(t *testing.T) {
			raw, err := json.Marshal(completeBody(t, shape, field))
			if err != nil {
				t.Fatalf("marshal probe body: %v", err)
			}
			var req crmcontracts.CreateDealRoomRequest
			if err := json.Unmarshal(raw, &req); err != nil {
				t.Fatalf("probe body does not decode: %v", err)
			}
			_, err = createInput(req)
			faulttest.AssertNamesOmittedID(t, err, field)
		})
	}
}

func TestAnAddedDocumentNamesItsOmittedAttachment(t *testing.T) {
	shape := reflect.TypeFor[crmcontracts.AddDealRoomDocumentRequest]()
	fields := requiredIDFields(shape)
	if len(fields) == 0 {
		t.Fatal("AddDealRoomDocumentRequest declares no required id — the probe is asserting nothing")
	}
	for _, field := range fields {
		t.Run(field, func(t *testing.T) {
			body := completeBody(t, shape, field)
			body["group_key"] = "legal"
			raw, err := json.Marshal(body)
			if err != nil {
				t.Fatalf("marshal probe body: %v", err)
			}
			var req crmcontracts.AddDealRoomDocumentRequest
			if err := json.Unmarshal(raw, &req); err != nil {
				t.Fatalf("probe body does not decode: %v", err)
			}
			_, err = addDocumentInput(req)
			faulttest.AssertNamesOmittedID(t, err, field)
		})
	}
}
