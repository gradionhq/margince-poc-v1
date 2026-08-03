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
// The obligation is DERIVED from the contract type — every non-pointer
// openapi_types.UUID field it declares — so a required id added to either body
// upstream fails here until the mapping validates it, instead of shipping as
// another unusable verb.

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
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
				var missing *RequiredFieldError
				err = tc.call(t, body)
				if !errors.As(err, &missing) {
					t.Fatalf("an absent %s was answered with %T (%v); want *RequiredFieldError, or the "+
						"caller is sent looking for a record it never named. Validate it in the mapping, "+
						"which both transports share.", field, err, err)
				}
				if missing.Field != field {
					t.Errorf("the refusal names %q, want the wire field %q — the name is what a caller "+
						"branches on and what a model reads back", missing.Field, field)
				}
			})
		}
	}
}
