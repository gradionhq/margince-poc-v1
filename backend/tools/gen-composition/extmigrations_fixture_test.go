// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package main

// The committed negative migration fixtures, driven through the same scan a
// `make composition` runs.
//
// scan_test.go already covers both RULES over temp-dir strings. What these add
// is a unit a HUMAN can point the generator at — `cp -R
// fixtures/extensions/bad-unprefixed-table extensions/ && make composition`
// reproduces the refusal by hand, which is what makes the wall demonstrable in
// a review rather than merely asserted in a suite. They also pin the POSITION,
// which is the part an author acts on: the identifier a budget refusal names is
// derived and appears nowhere in the file as written, so a refusal without a
// line number sends them looking for a string that is not there.

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fixtureRoot is the committed fixtures tree, relative to this package.
const fixtureRoot = "../../../fixtures/extensions"

func TestNegativeMigrationFixturesFailGenerationWithPosition(t *testing.T) {
	for _, tc := range []struct {
		unit string
		// wantPosition is the file:line the author has to go and edit.
		wantPosition string
		wantRule     string
	}{
		{
			unit:         "bad-unprefixed-table",
			wantPosition: "migrations/0001_note.up.sql:7",
			wantRule:     "outside the unit's namespace",
		},
		{
			unit:         "bad-overbudget-table",
			wantPosition: "migrations/0001_note.up.sql:7",
			wantRule:     "PostgreSQL truncates silently past 63",
		},
	} {
		t.Run(tc.unit, func(t *testing.T) {
			_, err := scanUnit(tc.unit, filepath.Join(fixtureRoot, tc.unit))
			if err == nil {
				t.Fatal("the fixture composed; it exists to be refused")
			}
			if !strings.Contains(err.Error(), tc.wantPosition) {
				t.Errorf("the refusal does not carry %s — an author has no other handle on a derived identifier:\n%v", tc.wantPosition, err)
			}
			if !strings.Contains(err.Error(), tc.wantRule) {
				t.Errorf("the refusal does not state the rule (%q):\n%v", tc.wantRule, err)
			}
			if !strings.Contains(err.Error(), tc.unit) {
				t.Errorf("the refusal does not name the unit:\n%v", err)
			}
		})
	}
}

// TestTheNamespaceWallFixtureDeclaresTheSameKeyAsCrmDemo holds the two halves
// of the wall fixture together.
//
// crm-nosy's whole job is to declare the SAME key name crm-demo declares, so
// that the run-time demonstration (compose's
// TestCrmDemoSigningKeyIsUnreachableFromASecondUnit) is about a namespace and
// not about two units that happened to pick different names. If either side
// renames its key, that demonstration silently becomes vacuous — it would pass
// for the wrong reason — so the agreement is pinned here, where both files are
// readable at once.
func TestTheNamespaceWallFixtureDeclaresTheSameKeyAsCrmDemo(t *testing.T) {
	const key = `{Key: "signing", Scope: extension.SecretScopeWorkspace}`
	for _, source := range []struct {
		path string
		// removable marks a file that a legitimate operation deletes. The
		// FIXTURE is part of the repository and its absence is a defect; the
		// installed UNIT is one `git rm -r extensions/crm-demo` away, which is
		// the documented removal recipe. A t.Fatal on the second made removal a
		// THREE-place operation — delete the unit, delete its core screen, and
		// edit this test — and the third place is one nobody would find until
		// `make check` failed in a package about migrations. Removing a unit
		// must not require editing the core's tests.
		removable bool
	}{
		{path: filepath.Join(fixtureRoot, "crm-nosy", "crmnosy.go")},
		{path: filepath.Join("..", "..", "..", "extensions", "crm-demo", "crmdemo.go"), removable: true},
	} {
		raw, err := os.ReadFile(source.path) // #nosec G304 -- a fixed path inside the repository under test
		if errors.Is(err, fs.ErrNotExist) && source.removable {
			// The pairing is vacuous rather than violated: with crm-demo gone
			// there is no second declaration to agree with, and the run-time
			// demonstration it guards does not compose either.
			t.Logf("%s is absent — this installation removed the unit, so there is no pair to hold", source.path)
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(raw), key) {
			t.Errorf("%s no longer declares %s — the namespace-wall demonstration would then compare two different key names and pass vacuously", source.path, key)
		}
	}
}
