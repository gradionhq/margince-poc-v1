// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package storekit

// Patch is the ONE spelling of the partial-UPDATE write shape, so a column it
// can render twice is a 42601 waiting in every store that sets one from two
// independent branches. These pin the property at the type rather than at the
// call sites: an assignment list is a set of columns, and the SQL it renders is
// the proof.

import (
	"reflect"
	"strings"
	"testing"
)

// Two branches of one request can legitimately want the same column — an
// `updated_at` bump next to whichever field the caller actually sent. The second
// write must land on the first's assignment, not beside it: Postgres rejects
// `SET updated_at = $1, updated_at = $2` with 42601, so an append would turn a
// legal request into a 500.
func TestSetTwiceOnOneColumnRendersOneAssignment(t *testing.T) {
	p := NewPatch()
	p.Set("display_name", "old name", "new name")
	p.Set("updated_at", "t0", "t1")
	p.Set("updated_at", "t0", "t2")

	sql := strings.Join(p.sets, ", ")
	if got := strings.Count(sql, "updated_at ="); got != 1 {
		t.Fatalf("updated_at assigned %d times in %q, want exactly 1 — Postgres rejects a duplicate column", got, sql)
	}
	// One bind value per assignment is what makes the slot index name the
	// placeholder it renders; the apply paths clone rather than append to keep it.
	if len(p.sets) != len(p.args) {
		t.Fatalf("%d assignments against %d bind values: the placeholders cannot all resolve", len(p.sets), len(p.args))
	}
}

// Last write wins on the value, and the placeholder still points at it. A repeat
// that updated the SQL but not the argument (or vice versa) would bind the wrong
// value into the right column, which is worse than the error it replaced.
func TestTheLastSetSuppliesTheBoundValue(t *testing.T) {
	p := NewPatch()
	p.Set("lifecycle", "prospect", "customer")
	p.Set("updated_at", "t0", "t1")
	p.Set("updated_at", "t0", "t2")

	if want := []any{"customer", "t2"}; !reflect.DeepEqual(p.args, want) {
		t.Errorf("args = %v, want %v", p.args, want)
	}
	// The exact fragments, not a suffix match: "$1" is a suffix of "$21" too, and
	// a repeat that rewrote the wrong slot is precisely a placeholder mix-up.
	if want := []string{"lifecycle = $1", "updated_at = $2"}; !reflect.DeepEqual(p.sets, want) {
		t.Errorf("sets = %v, want %v", p.sets, want)
	}
}

// The audit diff describes the whole change, so before stays the value the row
// actually held when the transaction read it. A second Set's oldVal is either the
// same or a re-read of a value this transaction already changed; letting it
// overwrite would make the diff claim the column started where the first write
// left it.
func TestARepeatKeepsTheOriginalBeforeImage(t *testing.T) {
	p := NewPatch()
	p.Set("updated_at", "the row's real prior value", "t1")
	p.Set("updated_at", "t1", "t2")

	if got := p.Before()["updated_at"]; got != "the row's real prior value" {
		t.Errorf("before image = %v, want the value read from the row", got)
	}
	if got := p.After()["updated_at"]; got != "t2" {
		t.Errorf("after image = %v, want the last value written", got)
	}
}

// A cf_ column comes through setQuoted, whose SET fragment is quoted while its
// audit key stays the wire name. The de-duplication has to key on that same wire
// name, or the two paths could each render an assignment for one column.
func TestSetQuotedDeduplicatesOnTheWireName(t *testing.T) {
	p := NewPatch()
	p.setQuoted("cf_employee_count", 10, 20)
	p.setQuoted("cf_employee_count", 10, 30)

	// The rendering matters as much as the count: the overwrite branch re-renders
	// the left-hand side, and a slip that passed the bare column there would
	// splice a catalog-derived identifier into the UPDATE unquoted — the one thing
	// setQuoted exists to prevent.
	if want := []string{`"cf_employee_count" = $1`}; !reflect.DeepEqual(p.sets, want) {
		t.Fatalf("sets = %v, want %v", p.sets, want)
	}
	if p.args[0] != 30 {
		t.Errorf("args[0] = %v, want 30 (the last write)", p.args[0])
	}
	if got := p.After()["cf_employee_count"]; got != 30 {
		t.Errorf("after image keys on the bare wire name: got %v for cf_employee_count", got)
	}
}

// Two writers that disagree about the identifier are not the same assignment.
// A core column and a catalog column sharing a name is a programming error, and
// merging them would either drop a validated value or decide the quoting — so
// they keep separate slots and the statement still fails loudly, as it did before
// the merge existed. Unreachable today (every catalog column is cf_-prefixed),
// which is exactly why it needs pinning rather than trusting.
func TestADisagreementAboutTheIdentifierIsNotMerged(t *testing.T) {
	p := NewPatch()
	p.Set("lifecycle", "prospect", "customer")
	p.setQuoted("lifecycle", "prospect", "from the catalog")

	if len(p.sets) != 2 {
		t.Fatalf("sets = %v, want the bare and quoted spellings kept apart", p.sets)
	}
	if p.args[0] != "customer" {
		t.Errorf("the core value was overwritten by the catalog one: args[0] = %v", p.args[0])
	}
}
