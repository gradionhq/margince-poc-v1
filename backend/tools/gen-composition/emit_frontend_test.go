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

func composedFixture() ([]derivedManifest, []declaredVerb) {
	units := []derivedManifest{
		{Unit: extensionUnit{Name: "alpha"}},
		{Unit: extensionUnit{Name: "beta"}},
	}
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
	got := string(frontendGen([]derivedManifest{{Unit: extensionUnit{Name: "de"}}}, nil))
	if !strings.Contains(got, "    name: \"de\",\n    secretScope: \"\",\n    verbs: [\n    ],\n") {
		t.Fatalf("a verb-less unit is missing or malformed:\n%s", got)
	}
}

// The secret scope the SPA places a unit's settings entry by. It is emitted
// for every unit, the one declaring no secret included — the empty string is
// read as "this unit has no credential to manage" and offers it on neither
// page, and a field that was simply absent would be indistinguishable from a
// registry generated before the field existed.
func TestFrontendRegistryCarriesTheDeclaredSecretScope(t *testing.T) {
	got := string(frontendGen([]derivedManifest{
		{
			Unit:     extensionUnit{Name: "dispact-connector"},
			Manifest: unitManifest{Secrets: []secretsRequest{{Key: "api-token", Scope: "user"}}},
		},
		{
			Unit:     extensionUnit{Name: "notes"},
			Manifest: unitManifest{Secrets: []secretsRequest{{Key: "signing", Scope: "workspace"}}},
		},
		{Unit: extensionUnit{Name: "yogi"}},
	}, nil))
	for _, want := range []string{
		"    name: \"dispact-connector\",\n    secretScope: \"user\",\n",
		"    name: \"notes\",\n    secretScope: \"workspace\",\n",
		"    name: \"yogi\",\n    secretScope: \"\",\n",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in:\n%s", want, got)
		}
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
		[]derivedManifest{{Unit: extensionUnit{Name: "alpha"}}},
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

// stubMatchesVanilla is the gate EVERY committed stub answers to — the Go
// wiring, the descriptor registry, the screen registry and the copy overlay —
// and until the tier's first frontend slice it had no unit coverage at all,
// being exercised only end to end by `make gen` / `make check-composition`. A
// gate whose failure path is never run is the one that quietly stops refusing.
//
// It ranges vanillaStubs rather than naming them, so a fifth artifact is
// policed by construction rather than by somebody remembering this test.
func TestStubMatchesVanillaRefusesAnEditToAnyStub(t *testing.T) {
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

// The composed frontend workspace is emitted, not tracked: it lives under the
// gitignored build/ tree, so an installation staging a frontend-bearing unit
// writes only there and never touches the tracked root lockfile.
func TestEmitsAComposedFrontendWorkspace(t *testing.T) {
	dir := t.TempDir()
	if err := emitComposedFrontendWorkspace(dir, []string{"notes", "relay-probe"}); err != nil {
		t.Fatalf("emit: %v", err)
	}
	for _, want := range []string{"package.json", "pnpm-workspace.yaml"} {
		if _, err := os.Stat(filepath.Join(dir, want)); err != nil {
			t.Errorf("missing %s: %v", want, err)
		}
	}
	ws, err := os.ReadFile(filepath.Join(dir, "pnpm-workspace.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, unit := range []string{"notes", "relay-probe"} {
		if !strings.Contains(string(ws), unit) {
			t.Errorf("workspace does not name %s", unit)
		}
	}
	// The host SPA must NOT be a member: pnpm installs beside each member, so a
	// workspace naming ../../../frontend would write the same frontend/node_modules
	// the root workspace owns and installs with --frozen-lockfile.
	for _, line := range strings.Split(string(ws), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "- ") && strings.HasSuffix(strings.TrimSpace(line), "/frontend") &&
			!strings.Contains(line, "/extensions/") {
			t.Errorf("the host SPA is a member (%q) — its node_modules is the root workspace's to own", strings.TrimSpace(line))
		}
	}
	// The emitted manifest must be PRIVATE and depend on nothing. It exists to
	// be a workspace root, and a root with dependencies of its own would install
	// a second copy of whatever the host already owns.
	raw, err := os.ReadFile(filepath.Join(dir, "package.json"))
	if err != nil {
		t.Fatal(err)
	}
	var pkg struct {
		Private      bool              `json:"private"`
		Dependencies map[string]string `json:"dependencies"`
	}
	if err := json.Unmarshal(raw, &pkg); err != nil {
		t.Fatalf("package.json does not parse: %v", err)
	}
	if !pkg.Private {
		t.Error("package.json is not private — a composed workspace root is not a publishable artifact")
	}
	if len(pkg.Dependencies) != 0 {
		t.Errorf("package.json declares %d dependencies — the root owns none", len(pkg.Dependencies))
	}
}

// A member naming a directory that does not exist is a claim, and pnpm only
// tolerates it by ignoring it. The caller passes frontend-bearing units only;
// this holds the emitter to writing exactly what it was given.
func TestTheWorkspaceNamesOnlyTheUnitsItWasGiven(t *testing.T) {
	dir := t.TempDir()
	if err := emitComposedFrontendWorkspace(dir, []string{"notes"}); err != nil {
		t.Fatalf("emit: %v", err)
	}
	ws, err := os.ReadFile(filepath.Join(dir, "pnpm-workspace.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var members int
	for _, line := range strings.Split(string(ws), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "- ") {
			members++
		}
	}
	if members != 1 {
		t.Errorf("workspace names %d members, want exactly the 1 it was given", members)
	}
}

// The overrides must be in pnpm-workspace.yaml and NOT in the root package.json's
// "pnpm" field: pnpm 11 stopped reading that field, so an override written there
// is silently dropped and the install fails on a unit's workspace:* specifier.
// This is the regression a laptop on pnpm 10 cannot see.
func TestTheOverridesAreWhereBothPnpmVersionsReadThem(t *testing.T) {
	dir := t.TempDir()
	if err := emitComposedFrontendWorkspace(dir, []string{"notes"}); err != nil {
		t.Fatalf("emit: %v", err)
	}
	ws, err := os.ReadFile(filepath.Join(dir, "pnpm-workspace.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"overrides:", "@margince/frontend", "link:", "@types/react"} {
		if !strings.Contains(string(ws), want) {
			t.Errorf("pnpm-workspace.yaml does not carry %q — an override pnpm 11 can read", want)
		}
	}
	pkg, err := os.ReadFile(filepath.Join(dir, "package.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(pkg), `"overrides"`) {
		t.Error("package.json declares overrides — pnpm 11 ignores that field, so they would silently not apply")
	}
}

// A second emission must replace the member list rather than accumulate it: a
// unit removed from extensions/ must leave the composed workspace, or the next
// install resolves a member that is not there.
func TestReEmittingTheWorkspaceReplacesTheMembers(t *testing.T) {
	dir := t.TempDir()
	if err := emitComposedFrontendWorkspace(dir, []string{"notes", "relay-probe"}); err != nil {
		t.Fatalf("emit: %v", err)
	}
	if err := emitComposedFrontendWorkspace(dir, []string{"notes"}); err != nil {
		t.Fatalf("re-emit: %v", err)
	}
	ws, err := os.ReadFile(filepath.Join(dir, "pnpm-workspace.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(ws), "relay-probe") {
		t.Error("a removed unit is still a member of the composed workspace")
	}
}
