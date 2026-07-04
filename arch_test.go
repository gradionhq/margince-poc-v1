package fablearch

// Structural fitness functions (architecture/03 §1): these tests make the
// boundary rules mechanical. B-EP01.2 — Tier-0 seam and kernel packages
// stay dependency-free; B-EP01.4's depguard/go-arch-lint gates cover the
// module DAG, this covers the leaf layer the compiler can't.

import (
	"go/build"
	"strings"
	"testing"
)

// leafPackages is the Tier-0 seam layer plus the shared kernel. They may
// import the stdlib and each other; a crm-* module or third-party import
// here is an architecture defect.
var leafPackages = []string{
	"crmctx",
	"sor",
	"mcp",
	"connector",
	"workflow",
	"model",
	"retrieval",
	"jurisdiction",
	"kernel/events",
	"kernel/ids",
	"kernel/errs",
	"kernel/prov",
}

const modulePath = "github.com/gradionhq/fable-poc"

func TestLeafPackagesAreDependencyFree(t *testing.T) {
	leafSet := make(map[string]bool, len(leafPackages))
	for _, p := range leafPackages {
		leafSet[modulePath+"/"+p] = true
	}

	for _, pkg := range leafPackages {
		imported, err := build.Import(modulePath+"/"+pkg, ".", 0)
		if err != nil {
			t.Fatalf("resolving %s: %v", pkg, err)
		}
		for _, imp := range imported.Imports {
			if !strings.Contains(imp, ".") {
				continue // stdlib
			}
			if leafSet[imp] {
				continue // leaf → leaf is allowed (types only, no cycles)
			}
			t.Errorf("%s imports %s: leaf packages must be stdlib-only", pkg, imp)
		}
	}
}
