// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package backendarch

// The contract's list envelope has ONE shape, and something depends on that.
//
// httperr.recordsIn counts what a REST response hands over by reading the
// envelope rather than a list of response type names: a struct carrying a `Data`
// slice is a page, and its length is the record count charged against the agent
// read bound (MCP-SESS-READS). A list response that spelled its page some other
// way would be counted as ONE record however many it served — silently, because
// a wrong number is still a number.
//
// So the rule the counter assumes is asserted here over the generated contract,
// which is the only place it can be: a list shape added tomorrow either carries
// `Data []T` or fails this test.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"
)

// contractGen is the generated contract surface every response type is declared in.
const contractGen = "internal/contracts/api_gen.go"

func TestEveryListResponseCarriesADataSlice(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, contractGen, nil, 0)
	if err != nil {
		t.Fatalf("parsing %s: %v", contractGen, err)
	}

	var checked int
	ast.Inspect(file, func(n ast.Node) bool {
		spec, ok := n.(*ast.TypeSpec)
		if !ok || !strings.HasSuffix(spec.Name.Name, "ListResponse") {
			return true
		}
		structType, ok := spec.Type.(*ast.StructType)
		if !ok {
			return true
		}
		checked++
		for _, field := range structType.Fields.List {
			for _, name := range field.Names {
				if name.Name != "Data" {
					continue
				}
				if _, isSlice := field.Type.(*ast.ArrayType); !isSlice {
					t.Errorf("%s.Data is not a slice; httperr.recordsIn counts a page by its length "+
						"and would charge this response as one record however many it serves", spec.Name.Name)
				}
				return true
			}
		}
		t.Errorf("%s declares no Data field; httperr.recordsIn reads the page off `Data` and would "+
			"charge this response as one record however many it serves", spec.Name.Name)
		return true
	})

	// A population of zero would pass every assertion above while proving
	// nothing — the shape this whole file exists to refuse.
	if checked == 0 {
		t.Fatalf("no *ListResponse types found in %s; the census matched nothing and can only pass", contractGen)
	}
}
