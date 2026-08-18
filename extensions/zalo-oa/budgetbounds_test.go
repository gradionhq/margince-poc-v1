// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package zalooa

// The request ceiling's bounds are written in four places that cannot import
// each other, so this is the one that holds them together.
//
// The migration's CHECK is what the database enforces. The contract declares the
// same range twice — once on the connect response and once on the status
// response — because the connection object is spelled member for member in both.
// The unit screen carries the maximum as a constant, because a generated client
// carries a schema's types and not its numeric limits.
//
// A comment promising that they move together would be a promise; deriving the
// numbers from the files and comparing them is a gate. Without it, raising the
// ceiling in the migration alone leaves the card telling an operator the range is
// something else, with every other check green.

import (
	"os"
	"regexp"
	"strconv"
	"testing"
)

// The bounds this unit was built around. They appear here as the ASSERTION and
// nowhere else in the Go code — nothing at runtime re-checks what the CHECK
// already enforces.
const (
	wantMinBudget = 2
	wantMaxBudget = 200
)

// TestTheRequestCeilingsBoundsAgreeEverywhereTheyAreWritten reads the three
// files that carry the range and fails when any of them has moved alone.
func TestTheRequestCeilingsBoundsAgreeEverywhereTheyAreWritten(t *testing.T) {
	t.Run("the migration's CHECK", func(t *testing.T) {
		sql := readFile(t, "migrations/0003_poll_request_budget.up.sql")
		found := regexp.MustCompile(
			`poll_request_budget BETWEEN (\d+) AND (\d+)`).FindStringSubmatch(sql)
		if found == nil {
			t.Fatal("the migration no longer declares a BETWEEN range for poll_request_budget; the column's bounds are what every other number here is checked against")
		}
		assertBound(t, "the CHECK's lower bound", found[1], wantMinBudget)
		assertBound(t, "the CHECK's upper bound", found[2], wantMaxBudget)
	})

	t.Run("both copies in the contract", func(t *testing.T) {
		contract := readFile(t, "api/crm.yaml")
		// Every declaration of the property, in document order: the connect
		// response and the status response. Both are matched rather than the
		// first, because two copies can also drift from each other.
		declarations := regexp.MustCompile(
			`poll_request_budget:\s*\n\s*type: integer\s*\n\s*minimum: (\d+)\s*\n\s*maximum: (\d+)`).
			FindAllStringSubmatch(contract, -1)
		if len(declarations) != 2 {
			t.Fatalf("the contract declares poll_request_budget with a minimum and maximum %d time(s), want 2 — the connection object is spelled member for member in the connect and status responses, so a copy that lost its bounds is a response nothing constrains", len(declarations))
		}
		for i, declared := range declarations {
			assertBound(t, "contract copy "+strconv.Itoa(i+1)+"'s minimum", declared[1], wantMinBudget)
			assertBound(t, "contract copy "+strconv.Itoa(i+1)+"'s maximum", declared[2], wantMaxBudget)
		}
	})

	t.Run("the screen's constant", func(t *testing.T) {
		screen := readFile(t, "frontend/screen.tsx")
		found := regexp.MustCompile(
			`MAX_REQUEST_BUDGET = (\d+)`).FindStringSubmatch(screen)
		if found == nil {
			t.Fatal("the unit screen no longer declares MAX_REQUEST_BUDGET; the card shows the budget against that ceiling, so a screen without it is a card that cannot say what the range is")
		}
		assertBound(t, "the screen's ceiling", found[1], wantMaxBudget)
	})
}

// readFile reads one of the unit's own files, failing the test rather than the
// caller — a file this test cannot open is a file that was moved, which is
// exactly the drift it exists to catch.
func readFile(t *testing.T, path string) string {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	return string(content)
}

// assertBound compares one written number against what this unit was built
// around, naming which of the four it is so a failure says where to look.
func assertBound(t *testing.T, what, written string, want int) {
	t.Helper()
	got, err := strconv.Atoi(written)
	if err != nil {
		t.Fatalf("%s is %q, which is not a number", what, written)
	}
	if got != want {
		t.Fatalf("%s is %d, and this unit is built around %d — the migration's CHECK, both contract copies and the screen's constant have to move together or the product tells an operator a range the database will refuse", what, got, want)
	}
}
