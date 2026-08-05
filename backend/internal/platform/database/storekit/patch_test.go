// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package storekit

// Patch is the ONE spelling of the partial-UPDATE write shape, so a column it
// can render twice is a 42601 waiting in every store that sets one from two
// independent branches. These pin the property at the type rather than at the
// call sites: an assignment list is a set of columns, and the SQL it renders is
// the proof.

import (
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

	for i, want := range []any{"customer", "t2"} {
		if p.args[i] != want {
			t.Errorf("args[%d] = %v, want %v", i, p.args[i], want)
		}
	}
	// Each assignment must name its own 1-based placeholder, in order.
	for i, set := range p.sets {
		if !strings.HasSuffix(set, "$"+itoa(i+1)) {
			t.Errorf("assignment %q does not bind $%d, so a repeat rewrote the wrong slot", set, i+1)
		}
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

	if len(p.sets) != 1 {
		t.Fatalf("%d assignments for one cf_ column: %v", len(p.sets), p.sets)
	}
	if p.args[0] != 30 {
		t.Errorf("args[0] = %v, want 30 (the last write)", p.args[0])
	}
}

func itoa(n int) string {
	if n < 10 {
		return string(rune('0' + n))
	}
	return itoa(n/10) + itoa(n%10)
}
