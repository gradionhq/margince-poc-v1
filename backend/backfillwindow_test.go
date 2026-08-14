// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package backendarch

// The CAP-PARAM-4 window set as a fitness function: the contract's four
// enums, the Go validator and the capture_backfill CHECK all state the
// SAME set, derived from the tree rather than remembered here.
//
// Five statements of one closed set is what makes this worth a gate. They
// fail apart silently and in the direction that reads as working: a picker
// offering a window the CHECK refuses hands the user a choice that 500s at
// the insert, and a validator narrower than the contract advertises an
// option the server rejects at the door. The set was widened once already
// (ADR-0106 added 24m/60m to ADR-0063's 3/6/12), and the whole risk of that
// change was doing it in four places out of five.
//
// The contract is the authority and the others are checked against it, in
// keeping with the other root fitness tests: api/crm.yaml is walked, never
// the generated Go.
//
// The GO side is deliberately one statement, not two. The transport used to
// carry its own switch from the `<n>m` wire enum to months, and the first
// widening reached the contract, the validator, the CHECK and both pickers —
// and not that switch, so every new window answered 422 at the door with
// every gate green. It now derives from capture.BackfillWindowMonths, and
// noOtherGoFileStatesTheSet below keeps it that way: a gate that can only
// check the sites it was told about cannot see the one that was forgotten.

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

const (
	backfillValidatorSource = "internal/modules/capture/backfill.go"
	backfillCheckGlob       = "migrations/core"
)

// backfillWindowSchemas are the contract schemas carrying the window enum.
// Named rather than discovered by shape: a schema that grows a `window`
// property for an unrelated reason must not silently join this gate, and a
// schema that LOSES it must fail rather than drop out of the comparison.
var backfillWindowSchemas = []string{
	"BackfillPreviewRequest", "BackfillPreview", "StartBackfillRequest", "BackfillStatus",
}

func TestTheBackfillWindowSetIsOneSet(t *testing.T) {
	contract := contractWindowSets(t)
	months := contract[backfillWindowSchemas[0]]
	for _, schema := range backfillWindowSchemas {
		got, ok := contract[schema]
		if !ok {
			t.Fatalf("%s no longer declares a window enum; the contract is this gate's authority", schema)
		}
		// `none` and null are how a schema says "no run", not window
		// lengths, so they are dropped before the sets are compared —
		// the request that starts a run cannot offer them and the reads
		// must.
		if !sameMonths(got, months) {
			t.Errorf("contract schemas disagree on the window set: %s says %v, %s says %v",
				backfillWindowSchemas[0], months, schema, got)
		}
	}
	if len(months) == 0 {
		t.Fatal("no window months read out of the contract — this gate would pass over anything")
	}

	if got := validatorWindowSet(t); !sameMonths(got, months) {
		t.Errorf("%s admits %v, the contract offers %v — a picker offering a window the validator refuses is a choice that 422s",
			backfillValidatorSource, got, months)
	}
	if got := checkConstraintWindowSet(t); !sameMonths(got, months) {
		t.Errorf("the capture_backfill CHECK accepts %v, the contract offers %v — a window the database refuses is a choice that 500s at the insert",
			got, months)
	}
	noOtherGoFileStatesTheSet(t, months)
}

