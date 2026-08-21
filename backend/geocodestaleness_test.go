// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package backendarch

// Every writer of a company's address must invalidate its coordinates.
//
// This is the one rule the geocoding design rests on, and it is exactly the
// rule that rots: a new address writer added later looks complete and correct
// on its own, and the defect it introduces is invisible — a radius query that
// keeps answering from the previous address, reporting success, until somebody
// notices a customer listed in the wrong city.
//
// So the obligation is derived rather than remembered. Any statement that
// writes an address column must either invalidate in the same statement or be
// ratified here with the reason it does not have to.

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/gatekit"
)

// addressWrite matches a statement that sets an address column.
var addressWrite = regexp.MustCompile(`(?i)(UPDATE|INSERT INTO)[\s\S]*address_(line1|line2|city|region|postal_code|country)\s*=`)

// setsAddressColumn matches the patch-builder form, which writes through a
// column name rather than SQL.
var setsAddressColumn = regexp.MustCompile(`Set\("address_(line1|line2|city|region|postal_code|country)"`)

// addressWritersExempt ratifies the writers that need no invalidation, each
// with the reason. A writer here that later starts needing one fails as loudly
// as one that was never covered.
var addressWritersExempt = gatekit.Waive(map[string]string{
	"merge.go": "a merge writes the surviving row's address from the row being merged INTO it, and " +
		"mergeOrganizations invalidates once for the survivor after every field is copied — " +
		"invalidating per column would queue the same company six times",
	"person.go": "writes a PERSON's address, not a company's. The columns are named alike on both " +
		"tables, which is why the pattern above matches it; only organization rows carry coordinates.",
	"coldstartprofile.go": "the cold-start profile applies an address and enqueues its own geocode " +
		"through applyProfileField, which invalidates for the whole apply rather than per column",
})

func TestEveryWriterOfACompanyAddressInvalidatesItsCoordinates(t *testing.T) {
	root := filepath.Join("internal", "modules", "people")
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("reading %s: %v", root, err)
	}
	checked := 0
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") || name == "geocode.go" {
			continue
		}
		body, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			t.Fatalf("reading %s: %v", name, err)
		}
		src := string(body)
		if !addressWrite.MatchString(src) && !setsAddressColumn.MatchString(src) {
			continue
		}
		checked++
		if addressWritersExempt.Waived(t, name) {
			continue
		}
		// Two acceptable forms: the Go helper, or geocode_status written in the
		// same SQL statement as the address. The second is what a table-driven
		// update uses, and it is stronger — one statement cannot interleave.
		// Three acceptable forms: addressChanged (invalidate + enqueue, which is
		// what a writer should call), the bare invalidate, or geocode_status
		// written in the same SQL statement as the address.
		if !strings.Contains(src, "addressChanged") &&
			!strings.Contains(src, "invalidateGeocode") &&
			!strings.Contains(src, "geocode_status") {
			t.Errorf("%s writes a company's address and never calls invalidateGeocode — a radius query "+
				"will keep answering from the PREVIOUS address, reporting success. Invalidate in the same "+
				"transaction as the write, or ratify the file in addressWritersExempt with the reason.", name)
		}
	}
	if checked == 0 {
		t.Fatal("no address writer was found — this gate checked nothing, which means the patterns above " +
			"have drifted from how addresses are written")
	}
	addressWritersExempt.AssertAllMatched(t)
}
