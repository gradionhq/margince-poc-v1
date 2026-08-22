// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package backendarch

// The browser and the server must scale money by the SAME table, or the integer
// they exchange means two different amounts.
//
// This is not a hypothetical drift. The frontend module first read its digit
// count from Intl, on the reasonable-sounding grounds that the runtime already
// ships one. It ships a DIFFERENT one: Intl follows CLDR, which records how a
// currency is USED, and this table follows ISO 4217, which records what the
// standard ASSIGNS. They disagree on ten codes, two of them ordinary spendable
// money — CLDR gives MGA and IRR zero digits where ISO gives two, and IQD zero
// where ISO gives three. A browser scaling by CLDR against a server scaling by
// ISO is a hundredfold disagreement per affected currency: exactly the defect
// the money sweep removed, reintroduced one currency over, and invisible
// because every OTHER currency agrees.
//
// So the TypeScript table is a deliberate mirror rather than a second answer,
// and this is what makes "mirror" true. It compares both directions: a code
// added to one side and not the other fails, and so does a changed digit count.

import (
	"os"
	"regexp"
	"strconv"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/values"
)

const frontendMinorUnits = "../frontend/src/format/minorunits.ts"

// tsMinorUnitEntry reads one `XXX: N` pair out of the TypeScript table. The
// table is hand-written and formatted by biome, so several pairs share a line.
var tsMinorUnitEntry = regexp.MustCompile(`\b([A-Z]{3}):\s*(\d)\b`)

func TestTheFrontendMinorUnitTableMatchesTheGoOne(t *testing.T) {
	source, err := os.ReadFile(frontendMinorUnits)
	if err != nil {
		t.Fatalf("reading the frontend minor-unit table: %v", err)
	}
	const marker = "MINOR_UNIT_EXCEPTIONS"
	start := indexAfter(string(source), marker+": Readonly<Record<string, number>> = {")
	if start < 0 {
		t.Fatalf("%s no longer declares %s as an object literal — this gate is reading a shape that is gone", frontendMinorUnits, marker)
	}
	end := indexAfter(string(source)[start:], "};")
	if end < 0 {
		t.Fatalf("%s's %s literal is unterminated", frontendMinorUnits, marker)
	}

	inTS := map[string]int{}
	for _, m := range tsMinorUnitEntry.FindAllStringSubmatch(string(source)[start:start+end], -1) {
		digits, convErr := strconv.Atoi(m[2])
		if convErr != nil {
			t.Fatalf("%s: %q is not a digit count", frontendMinorUnits, m[2])
		}
		inTS[m[1]] = digits
	}
	if len(inTS) == 0 {
		t.Fatal("no entries parsed out of the frontend table — a gate that reads nothing agrees with everything")
	}

	inGo := values.MinorUnitExceptions()
	for code, want := range inGo {
		got, present := inTS[code]
		switch {
		case !present:
			t.Errorf("%s is an exception in Go (%d digits) and absent from the frontend table, so the browser will scale it at two and the server at %d", code, want, want)
		case got != want:
			t.Errorf("%s: Go says %d digits, the frontend says %d — the integer they exchange means two different amounts", code, want, got)
		}
	}
	for code, got := range inTS {
		if _, present := inGo[code]; !present {
			t.Errorf("%s is an exception in the frontend table (%d digits) and absent from Go's, so the server will scale it at two", code, got)
		}
	}
}

func indexAfter(haystack, needle string) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i + len(needle)
		}
	}
	return -1
}
