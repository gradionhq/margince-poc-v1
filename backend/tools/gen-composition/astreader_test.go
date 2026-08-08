// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package main

import (
	"path/filepath"
	"strings"
	"testing"
)

// handleUnitSource is a unit declaring one tool whose Handle field is
// exactly handleExpr. describe controls whether a Description is present,
// so a case that is genuinely served does not trip the separate
// "served tool needs a Description" refusal instead of the rule under
// test. quote, recv.Method, mkHandler and mustDial are declared but never
// called — the derivation only parses this source, it never compiles or
// runs it.
func handleUnitSource(handleExpr string, describe bool) string {
	desc := ""
	if describe {
		desc = "\t\t\tDescription: \"Reads nothing this workspace holds.\",\n"
	}
	return `package x

import "github.com/gradionhq/margince/backend/pkg/extension"

func quote() {}

type recv struct{}

func (recv) Method() {}

func mkHandler() extension.ToolHandler { return nil }

// mustDial has the SAME shape as extension.ToolHandler's conversion —
// one argument, a nil literal — which is exactly what isStaticallyNil must
// tell apart from extension.ToolHandler(nil) by checking the callee, not
// just the argument count.
func mustDial(extension.ToolHandler) extension.ToolHandler { return nil }

func New() extension.Extension {
	return extension.Extension{
		Name:    "x",
		Version: "0.1.0",
		Tools: []extension.Tool{{
			Name:    "t",
			Version: "1.0.0",
` + desc + `			Tier:           extension.TierAutoExecute,
			RequestedScope: extension.ScopeRead,
			Handle:         ` + handleExpr + `,
		}},
	}
}
`
}

