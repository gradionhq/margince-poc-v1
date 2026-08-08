// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/pkg/extension"
)

// The SPA's half of the two-lane bind, tested on the same terms as the Go
// half: the committed empty-tree registry under frontend/src and this
// generator's vanilla output are the same bytes, and a composed set emits the
// descriptors a screen reads. gen-composition itself holds the first property
// at gen time (stubMatchesVanilla); this holds it in the unit lane, where a
// stub edit fails fastest and without a repository walk.

func TestVanillaFrontendRegistryMatchesTheCommittedStub(t *testing.T) {
	stub, err := os.ReadFile(filepath.Join("..", "..", "..", filepath.FromSlash(frontendVanillaStub)))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(stub, frontendGen(nil, nil)) {
		t.Fatalf("%s differs from the generator's vanilla output:\n--- stub ---\n%s\n--- generated ---\n%s", frontendVanillaStub, stub, frontendGen(nil, nil))
	}
}

// The empty registry is a value the SPA reads, not merely a file that exists:
// `extensions` has to be an empty ARRAY, because app/extensions.ts calls .find
// on it unconditionally and a `null`, `{}` or `as const` tuple would crash the
// vanilla lane at its first unit route.
func TestVanillaFrontendRegistryIsAnEmptyArray(t *testing.T) {
	got := string(frontendGen(nil, nil))
	if !strings.HasSuffix(got, "export const extensions: readonly ExtensionDescriptor[] = [];\n") {
		t.Fatalf("vanilla registry does not end in an empty array literal:\n%s", got)
	}
}

func composedFixture() ([]extensionUnit, []declaredVerb) {
	units := []extensionUnit{{Name: "alpha"}, {Name: "beta"}}
	verbs := []declaredVerb{
		{verb: extension.Verb{
			Unit: "alpha", OperationID: "alphaSync", Route: "/ext/alpha/sync",
			Method: "POST", Title: "Sync contacts", Version: "1.2.0",
			RbacObject: "ext_alpha_contact",
			RbacAction: extension.RbacRead,
		}},
		{verb: extension.Verb{
			Unit: "beta", OperationID: "betaPing", Route: "/ext/beta/ping",
			Method: "GET", Title: "Ping", Version: "0.1.0",
		}},
	}
	return units, verbs
}

func TestFrontendRegistryCarriesEveryDeclaredVerbUnderItsUnit(t *testing.T) {
	got := string(frontendGen(composedFixture()))
	for _, want := range []string{
		`    name: "alpha",`,
		`        operationId: "alphaSync",`,
		`        route: "/ext/alpha/sync",`,
		`        method: "POST",`,
		`        title: "Sync contacts",`,
		`        version: "1.2.0",`,
		`        rbacObject: "ext_alpha_contact",`,
		`    name: "beta",`,
		`        operationId: "betaPing",`,
		// A verb declaring no object emits the empty string rather than
		// omitting the field: the descriptor type has no optional member, and
		// app/extensions.ts reads it unconditionally to decide whether the
		// screen has a capability gate at all.
		`        rbacObject: "",`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("composed registry misses %q:\n%s", want, got)
		}
	}
	// Unit order is the scan's sorted order, and a verb belongs to exactly one
	// unit — a grouping bug that put every verb under the first unit would
	// still contain all the lines above.
	if strings.Index(got, `name: "alpha"`) > strings.Index(got, `name: "beta"`) {
		t.Errorf("units are not in sorted order:\n%s", got)
	}
	if strings.Index(got, `"alphaSync"`) > strings.Index(got, `name: "beta"`) {
		t.Errorf("alpha's verb is emitted outside alpha's descriptor:\n%s", got)
	}
	if strings.Index(got, `"betaPing"`) < strings.Index(got, `name: "beta"`) {
		t.Errorf("beta's verb is emitted outside beta's descriptor:\n%s", got)
	}
}

// A unit that composes but declares no governed operation is a real state
// (a Go-only unit with no api/ fragment — extensions/de today). It must reach
// the registry with an empty verb list rather than disappearing from it: the
// SPA's not-found card is a claim that the unit is not ENABLED, and it would
// be a lie for a unit that is.
func TestAUnitWithNoVerbsStillReachesTheRegistry(t *testing.T) {
	got := string(frontendGen([]extensionUnit{{Name: "de"}}, nil))
	if !strings.Contains(got, "    name: \"de\",\n    verbs: [\n    ],\n") {
		t.Fatalf("a verb-less unit is missing or malformed:\n%s", got)
	}
}

// The emitted file is TypeScript, so every string in it is attacker-adjacent
// input: a unit author writes the title and the operation id. tsString goes
// through encoding/json precisely so a quote, a backslash or a newline cannot
// end the literal early — the failure mode is arbitrary code in the SPA's
// bundle, not a formatting glitch.
func TestFrontendRegistryEscapesDeclaredText(t *testing.T) {
	hostile := "\" + (() => { fetch(\"//evil\") })() + \"\n\\"
	got := string(frontendGen(
		[]extensionUnit{{Name: "alpha"}},
		[]declaredVerb{{verb: extension.Verb{Unit: "alpha", Title: hostile}}},
	))
	encoded, err := json.Marshal(hostile)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "        title: "+string(encoded)+",\n") {
		t.Fatalf("hostile title is not JSON-escaped:\n%s", got)
	}
	// The literal must stay on ONE line — an unescaped newline would make the
	// emitted file a syntax error at best and a second statement at worst.
	for _, line := range strings.Split(got, "\n") {
		if strings.Contains(line, "fetch") && !strings.HasPrefix(strings.TrimSpace(line), "title:") {
			t.Fatalf("the hostile string escaped its own literal on line %q", line)
		}
	}
}

// stubMatchesVanilla is the gate BOTH committed stubs answer to, and until this
// task it had no unit coverage at all — it was exercised only end to end by
// `make gen` / `make check-composition`. A gate whose failure path is never run
// is the one that quietly stops refusing, and this task gave it a second lane
// to police.
func TestStubMatchesVanillaRefusesAnEditToEitherStub(t *testing.T) {
	root := filepath.Join("..", "..", "..")
	if err := stubMatchesVanilla(root); err != nil {
		t.Fatalf("the committed tree does not satisfy its own vanilla gate: %v", err)
	}

	for _, s := range vanillaStubs {
		t.Run(s.rel, func(t *testing.T) {
			// A copy of the tree's two stubs, one of them edited. Copying only
			// the files the gate reads keeps this a unit test: the gate opens
			// exactly these paths and nothing else.
			tmp := t.TempDir()
			for _, other := range vanillaStubs {
				content := other.emit()
				if other.rel == s.rel {
					// One byte of drift — a lone trailing newline, the least
					// visible edit a human could leave behind.
					content = append(content, '\n')
				}
				path := filepath.Join(tmp, filepath.FromSlash(other.rel))
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, content, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			err := stubMatchesVanilla(tmp)
			if err == nil || !strings.Contains(err.Error(), s.rel) {
				t.Fatalf("err = %v, want a refusal naming %s", err, s.rel)
			}
		})
	}

	t.Run("a missing stub is a refusal, not an empty match", func(t *testing.T) {
		if err := stubMatchesVanilla(t.TempDir()); err == nil {
			t.Fatal("an empty tree satisfied the vanilla gate")
		}
	})
}
