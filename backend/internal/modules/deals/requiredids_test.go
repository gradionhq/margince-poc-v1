// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package deals

// A required id the caller omitted must be named as a missing argument, not
// discovered as a missing row.
//
// The defect this closes: oapi-codegen renders a REQUIRED body id as a
// non-pointer openapi_types.UUID, and encoding/json leaves an absent key at the
// zero value with no error. So `create_record` for a deal reached
// ensureOpenBirthStage with two zero UUIDs, whose composite lookup matched
// nothing and answered a bare apperrors.ErrNotFound — a 404 naming no argument
// on REST and "not found" with nothing to fix on MCP. Both cases were reported
// as "cannot create a deal or a project at all", and they were.
//
// The mapping is where this belongs, not the handler: the HTTP handlers and the
// SoR provider (the MCP door) both decode the same crm.yaml shapes through these
// functions, so one check here answers both surfaces.
//
// Scope, stated exactly, because the first version of this file claimed more than
// it delivered: the FIELDS are derived from each contract type, but the two types
// are named here by hand. So a required id added to either body upstream fails
// here until the mapping validates it — and a required id on any OTHER contract
// body is not covered.
//
// That gap is real and it is enumerated rather than hidden:
// backend/requiredbodyids_test.go walks internal/contracts for every request body
// carrying a required non-pointer UUID and requires each to be either probed here
// or named as a known gap with a reason. A new one fails, so the list only shrinks.

import (
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"testing"

	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// requiredIDFields reports the wire names of every non-pointer UUID field on a
// contract request body. Non-pointer IS the generator's spelling of "required",
// which is what makes this derivable rather than a list.
func requiredIDFields(t reflect.Type) []string {
	uuidType := reflect.TypeFor[openapi_types.UUID]()
	var names []string
	for i := 0; i < t.NumField(); i++ {
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

// completeBody renders a body that satisfies every required field of t, so the
// only thing missing in each probe is the one id under test.
func completeBody(t *testing.T, shape reflect.Type, omit string) map[string]any {
	t.Helper()
	body := map[string]any{}
	for i := 0; i < shape.NumField(); i++ {
		field := shape.Field(i)
		tag := field.Tag.Get("json")
		name, opts, _ := strings.Cut(tag, ",")
		// Optional fields are left out entirely: a mapping must refuse the
		// missing REQUIRED id even on an otherwise minimal body.
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
	// Each entry binds a contract create body to the mapping that decodes it.
	// The mapping is called directly: what is under test is the refusal, and it
	// has to land before any store or database is involved.
	for _, tc := range []struct {
		name  string
		shape reflect.Type
		call  func(t *testing.T, body []byte) error
	}{
		{
			name:  "CreateDealRequest",
			shape: reflect.TypeFor[crmcontracts.CreateDealRequest](),
			call: func(t *testing.T, body []byte) error {
				t.Helper()
				var req crmcontracts.CreateDealRequest
				if err := json.Unmarshal(body, &req); err != nil {
					t.Fatalf("probe body does not decode: %v", err)
				}
				_, err := dealCreateInput(req)
				return err
			},
		},
		{
			name:  "CreateProjectRequest",
			shape: reflect.TypeFor[crmcontracts.CreateProjectRequest](),
			call: func(t *testing.T, body []byte) error {
				t.Helper()
				var req crmcontracts.CreateProjectRequest
				if err := json.Unmarshal(body, &req); err != nil {
					t.Fatalf("probe body does not decode: %v", err)
				}
				_, err := projectCreateInput(req)
				return err
			},
		},
		{
			// The asymmetry PR #370 created: advance_deal is guarded on the MCP
			// side at Registry.Invoke, so REST answered a bare 404 for the
			// identical mistake — one rule, two answers.
			name:  "AdvanceDealRequest",
			shape: reflect.TypeFor[crmcontracts.AdvanceDealRequest](),
			call: func(t *testing.T, body []byte) error {
				t.Helper()
				var req crmcontracts.AdvanceDealRequest
				if err := json.Unmarshal(body, &req); err != nil {
					t.Fatalf("probe body does not decode: %v", err)
				}
				_, err := advanceDealInput(req, nil)
				return err
			},
		},
		{
			name:  "CreateStageRequest",
			shape: reflect.TypeFor[crmcontracts.CreateStageRequest](),
			call: func(t *testing.T, body []byte) error {
				t.Helper()
				var req crmcontracts.CreateStageRequest
				if err := json.Unmarshal(body, &req); err != nil {
					t.Fatalf("probe body does not decode: %v", err)
				}
				_, err := stageCreateInput(req)
				return err
			},
		},
	} {
		fields := requiredIDFields(tc.shape)
		if len(fields) == 0 {
			t.Errorf("%s declares no required id — if that is now true, drop it from this walk "+
				"rather than leaving a probe that asserts nothing", tc.name)
		}
		for _, field := range fields {
			t.Run(tc.name+"/"+field, func(t *testing.T) {
				body, err := json.Marshal(completeBody(t, tc.shape, field))
				if err != nil {
					t.Fatalf("marshal probe body: %v", err)
				}
				assertNamesTheOmittedID(t, tc.call(t, body), field)
			})
		}
	}
}

// assertNamesTheOmittedID is the one assertion every probe makes: the refusal
// classifies as the caller's mistake and names the wire field they omitted.
//
// It asserts the OBSERVABLE property rather than a concrete error type, because
// there are two legitimate carriers — this module's RequiredFieldError for a
// required non-id field, and httperr.RequireBodyID for the ids — and a caller
// cannot tell them apart, which is the point.
func assertNamesTheOmittedID(t *testing.T, err error, field string) {
	t.Helper()
	if err == nil {
		t.Fatalf("an absent %s was accepted, so the caller is sent looking for a record it never "+
			"named", field)
	}
	fault, ok := httperr.Classify(err)
	if !ok {
		t.Fatalf("an absent %s was answered with %v, which is outside the taxonomy — a surface would "+
			"report the caller's own omission as an internal server fault", field, err)
	}
	if fault.Status != http.StatusUnprocessableEntity {
		t.Errorf("an absent %s answered status %d, want 422 (404 is the defect this closes)", field, fault.Status)
	}
	for _, refusal := range fault.Fields {
		if refusal.Field == field {
			return
		}
	}
	t.Errorf("the refusal for an absent %s names %+v, want the wire field %q — the name is what a "+
		"caller branches on and what a model reads back", field, fault.Fields, field)
}
