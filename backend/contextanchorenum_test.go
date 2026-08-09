// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package backendarch

// GET /records/{entity_type}/{id}/context accepts exactly the record types the
// search module can search, and the contract has to say the same set.
//
// The handler derives its own admission from that table (search.knownEntity),
// so the two can only disagree in the contract's direction — and every way they
// disagree is quiet. An anchor the contract omits is one a generated client
// cannot ask for and the server would happily answer; an anchor the contract
// invents is one a client is told it may ask for and that answers 422. Both had
// already happened for `project` before this gate existed.
//
// Neither side is restated here: the Go half is AST-parsed from the module's
// one entity table, the contract half read off the path parameter.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"slices"
	"strconv"
	"testing"

	"gopkg.in/yaml.v3"
)

const searchBranchFile = "internal/modules/search/store.go"

const contextPath = "/records/{entity_type}/{id}/context"

func TestContextAnchorEnumMatchesTheSearchableEntities(t *testing.T) {
	contract := contextAnchorEnum(t)
	searchable := searchableEntitiesFromSource(t)
	slices.Sort(contract)
	slices.Sort(searchable)
	if !slices.Equal(contract, searchable) {
		t.Errorf("%s's %s entity_type enum is %v and the search module anchors %v — "+
			"a type in one list and not the other is either an anchor no client may name "+
			"or a documented anchor that answers 422",
			contractFile, contextPath, contract, searchable)
	}
}

// The response echoes the anchor back as a ContextEntityRef, so every anchor
// type must be an admissible ref type. A missing member makes the endpoint's
// own answer fail validation against the contract that describes it.
func TestEveryContextAnchorIsAnAdmissibleRefType(t *testing.T) {
	refTypes := contractSchema(t, "ContextEntityRef").Properties["type"]
	var typeSchema contractSchemaFields
	if err := refTypes.Decode(&typeSchema); err != nil {
		t.Fatalf("reading ContextEntityRef.type: %v", err)
	}
	if len(typeSchema.Enum) == 0 {
		t.Fatalf("%s declares no ContextEntityRef.type enum; the ref vocabulary has moved", contractFile)
	}
	for _, anchor := range contextAnchorEnum(t) {
		if !slices.Contains(typeSchema.Enum, anchor) {
			t.Errorf("%s is a context anchor and not a ContextEntityRef type %v — "+
				"the response's own anchor ref would fail the contract", anchor, typeSchema.Enum)
		}
	}
}

// contextAnchorEnum reads the path parameter's enum off the contract.
func contextAnchorEnum(t *testing.T) []string {
	t.Helper()
	raw, err := os.ReadFile(contractFile)
	if err != nil {
		t.Fatalf("reading %s: %v", contractFile, err)
	}
	var doc struct {
		Paths map[string]struct {
			Parameters []struct {
				Name   string               `yaml:"name"`
				In     string               `yaml:"in"`
				Schema contractSchemaFields `yaml:"schema"`
			} `yaml:"parameters"`
		} `yaml:"paths"`
	}
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("parsing %s: %v", contractFile, err)
	}
	path, ok := doc.Paths[contextPath]
	if !ok {
		t.Fatalf("%s declares no %s; the endpoint this gate watches has moved", contractFile, contextPath)
	}
	for _, param := range path.Parameters {
		if param.Name == "entity_type" && param.In == "path" && len(param.Schema.Enum) > 0 {
			return param.Schema.Enum
		}
	}
	t.Fatalf("%s's %s declares no entity_type path enum", contractFile, contextPath)
	return nil
}

// searchableEntitiesFromSource extracts the `entity:` value of every
// searchBranches element — the module's one entity table, parsed rather than
// copied, so a branch added or withdrawn reaches this gate on its own.
func searchableEntitiesFromSource(t *testing.T) []string {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), searchBranchFile, nil, 0)
	if err != nil {
		t.Fatalf("parsing %s: %v", searchBranchFile, err)
	}
	var entities []string
	for _, decl := range file.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok || gd.Tok != token.VAR {
			continue
		}
		for _, spec := range gd.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok || len(vs.Names) != 1 || vs.Names[0].Name != "searchBranches" {
				continue
			}
			entities = append(entities, branchEntities(t, vs.Values)...)
		}
	}
	if len(entities) == 0 {
		t.Fatalf("parsed no branches from %s; the declaration this gate derives from has moved", searchBranchFile)
	}
	return entities
}

// branchEntities reads the `entity: "…"` field out of each element of a
// searchBranches composite literal.
func branchEntities(t *testing.T, values []ast.Expr) []string {
	t.Helper()
	var out []string
	for _, value := range values {
		outer, ok := value.(*ast.CompositeLit)
		if !ok {
			continue
		}
		for _, element := range outer.Elts {
			branch, ok := element.(*ast.CompositeLit)
			if !ok {
				continue
			}
			for _, field := range branch.Elts {
				kv, ok := field.(*ast.KeyValueExpr)
				if !ok {
					continue
				}
				key, ok := kv.Key.(*ast.Ident)
				if !ok || key.Name != "entity" {
					continue
				}
				lit, ok := kv.Value.(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					t.Fatalf("a searchBranches entity is not a string literal: %s", branch.Elts)
				}
				entity, err := strconv.Unquote(lit.Value)
				if err != nil {
					t.Fatalf("unquoting a searchBranches entity: %v", err)
				}
				out = append(out, entity)
			}
		}
	}
	return out
}
