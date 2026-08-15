// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build !integration

package backendarch

// The egress SSRF denylist exists twice. internal/platform/netguard owns it;
// extensions/dispact-connector restates it because an extension unit is its own
// module and may import only the published pkg/** surface, which this guard is
// not on yet.
//
// A copy drifts, and this one drifted the moment the core list grew: the unit
// dials a member-supplied host, so a range the core refuses and the copy admits
// is not a style difference, it is the way around the core's guard. Only this
// module can see both files, so the comparison lives here rather than beside
// either list.
//
// It compares the CIDR sets, not the file text: the two are formatted
// differently and grouped differently on purpose.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"slices"
	"strconv"
	"testing"
)

// egressDenylists names where each copy lives and the identifier that holds it.
var egressDenylists = map[string]struct{ file, ident string }{
	"core":      {"internal/platform/netguard/netguard.go", "reservedNets"},
	"extension": {"../extensions/dispact-connector/client.go", "reservedCIDRs"},
}

func TestTheExtensionCopyOfTheEgressDenylistMatchesTheCore(t *testing.T) {
	core := cidrLiteralsIn(t, egressDenylists["core"].file, egressDenylists["core"].ident)
	extension := cidrLiteralsIn(t, egressDenylists["extension"].file, egressDenylists["extension"].ident)

	for _, cidr := range core {
		if !slices.Contains(extension, cidr) {
			t.Errorf("%s refuses %s and the extension copy does not — a member-supplied host reaches it there",
				egressDenylists["core"].file, cidr)
		}
	}
	for _, cidr := range extension {
		if !slices.Contains(core, cidr) {
			t.Errorf("the extension copy refuses %s and %s does not — the core guard is the weaker of the two",
				cidr, egressDenylists["core"].file)
		}
	}
}

// cidrLiteralsIn reads the string literals of the []string the named identifier
// is declared from. It fails rather than returning nothing when the identifier
// is gone: an empty list would compare equal to an empty list and report two
// matching denylists where there is no denylist at all.
func cidrLiteralsIn(t *testing.T, file, ident string) []string {
	t.Helper()
	parsed, err := parser.ParseFile(token.NewFileSet(), file, nil, 0)
	if err != nil {
		t.Fatalf("reading %s: %v", file, err)
	}

	var cidrs []string
	ast.Inspect(parsed, func(node ast.Node) bool {
		spec, ok := node.(*ast.ValueSpec)
		if !ok || !slices.ContainsFunc(spec.Names, func(name *ast.Ident) bool { return name.Name == ident }) {
			return true
		}
		ast.Inspect(spec, func(inner ast.Node) bool {
			literal, ok := inner.(*ast.BasicLit)
			if !ok || literal.Kind != token.STRING {
				return true
			}
			value, err := strconv.Unquote(literal.Value)
			if err != nil {
				t.Fatalf("%s: %s holds an unreadable literal %s", file, ident, literal.Value)
			}
			cidrs = append(cidrs, value)
			return true
		})
		return false
	})

	if len(cidrs) == 0 {
		t.Fatalf("%s: found no CIDR literals under %s — it was renamed or moved, and this test now compares nothing",
			file, ident)
	}
	return cidrs
}
