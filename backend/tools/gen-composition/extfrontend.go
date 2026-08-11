// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package main

// The frontend layer's composition rule: what a unit's screen package must be
// for the SPA to mount it. It is the rule that let `frontend` leave
// unbuiltCapabilityLayers, exactly as collectUnitTables did for `migrations`
// and collectUnitFragments for `api`.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// frontendLayer is the unit subdirectory holding its screen package.
const frontendLayer = "frontend"

// extPackagePrefix namespaces a unit's npm package the way ext_ namespaces its
// tables, its RBAC objects and its job kinds. One workspace holds every enabled
// unit, so the prefix is what keeps two of them from colliding — and it makes a
// unit package recognisable as one in a lockfile a human reads.
const extPackagePrefix = "@margince-ext/"

// hostedFrameworkDeps are the packages a unit must declare as PEERS rather than
// as its own, because each keeps state the HOST owns and a second copy is a
// second, empty one:
//
//   - react/react-dom hold the hook dispatcher. A unit rendering through its
//     own copy calls hooks the host never dispatched, and every one of them
//     throws with a message naming neither the unit nor the cause.
//   - @tanstack/react-query holds the QueryClient in a React context. A unit
//     with its own copy reads a DIFFERENT context than the provider the app
//     mounted, so its first useQuery throws "No QueryClient set" on a page
//     where one is plainly set.
//
// As peers they all resolve to the host's copy. resolve.dedupe in
// vite.config.ts is the other half, for the case where one of a unit's own
// dependencies pulls a second copy in transitively.
var hostedFrameworkDeps = []string{"react", "react-dom", "@tanstack/react-query"}

// unitFrontend is one unit's screen package, as the workspace sees it.
type unitFrontend struct {
	// Package is the npm name, and Export is the specifier the generated
	// registry imports. They are equal today — a unit's entry point is its
	// package root — and kept apart because a package that later publishes a
	// subpath would change one and not the other.
	Package string
	Export  string
}

// unitPackageJSON is the subset of a package manifest this composer judges.
// Everything else about the package is the unit author's business.
type unitPackageJSON struct {
	Name             string            `json:"name"`
	Private          bool              `json:"private"`
	Main             string            `json:"main"`
	Dependencies     map[string]string `json:"dependencies"`
	PeerDependencies map[string]string `json:"peerDependencies"`
}

// collectUnitFrontend reads a unit's frontend layer. Absent is the common case
// (de, yogi and crm-hello are all shaped that way) and composes nothing.
//
// Each refusal below is a failure that would otherwise happen somewhere worse:
// a name collision resolves to whichever member pnpm saw last, a missing entry
// point fails in the generated registry rather than at its cause, and a direct
// dependency on a hosted framework fails at RUN TIME in the browser.
func collectUnitFrontend(name, dir string) (*unitFrontend, error) {
	manifest := filepath.Join(dir, frontendLayer, "package.json")
	raw, err := os.ReadFile(manifest) // #nosec G304 -- the unit tree this composer was pointed at
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var pkg unitPackageJSON
	if err := json.Unmarshal(raw, &pkg); err != nil {
		return nil, fmt.Errorf("extensions/%s: %s/package.json: %w", name, frontendLayer, err)
	}

	want := extPackagePrefix + name
	switch {
	case pkg.Name != want:
		return nil, fmt.Errorf("extensions/%s: %s/package.json declares name %q — a unit's package must be named %s, because one workspace holds every enabled unit and a shared name is two members claiming one identity",
			name, frontendLayer, pkg.Name, want)
	case !pkg.Private:
		return nil, fmt.Errorf("extensions/%s: %s/package.json must be private — an installation's own unit is not a publishable artifact, and a non-private member is one `pnpm publish -r` away from a registry",
			name, frontendLayer)
	case pkg.Main == "":
		return nil, fmt.Errorf("extensions/%s: %s/package.json declares no main — the screen the SPA mounts is that module's default export",
			name, frontendLayer)
	}
	for _, dep := range hostedFrameworkDeps {
		if _, direct := pkg.Dependencies[dep]; direct {
			return nil, fmt.Errorf("extensions/%s: %s/package.json lists %s as a dependency — it must be a peerDependency, or the unit ships a second copy of state the host owns and its screen fails at RUN TIME, with an error naming neither the unit nor the cause",
				name, frontendLayer, dep)
		}
	}
	// An absolute main is refused by name rather than left to the check below.
	// filepath.Join swallows the leading separator, so an absolute path would
	// be stat'd as <layer>/etc/... and reported as merely missing — while Node
	// and Vite resolve an absolute specifier as itself. Naming the real cause
	// costs one branch; a misattributed error costs an author an afternoon.
	if strings.HasPrefix(pkg.Main, "/") || filepath.IsAbs(filepath.FromSlash(pkg.Main)) {
		return nil, fmt.Errorf("extensions/%s: %s/package.json declares an absolute main %q — it must be a path relative to the unit's own %s/, or the composed registry means one thing here and another in the bundler",
			name, frontendLayer, pkg.Main, frontendLayer)
	}
	// The entry point has to be INSIDE the layer, and existence is not that
	// property. frontend/scripts/check-ext-imports.sh globs
	// extensions/*/frontend, so a main reaching past the layer does not smuggle
	// one import — it relocates the unit's shipped code out of the only gate
	// holding the unit/core boundary, wholesale. Refused here because this is
	// where the layer is already the subject; the gate cannot see a package
	// manifest, and the bundler will not object.
	layerDir := filepath.Join(dir, frontendLayer)
	entry := filepath.Join(layerDir, filepath.FromSlash(pkg.Main))
	rel, err := filepath.Rel(layerDir, entry)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return nil, fmt.Errorf("extensions/%s: %s/package.json declares main %q, which leaves the unit's own %s/ — the entry module is the one file the whole layer is reached through, and outside the layer it is code no gate scans",
			name, frontendLayer, pkg.Main, frontendLayer)
	}
	// Checked here rather than left to the bundler: an entry point that does
	// not exist fails inside the GENERATED registry, where the error names a
	// file nobody wrote and the cause is two steps away.
	if _, err := os.Stat(entry); err != nil {
		return nil, fmt.Errorf("extensions/%s: %s/%s does not exist, and it is the module the composed registry imports",
			name, frontendLayer, pkg.Main)
	}
	return &unitFrontend{Package: pkg.Name, Export: pkg.Name}, nil
}

