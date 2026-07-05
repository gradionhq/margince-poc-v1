// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// Command gen-stubs regenerates internal/compose/stubs_gen.go from the
// ServerInterface in internal/contracts/api_gen.go: one explicit 501 stub
// per contract operation. The path defaults suit `make gen` (run from
// backend/); the go:generate directive in internal/compose passes them
// relative to that package dir.
package main

import (
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"os"
	"strings"
)

var (
	srcPath = flag.String("src", "internal/contracts/api_gen.go", "generated contract source holding ServerInterface")
	outPath = flag.String("out", "internal/compose/stubs_gen.go", "stub file to write")
)

// header is the fixed preamble of the generated file; everything below it
// is one stub per ServerInterface method, in interface declaration order.
const header = `// Code generated from internal/contracts/api_gen.go ServerInterface; DO NOT EDIT.
// Regenerate: make gen (tools/gen-stubs).

package compose

import (
	nethttp "net/http"

	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
)

// stubs satisfies every crmcontracts.ServerInterface operation with an
// explicit 501: the whole contract surface exists from day one, and an
// unimplemented call is loud, never a silent 404. Server embeds stubs
// (one level deep) and module handlers shadow the operations they implement.
type stubs struct{}

var _ crmcontracts.ServerInterface = stubs{}
`

func main() {
	flag.Parse()
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "gen-stubs:", err)
		os.Exit(1)
	}
}

func run() error {
	iface, err := serverInterface(*srcPath)
	if err != nil {
		return err
	}

	lines := []string{header}
	count := 0
	for _, m := range iface.Methods.List {
		ft, ok := m.Type.(*ast.FuncType)
		if !ok || len(m.Names) != 1 {
			continue
		}
		name := m.Names[0].Name
		var params []string
		for _, field := range ft.Params.List {
			typ := rewriteType(types.ExprString(field.Type))
			for _, n := range field.Names {
				params = append(params, n.Name+" "+typ)
			}
		}
		lines = append(lines,
			fmt.Sprintf("func (stubs) %s(%s) {\n\thttperr.NotImplemented(w, r, %q)\n}\n", name, strings.Join(params, ", "), name))
		count++
	}

	if err := os.WriteFile(*outPath, []byte(strings.Join(lines, "\n")), 0o600); err != nil {
		return err
	}
	fmt.Printf("%d stubs generated\n", count)
	return nil
}

// serverInterface parses the generated contract package and returns the
// ServerInterface declaration — the authoritative list of operations.
func serverInterface(path string) (*ast.InterfaceType, error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
	if err != nil {
		return nil, err
	}
	for _, decl := range file.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok || gd.Tok != token.TYPE {
			continue
		}
		for _, spec := range gd.Specs {
			ts, ok := spec.(*ast.TypeSpec)
			if !ok || ts.Name.Name != "ServerInterface" {
				continue
			}
			if iface, ok := ts.Type.(*ast.InterfaceType); ok {
				return iface, nil
			}
		}
	}
	return nil, fmt.Errorf("%s: no ServerInterface declaration", path)
}

// rewriteType maps a type as spelled inside package contracts to how the
// generated compose file must spell it: net/http gets the nethttp alias
// (compose's own http import would shadow it) and every unqualified
// exported type is a contract type, qualified crmcontracts.
func rewriteType(t string) string {
	switch t {
	case "http.ResponseWriter":
		return "nethttp.ResponseWriter"
	case "*http.Request":
		return "*nethttp.Request"
	}
	if t != "" && t[0] >= 'A' && t[0] <= 'Z' && !strings.Contains(t, ".") {
		return "crmcontracts." + t
	}
	return t
}
