// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package backendarch

// The RBAC vocabulary is DECLARED in the contract and restated in Go, and the
// two must not drift.
//
// api/crm.yaml's RbacObject and RbacAction enums are what the web client types
// every capability check against: `openapi-typescript` renders them as string
// unions, so a button scoped to a misspelled object is a compile error there.
// That guarantee is only worth as much as the enum's agreement with the server
// — an object the contract omits produces a union the client cannot express,
// and an object the contract invents produces a check that always denies while
// looking deliberate.
//
// Go cannot derive its half. oapi-codegen names enums after parent+property
// (AuthorizationSeatType) and emits nothing at all for a top-level standalone
// string enum, so there are no generated constants for policy.go to derive
// from. These gates close that hole the other way: both lists stay
// hand-written, and merging them out of agreement is impossible.
//
// Both sides are DERIVED, neither restated here — the object list is
// AST-parsed from policy.go (coreObjectsFromSource, rbaccoverage_test.go) and
// the action list from principal.ObjectGrant's fields. A test that spelled the
// vocabulary a third time would drift exactly like the two it is watching.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

const (
	contractFile  = "api/crm.yaml"
	principalFile = "internal/shared/kernel/principal/principal.go"
)

func TestContractObjectEnumMatchesPolicyVocabulary(t *testing.T) {
	contract := contractSchema(t, "RbacObject").Enum
	if len(contract) == 0 {
		t.Fatal("api/crm.yaml declares no RbacObject enum; the vocabulary the web client " +
			"types against has moved or been deleted")
	}
	policy := coreObjectsFromSource(t)
	if len(policy) == 0 {
		t.Fatal("parsed no objects from policy.go; the declaration this gate compares against has moved")
	}
	// Set comparison alone would let a duplicated enum entry through, and a
	// duplicate is how a hand-edited 29-item list quietly loses a member.
	for _, list := range []struct {
		name   string
		values []string
	}{{"crm.yaml's RbacObject enum", contract}, {"policy.coreObjects", policy}} {
		seen := map[string]bool{}
		for _, value := range list.values {
			if seen[value] {
				t.Errorf("%s declares %q more than once", list.name, value)
			}
			seen[value] = true
		}
	}

	for _, object := range missing(policy, contract) {
		t.Errorf("RBAC object %q is in policy.coreObjects but not in crm.yaml's RbacObject enum. "+
			"The web client cannot name it, so no affordance can be scoped to it — add it to the "+
			"enum (and reconcile the addition upstream, P3).", object)
	}
	for _, object := range missing(contract, policy) {
		t.Errorf("crm.yaml's RbacObject enum declares %q but policy.coreObjects does not. "+
			"A client check against it would compile and then always deny, which reads as a "+
			"revoked permission rather than a contract error — drop it from the enum, or add "+
			"the object to policy with its backfill migration.", object)
	}
}

// The action vocabulary is bounded by principal.ObjectGrant: a grant carries
// exactly one boolean per action, so the struct's fields ARE the closed set. A
// fifth action cannot exist without a fifth field, which is what makes this
// derivation total rather than a spot check.
func TestContractActionEnumMatchesObjectGrant(t *testing.T) {
	fields := objectGrantActions(t)
	if len(fields) == 0 {
		t.Fatal("parsed no fields from principal.ObjectGrant; the struct this gate derives from has moved")
	}

	for _, name := range []string{"RbacAction", "RbacObjectGrant"} {
		schema := contractSchema(t, name)
		// RbacAction enumerates the verbs; RbacObjectGrant must require one
		// property per verb. They are checked together because a grant that
		// omitted an action would still marshal — the client would read the
		// missing key as a denial it was never told about.
		declared := schema.Enum
		if name == "RbacObjectGrant" {
			// Both halves: `required` alone would pass a schema that requires
			// an action it never declares a property for, and `properties`
			// alone would pass one that declares every action optional — a
			// grant the client would read as absent, which denies.
			declared = schema.Required
			properties := make([]string, 0, len(schema.Properties))
			for property := range schema.Properties {
				properties = append(properties, property)
			}
			if diff := append(missing(declared, properties), missing(properties, declared)...); len(diff) > 0 {
				t.Errorf("crm.yaml's RbacObjectGrant lists %v under one of required/properties but not "+
					"the other; an action declared optional reads as absent to a client, which denies", diff)
			}
		}
		for _, action := range missing(fields, declared) {
			t.Errorf("principal.ObjectGrant carries action %q but crm.yaml's %s does not declare it; "+
				"a client cannot ask about a grant the contract hides", action, name)
		}
		for _, action := range missing(declared, fields) {
			t.Errorf("crm.yaml's %s declares action %q but principal.ObjectGrant has no such field; "+
				"the server can never answer it", name, action)
		}
	}
}

// missing returns the members of have that want does not contain. Both sides
// of every comparison above run through it, so a difference is reported as the
// specific tokens that differ rather than two lists for a human to diff.
func missing(have, want []string) []string {
	var out []string
	for _, token := range have {
		if !slices.Contains(want, token) {
			out = append(out, token)
		}
	}
	slices.Sort(out)
	return out
}

// contractSchemaFields is the slice of one components.schemas entry these
// gates read. Decoding a narrow struct rather than a free-form map keeps a
// malformed contract a parse failure instead of a silently empty enum.
type contractSchemaFields struct {
	Enum       []string             `yaml:"enum"`
	Required   []string             `yaml:"required"`
	Properties map[string]yaml.Node `yaml:"properties"`
}

func contractSchema(t *testing.T, name string) contractSchemaFields {
	t.Helper()
	raw, err := os.ReadFile(contractFile)
	if err != nil {
		t.Fatalf("reading %s: %v", contractFile, err)
	}
	var doc struct {
		Components struct {
			Schemas map[string]contractSchemaFields `yaml:"schemas"`
		} `yaml:"components"`
	}
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("parsing %s: %v", contractFile, err)
	}
	schema, ok := doc.Components.Schemas[name]
	if !ok {
		t.Fatalf("%s declares no components.schemas.%s", contractFile, name)
	}
	return schema
}

// objectGrantActions returns principal.ObjectGrant's field names lowercased —
// the wire spelling of each action. The struct declares them on one line
// (`Create, Read, Update, Delete bool`), so every name on every field is
// collected rather than one per declaration.
func objectGrantActions(t *testing.T) []string {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), principalFile, nil, 0)
	if err != nil {
		t.Fatalf("parsing %s: %v", principalFile, err)
	}
	var actions []string
	for _, decl := range file.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok || gd.Tok != token.TYPE {
			continue
		}
		for _, spec := range gd.Specs {
			ts, ok := spec.(*ast.TypeSpec)
			if !ok || ts.Name.Name != "ObjectGrant" {
				continue
			}
			st, ok := ts.Type.(*ast.StructType)
			if !ok {
				continue
			}
			for _, field := range st.Fields.List {
				// Booleans only. Every field is an action TODAY, but a struct
				// that later carried metadata — a source, a timestamp — would
				// otherwise silently enlarge the required action vocabulary and
				// fail this gate somewhere far from the change that caused it.
				if ident, ok := field.Type.(*ast.Ident); !ok || ident.Name != "bool" {
					continue
				}
				for _, name := range field.Names {
					actions = append(actions, strings.ToLower(name.Name))
				}
			}
		}
	}
	return actions
}