// noOtherGoFileStatesTheSet is the half that would have caught the transport:
// outside capture's own declaration, no hand-written Go may enumerate the
// window vocabulary. A second statement is a second thing to widen, and the
// one that gets forgotten refuses at a layer the widening never visited.
//
// It looks for the wire spellings (`"12m"` and friends) rather than the
// months, because a bare 12 is an ordinary integer and a "12m" string in Go
// is this vocabulary or nothing.
func noOtherGoFileStatesTheSet(t *testing.T, months []int) {
	t.Helper()
	spellings := make([]string, 0, len(months))
	for _, m := range months {
		spellings = append(spellings, strconv.Quote(strconv.Itoa(m)+"m"))
	}
	err := filepath.WalkDir(".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") ||
			strings.HasSuffix(path, "_test.go") || strings.HasSuffix(path, "_gen.go") {
			return err
		}
		src, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		var found []string
		for _, spelling := range spellings {
			if strings.Contains(string(src), spelling) {
				found = append(found, spelling)
			}
		}
		// One spelling is a mention; two or more is an enumeration, which
		// is the thing that goes stale half-widened.
		if len(found) > 1 {
			t.Errorf("%s enumerates the window vocabulary (%s) — derive it from capture.BackfillWindowMonths instead, or the next widening will reach the contract and not this file",
				filepath.ToSlash(path), strings.Join(found, " "))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// sameMonths compares two window sets AS SETS: same members, whatever order
// the source stated them in. Every caller happens to sort already, so the
// order-independence is not load-bearing today — it is here because the next
// caller will read this name and trust it, and a gate that answered "these
// differ" about two spellings of one set would be the silent failure this
// file exists to prevent elsewhere.
func sameMonths(a, b []int) bool {
	return slices.Equal(slices.Sorted(slices.Values(a)), slices.Sorted(slices.Values(b)))
}

// contractWindowSets reads each named schema's window enum out of
// api/crm.yaml as a sorted month list.
func contractWindowSets(t *testing.T) map[string][]int {
	t.Helper()
	raw, err := os.ReadFile(contractPath)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Components struct {
			Schemas map[string]struct {
				Properties struct {
					Window struct {
						Enum []any `yaml:"enum"`
					} `yaml:"window"`
				} `yaml:"properties"`
			} `yaml:"schemas"`
		} `yaml:"components"`
	}
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	out := map[string][]int{}
	for _, name := range backfillWindowSchemas {
		schema, ok := doc.Components.Schemas[name]
		if !ok {
			continue
		}
		out[name] = monthsOf(schema.Properties.Window.Enum)
	}
	return out
}

// monthsOf keeps the `<n>m` members of a window enum and drops the ones
// that name no length (`none`, and the nullable read's null).
func monthsOf(enum []any) []int {
	var months []int
	for _, member := range enum {
		text, ok := member.(string)
		if !ok || !strings.HasSuffix(text, "m") {
			continue
		}
		n, err := strconv.Atoi(strings.TrimSuffix(text, "m"))
		if err != nil {
			continue
		}
		months = append(months, n)
	}
	sort.Ints(months)
	return months
}

var (
	validatorWindows = regexp.MustCompile(`backfillWindowMonths\s*=\s*\[\]int\{([^}]*)\}`)
	windowCheck      = regexp.MustCompile(`window_months IN \(([^)]*)\)`)
	intMember        = regexp.MustCompile(`\b(\d+)\b`)
)

// validatorWindowSet reads the months the capture module admits. Read from
// the source text rather than by importing the package: the map is
// unexported, and a gate that had to be handed the value it checks is not
// checking anything.
func validatorWindowSet(t *testing.T) []int {
	t.Helper()
	src, err := os.ReadFile(backfillValidatorSource)
	if err != nil {
		t.Fatal(err)
	}
	match := validatorWindows.FindSubmatch(src)
	if match == nil {
		t.Fatalf("no backfillWindowMonths declaration found in %s — this gate cannot check what it cannot find",
			backfillValidatorSource)
	}
	return sortedInts(string(match[1]))
}

// checkConstraintWindowSet reads the months the database admits, from the
// HIGHEST-numbered migration that restates the CHECK — migrations are
// additive and the last statement is the effective one.
func checkConstraintWindowSet(t *testing.T) []int {
	t.Helper()
	entries, err := os.ReadDir(backfillCheckGlob)
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".up.sql") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	var effective string
	for _, name := range names {
		src, err := os.ReadFile(backfillCheckGlob + "/" + name)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(src), "capture_backfill") {
			continue
		}
		if match := windowCheck.FindStringSubmatch(string(src)); match != nil {
			effective = match[1]
		}
	}
	if effective == "" {
		t.Fatal("no capture_backfill window CHECK found in the core migrations")
	}
	return sortedInts(effective)
}

// sortedInts pulls every integer out of a fragment of Go or SQL.
func sortedInts(fragment string) []int {
	var out []int
	for _, match := range intMember.FindAllStringSubmatch(fragment, -1) {
		n, err := strconv.Atoi(match[1])
		if err != nil {
			continue
		}
		out = append(out, n)
	}
	sort.Ints(out)
	return out
}