// screenIdent turns a unit name into the JavaScript identifier the generated
// registry binds its import to: crm-demo → CrmDemoScreen.
//
// A hyphen is legal in a package name and illegal in an identifier, so the
// hyphens have to go — the same split the Go side already makes, where
// `crm-hello` is package `crmhello`. What matters is that they go INJECTIVELY:
// see unitCamel.
func screenIdent(unit string) string {
	return unitCamel(unit) + "Screen"
}

// unitCamel is the one place a unit name becomes a camel-case token, shared by
// screenIdent and localeKeyPrefix so the two can never drift into disagreeing
// about which unit a generated symbol belongs to.
//
// It must be INJECTIVE over the validated unit-name alphabet
// (`^[a-z0-9]+(-[a-z0-9]+)*$`, extension.Name.Validate), and the obvious
// spelling — title-case every hyphen-separated segment and concatenate — is
// NOT. Upper-casing a DIGIT is a no-op, so `foo-1` and `foo1` both collapsed to
// `Foo1`: two legal, distinct units generating one `Foo1Screen` import and one
// `extFoo1.` copy namespace. That is either a duplicate-identifier failure in
// generated TypeScript nobody wrote, or — worse, in the copy overlay, whose
// keys are strings and cannot clash at compile time — one unit's strings
// silently claiming the other's namespace and mergeUnitLocales blaming the
// wrong directory.
//
// The rule here keeps the readable form and pays only where the ambiguity
// actually is:
//
//   - a segment starting with a LETTER is title-cased, exactly as before, so
//     crm-demo is still CrmDemo and a stack trace still names the unit;
//   - a segment starting with a DIGIT, where the case marker is invisible, is
//     preceded by `_`: foo-1 → Foo_1, which `foo1` → Foo1 cannot equal.
//
// The digit rule is applied to the FIRST segment too rather than skipped as
// redundant, which also keeps a name like `1foo` (legal per the grammar) from
// emitting `1fooScreen` — an identifier no JavaScript parser accepts.
//
// WHY IT IS INJECTIVE. The output is decodable back to the input, so no two
// inputs can share an output. A unit name contains no upper-case letter and no
// underscore, and this function introduces an upper-case letter only at a
// letter-initial segment's first character and an `_` only immediately before a
// digit-initial segment. So in the output, the segment starts are EXACTLY the
// position 0, every upper-case letter, and every `_` — no segment start goes
// unmarked, and no interior position is marked. Split there, drop the `_`s,
// lower-case the result, rejoin with `-`, and the original name is back. The
// inverse is spelled out and exercised in TestUnitCamelIsInjective.
func unitCamel(unit string) string {
	var b strings.Builder
	for _, part := range strings.Split(unit, "-") {
		if part == "" {
			continue // unreachable for a validated name; a doubled hyphen is refused
		}
		if part[0] >= '0' && part[0] <= '9' {
			b.WriteString("_")
			b.WriteString(part)
			continue
		}
		b.WriteString(strings.ToUpper(part[:1]))
		b.WriteString(part[1:])
	}
	return b.String()
}
