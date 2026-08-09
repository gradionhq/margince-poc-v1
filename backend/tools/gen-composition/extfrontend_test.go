// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeFrontendLayer lays down a unit's frontend package with the given
// manifest and an entry module, so each case below varies one thing.
func writeFrontendLayer(t *testing.T, dir, manifest string) {
	t.Helper()
	layer := filepath.Join(dir, "frontend")
	if err := os.MkdirAll(layer, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		"package.json": manifest,
		"screen.tsx":   "export default function S() { return null }\n",
	} {
		if err := os.WriteFile(filepath.Join(layer, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// TestCollectUnitFrontendRefusals: the four rules that make a screen mountable
// AND removable, each one a way the layer would otherwise fail somewhere worse
// than generation.
func TestCollectUnitFrontendRefusals(t *testing.T) {
	for name, tc := range map[string]struct{ manifest, want string }{
		// One workspace holds every enabled unit, so two units sharing a
		// package name are two members claiming one identity — and pnpm
		// resolves whichever it saw last.
		"a package named for another unit": {
			manifest: `{"name":"@margince-ext/other","private":true,"main":"screen.tsx","peerDependencies":{"react":"^19.0.0"}}`,
			want:     "must be named @margince-ext/demo",
		},
		"a package outside the extension namespace": {
			manifest: `{"name":"demo-screens","private":true,"main":"screen.tsx","peerDependencies":{"react":"^19.0.0"}}`,
			want:     "must be named @margince-ext/demo",
		},
		// A member that is not private is one `pnpm publish -r` away from a
		// registry, which is not what an installation's own unit is for.
		"a publishable package": {
			manifest: `{"name":"@margince-ext/demo","main":"screen.tsx","peerDependencies":{"react":"^19.0.0"}}`,
			want:     "must be private",
		},
		"no entry point": {
			manifest: `{"name":"@margince-ext/demo","private":true,"peerDependencies":{"react":"^19.0.0"}}`,
			want:     "declares no main",
		},
		"an entry point that does not exist": {
			manifest: `{"name":"@margince-ext/demo","private":true,"main":"nope.tsx","peerDependencies":{"react":"^19.0.0"}}`,
			want:     "does not exist",
		},
		// The one that fails at RUN TIME rather than at build time, with an
		// error naming neither the unit nor the cause: two React instances in
		// one bundle, and every hook in the screen throws.
		"react as a direct dependency": {
			manifest: `{"name":"@margince-ext/demo","private":true,"main":"screen.tsx","dependencies":{"react":"^19.0.0"}}`,
			want:     "must be a peerDependency",
		},
		"react-dom as a direct dependency": {
			manifest: `{"name":"@margince-ext/demo","private":true,"main":"screen.tsx","dependencies":{"react-dom":"^19.0.0"}}`,
			want:     "must be a peerDependency",
		},
		"a manifest that is not JSON": {
			manifest: `{`,
			want:     "package.json",
		},
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			writeFrontendLayer(t, dir, tc.manifest)
			_, err := collectUnitFrontend("demo", dir)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want one mentioning %q", err, tc.want)
			}
		})
	}
}

func TestCollectUnitFrontendAcceptsAWellFormedLayer(t *testing.T) {
	dir := t.TempDir()
	writeFrontendLayer(t, dir, `{"name":"@margince-ext/demo","private":true,"main":"screen.tsx","peerDependencies":{"react":"^19.0.0","react-dom":"^19.0.0"}}`)
	got, err := collectUnitFrontend("demo", dir)
	if err != nil {
		t.Fatalf("a well-formed layer was refused: %v", err)
	}
	if got == nil || got.Package != "@margince-ext/demo" {
		t.Fatalf("frontend = %#v", got)
	}
}

// A unit with no frontend layer is the common case — de, yogi and crm-hello
// are all shaped that way — and composes normally.
func TestCollectUnitFrontendAbsentIsNotAnError(t *testing.T) {
	got, err := collectUnitFrontend("demo", t.TempDir())
	if err != nil || got != nil {
		t.Fatalf("got %#v, %v — a unit without a screen composes normally", got, err)
	}
}

// The hyphen split every unit name may carry, resolved into an identifier the
// generated registry can actually name.
func TestScreenIdent(t *testing.T) {
	for unit, want := range map[string]string{
		"crm-demo":   "CrmDemoScreen",
		"de":         "DeScreen",
		"a-b-c":      "ABCScreen",
		"crm-hello2": "CrmHello2Screen",
	} {
		if got := screenIdent(unit); got != want {
			t.Errorf("screenIdent(%q) = %q, want %q", unit, got, want)
		}
	}
}

// TestExtScreensGenImportsOnlyUnitsWithAScreen: the registry is the join
// between the enabled set and the units that actually ship a screen. A unit
// without one contributes nothing and is not an error — App.tsx falls through
// to the generic published-operations card, which is what de and yogi get.
func TestExtScreensGenImportsOnlyUnitsWithAScreen(t *testing.T) {
	got := string(extScreensGen([]extensionUnit{
		{Name: "crm-demo", Frontend: &unitFrontend{Package: "@margince-ext/crm-demo", Export: "@margince-ext/crm-demo"}},
		{Name: "de"},
	}))
	for _, want := range []string{
		`import CrmDemoScreen from "@margince-ext/crm-demo";`,
		`"crm-demo": CrmDemoScreen,`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("emitted registry is missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, `"de"`) {
		t.Errorf("a unit with no frontend layer must contribute no entry:\n%s", got)
	}
}

// The emitted file is a function of the ENABLED SET, not of the order the
// filesystem handed directories over — scanExtensions sorts, and this pins
// that the emitter preserves it rather than ranging a map.
func TestExtScreensGenIsOrderedByUnitName(t *testing.T) {
	got := string(extScreensGen([]extensionUnit{
		{Name: "alpha", Frontend: &unitFrontend{Package: "@margince-ext/alpha", Export: "@margince-ext/alpha"}},
		{Name: "beta", Frontend: &unitFrontend{Package: "@margince-ext/beta", Export: "@margince-ext/beta"}},
	}))
	if strings.Index(got, "AlphaScreen") > strings.Index(got, "BetaScreen") {
		t.Errorf("imports are not in unit order:\n%s", got)
	}
}