// TestHandleMustBePlainIdentifier pins the seam's central rule for a
// governed tool's handler: the AST cannot distinguish an inert `pkg.Fn`
// from a liveness-reopening `recv.Method` without type information it does
// not have, so identifier-only is the sole rule that keeps a declaration's
// inertness checkable. The three documented inert spellings must keep
// deriving regardless of shape (see TestAServedToolWithNoDescriptionIsRefusedAtTheDeclaration
// in unitmanifest_test.go, which pins the same three at the call site).
func TestHandleMustBePlainIdentifier(t *testing.T) {
	for _, tc := range []struct {
		name    string
		handle  string
		wantErr bool
	}{
		{"identifier", "quote", false},
		{"nil", "nil", false},
		{"converted nil", "extension.ToolHandler(nil)", false},
		{"parenthesised nil", "(nil)", false},
		{"call expression", "mkHandler()", true},
		{"selector", "pkg.Fn", true},
		{"method value", "recv.Method", true},
		// Same shape as the accepted "converted nil" case — one argument,
		// nil — but the callee is unit-authored, not the published
		// extension.ToolHandler conversion. Pins the callee check in
		// isStaticallyNil: without it, this reads as inert and slips past
		// both readHandle's identifier rule AND the served-tool
		// Description refusal.
		{"one-argument nil call to unit code", "mustDial(nil)", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// "quote" is the one case that is actually served; every other
			// accepted case is a documented inert nil spelling and needs no
			// Description.
			describe := tc.handle == "quote"
			_, err := deriveSynthetic(t, "x", handleUnitSource(tc.handle, describe))
			if tc.wantErr {
				if err == nil || !strings.Contains(err.Error(), "Tool.Handle must be a plain identifier") {
					t.Fatalf("Handle: %s: err = %v, want the identifier-only refusal", tc.handle, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("Handle: %s must derive: %v", tc.handle, err)
			}
		})
	}
}

// initUnitSource is a unit whose root package holds a package-level
// init(), alongside an otherwise-valid New().
const initUnitSource = `package x

import "github.com/gradionhq/margince/backend/pkg/extension"

func init() {}

func New() extension.Extension {
	return extension.Extension{Name: "x", Version: "0.1.0"}
}
`

// TestPackageInitIsRejected: init() runs at IMPORT — before
// composition.Extensions() builds anything and long before
// RegisterExtensions validates the composed set — so it is the one place a
// unit could do live work while the tier claims its declaration is inert
// data. Task 1's runtime-role assertion cannot reach this window (there is
// no runtime yet to assert about); this AST walk is the only gate that can.
func TestPackageInitIsRejected(t *testing.T) {
	_, err := deriveSynthetic(t, "x", initUnitSource)
	if err == nil || !strings.Contains(err.Error(), "func init is not permitted") {
		t.Fatalf("err = %v, want the init-is-not-permitted refusal", err)
	}
}

// callBearingVarUnitSource is a unit whose root package holds a
// package-level var initialized by a call, alongside an otherwise-valid
// New(). `var conn = mustDial()` has the same import-time timing as
// init() — nothing distinguishes them for this purpose.
const callBearingVarUnitSource = `package x

import "github.com/gradionhq/margince/backend/pkg/extension"

var conn = mustDial()

func mustDial() int { return 0 }

func New() extension.Extension {
	return extension.Extension{Name: "x", Version: "0.1.0"}
}
`

// TestCallBearingVarInitializerIsRejected pins the second import-time gate:
// a package-level var whose initializer calls a function runs at the same
// moment as init(), before the pool exists and before anything validates
// the declaration.
func TestCallBearingVarInitializerIsRejected(t *testing.T) {
	_, err := deriveSynthetic(t, "x", callBearingVarUnitSource)
	if err == nil || !strings.Contains(err.Error(), "var initializer must not call a function") {
		t.Fatalf("err = %v, want the call-bearing-var refusal", err)
	}
}

// TestLiteralOnlyPackageVarIsAccepted: a package-level var holding only
// literals (yogi's `var quotes = []string{...}` is the in-tree example)
// runs no code at import and must keep deriving — the gate targets calls,
// not package-level state.
func TestLiteralOnlyPackageVarIsAccepted(t *testing.T) {
	src := `package x

import "github.com/gradionhq/margince/backend/pkg/extension"

var names = []string{"a", "b"}

func New() extension.Extension {
	return extension.Extension{Name: "x", Version: "0.1.0"}
}
`
	if _, err := deriveSynthetic(t, "x", src); err != nil {
		t.Fatalf("a literal-only package var must derive: %v", err)
	}
}

// TestLocalVarCallIsNotRejected: a call-bearing var declared INSIDE a
// function body is ordinary code that only runs when called — the gate is
// package-level only, so it must not reject New() itself or any helper for
// holding one.
func TestLocalVarCallIsNotRejected(t *testing.T) {
	src := `package x

import "github.com/gradionhq/margince/backend/pkg/extension"

func helper() int {
	v := compute()
	return v
}

func compute() int { return 1 }

func New() extension.Extension {
	return extension.Extension{Name: "x", Version: "0.1.0"}
}
`
	if _, err := deriveSynthetic(t, "x", src); err != nil {
		t.Fatalf("a local var initializer must not be rejected: %v", err)
	}
}

// TestSubpackageGoFilesAreRejected pins the gap rejectLiveInitializers
// cannot reach on its own: parser.ParseDir in deriveUnitManifest only ever
// reads the unit's ROOT directory. A subpackage's init() — reached only
// through a blank import from the root package — would never be parsed by
// anything in this generator, so it must be refused at the scan stage
// (scanUnit's refuseNonRootGoPackages) before deriveUnitManifest ever runs.
// deriveSynthetic cannot express this (it writes one root-only file), so
// this test drives scanUnit directly, the way TestScanExtensions does.
func TestSubpackageGoFilesAreRejected(t *testing.T) {
	root := t.TempDir()
	writeUnit(t, root, "x", map[string]string{
		"go.mod": "module example.test/ext/x\n\ngo 1.26.5\n",
		"x.go": `package x

import (
	_ "example.test/ext/x/internal/live"

	"github.com/gradionhq/margince/backend/pkg/extension"
)

func New() extension.Extension {
	return extension.Extension{Name: "x", Version: "0.1.0"}
}
`,
		// The blank import above is what would actually run this at
		// compose time; the subpackage itself is what refuseNonRootGoPackages
		// must catch regardless, since it never resolves imports at all.
		"internal/live/live.go": "package live\n\nfunc init() {}\n",
	})
	_, err := scanUnit("x", filepath.Join(root, "extensions", "x"))
	if err == nil || !strings.Contains(err.Error(), "holds a Go package outside the unit root") {
		t.Fatalf("err = %v, want the subpackage refusal", err)
	}
}

// TestUnbuiltCapabilityLayersStayExemptFromTheSubpackageWalk pins the other
// half of the same rule: a not-yet-composed capability layer must keep
// failing on its OWN refusal (scanUnit's unbuiltCapabilityLayers loop,
// checked first) rather than on "holds a Go package outside the unit root" —
// Task 13's crm-demo unit ships api/ and frontend/ subdirectories and the two
// refusals must not collide.
//
// migrations/ used to be on this list and is deliberately no longer:
// TestMigrationsLayerIsGovernedByItsOwnRule below pins what replaced the
// blanket refusal.
func TestUnbuiltCapabilityLayersStayExemptFromTheSubpackageWalk(t *testing.T) {
	for _, layer := range unbuiltCapabilityLayers {
		t.Run(layer, func(t *testing.T) {
			root := t.TempDir()
			writeUnit(t, root, "x", map[string]string{
				"go.mod": "module example.test/ext/x\n\ngo 1.26.5\n",
				"x.go": `package x

import "github.com/gradionhq/margince/backend/pkg/extension"

func New() extension.Extension {
	return extension.Extension{Name: "x", Version: "0.1.0"}
}
`,
				layer + "/placeholder.go": "package placeholder\n",
			})
			_, err := scanUnit("x", filepath.Join(root, "extensions", "x"))
			if err == nil || !strings.Contains(err.Error(), "composition is not built yet") {
				t.Fatalf("layer %s: err = %v, want the not-built-yet refusal, not the subpackage one", layer, err)
			}
		})
	}
}
