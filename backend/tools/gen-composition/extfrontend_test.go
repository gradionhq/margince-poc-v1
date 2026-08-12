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

// An entry point that RESOLVES, but resolves outside the unit's own layer.
//
// Existence was the only thing asked of `main`, and existence is not the
// property that matters: check-ext-imports.sh globs extensions/*/frontend, so a
// `main` reaching past that directory moves the unit's real code out of the one
// gate holding the unit/core boundary — wholesale, rather than one smuggled
// import at a time. The frontend layer is the gated surface, so the entry point
// has to be inside it.
func TestCollectUnitFrontendRefusesAnEntryPointOutsideTheLayer(t *testing.T) {
	for name, tc := range map[string]struct{ main, sibling, want string }{
		"a main that climbs out of the layer": {
			main:    "../screen.tsx",
			sibling: "screen.tsx",
			want:    "leaves",
		},
		"a main that climbs out and back into core": {
			main:    "../../frontend/src/main.tsx",
			sibling: filepath.Join("..", "frontend", "src", "main.tsx"),
			want:    "leaves",
		},
		// Refused today only by accident, and with the wrong cause named:
		// filepath.Join swallows the leading slash, so the generator stats
		// <layer>/etc/hosts and reports "does not exist" while Node and Vite
		// would resolve /etc/hosts itself.
		"an absolute main": {
			main: "/etc/hosts",
			want: "absolute",
		},
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			writeFrontendLayer(t, dir, `{"name":"@margince-ext/demo","private":true,"main":"`+tc.main+`","peerDependencies":{"react":"^19.0.0"}}`)
			if tc.sibling != "" {
				target := filepath.Join(dir, "frontend", tc.sibling)
				if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(target, []byte("export default function S() { return null }\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
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
//
// The hyphen-bearing cases are load-bearing coverage: the reference unit is
// `notes`, a single word, so nothing in the enabled set exercises the split.
//
// The pairs below are the reason unitCamel exists. Title-casing alone made
// `foo-1` and `foo1` — both legal names — one `Foo1Screen`, so keep asserting
// them as a PAIR rather than as two independent rows: what matters is that the
// two lines differ, not what either says on its own.
func TestScreenIdent(t *testing.T) {
	for unit, want := range map[string]string{
		"notes":      "NotesScreen",
		"de":         "DeScreen",
		"a-b-c":      "ABCScreen",
		"ab-c":       "AbCScreen",
		"abc":        "AbcScreen",
		"crm-demo":   "CrmDemoScreen",
		"crm-hello2": "CrmHello2Screen",
		// The collision the reviewer found, now two identifiers.
		"foo-1": "Foo_1Screen",
		"foo1":  "Foo1Screen",
		// A digit-initial name is legal, and `1fooScreen` is not an
		// identifier any JavaScript parser accepts.
		"1foo":    "_1fooScreen",
		"a-1-b":   "A_1BScreen",
		"a1-b":    "A1BScreen",
		"crm-2-x": "Crm_2XScreen",
		"crm2-x":  "Crm2XScreen",
	} {
		if got := screenIdent(unit); got != want {
			t.Errorf("screenIdent(%q) = %q, want %q", unit, got, want)
		}
	}
}

// unitFromCamel is unitCamel's INVERSE, written out here so the injectivity
// argument in unitCamel's doc comment is executed rather than merely asserted:
// if every name in a representative set survives a round trip, no two of them
// can share an encoding.
//
// It reads the encoding exactly as the argument describes it — segment starts
// are position 0, every upper-case letter and every `_`, and nothing else.
func unitFromCamel(camel string) string {
	var parts []string
	var cur strings.Builder
	for i := 0; i < len(camel); i++ {
		c := camel[i]
		boundary := i > 0 && (c == '_' || (c >= 'A' && c <= 'Z'))
		if boundary {
			parts = append(parts, cur.String())
			cur.Reset()
		}
		if c == '_' {
			continue
		}
		cur.WriteByte(c)
	}
	parts = append(parts, cur.String())
	return strings.ToLower(strings.Join(parts, "-"))
}

// TestUnitCamelIsInjective: the property the generated registry depends on.
// Two units sharing an identifier is a duplicate `import` in a file nobody
// wrote — or, in the copy overlay, no error at all and one unit reading the
// other's strings.
//
// The set is exhaustive over a small alphabet rather than sampled, because the
// defect this replaces lived precisely in the short digit-bearing names a
// hand-written list is least likely to think of.
func TestUnitCamelIsInjective(t *testing.T) {
	names := representativeUnitNames()
	seen := map[string]string{}
	for _, n := range names {
		got := unitCamel(n)
		if prev, taken := seen[got]; taken {
			t.Fatalf("unitCamel(%q) = unitCamel(%q) = %q — two legal unit names, one identifier", prev, n, got)
		}
		seen[got] = n
		// The round trip is the argument itself: a decodable encoding
		// cannot be many-to-one.
		if back := unitFromCamel(got); back != n {
			t.Errorf("unitCamel(%q) = %q decodes to %q — the encoding lost part of the name", n, got, back)
		}
	}
	if len(names) < 100 {
		t.Fatalf("the injectivity set is only %d names — it is meant to be broad", len(names))
	}
}

// representativeUnitNames enumerates every name of up to three segments over
// {a, b, 1, 2, a1} — hyphen and digit placements in every combination, which is
// where every collision of this class lives — plus the real enabled set.
func representativeUnitNames() []string {
	segments := []string{"a", "b", "1", "2", "a1"}
	var names []string
	for _, s1 := range segments {
		names = append(names, s1)
		for _, s2 := range segments {
			names = append(names, s1+"-"+s2)
			for _, s3 := range segments {
				names = append(names, s1+"-"+s2+"-"+s3)
			}
		}
	}
	return append(names, "notes", "de", "yogi", "crm-demo", "crm-hello2", "foo-1", "foo1")
}

// TestExtScreensGenImportsOnlyUnitsWithAScreen: the registry is the join
// between the enabled set and the units that actually ship a screen. A unit
// without one contributes nothing and is not an error — App.tsx falls through
// to the generic published-operations card, which is what de and yogi get.
func TestExtScreensGenImportsOnlyUnitsWithAScreen(t *testing.T) {
	got := string(extScreensGen([]extensionUnit{
		{Name: "notes", Frontend: &unitFrontend{Package: "@margince-ext/notes", Export: "@margince-ext/notes"}},
		{Name: "de"},
	}))
	for _, want := range []string{
		`import NotesScreen from "@margince-ext/notes";`,
		`"notes": NotesScreen,`,
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
