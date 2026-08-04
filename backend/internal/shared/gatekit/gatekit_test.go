// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package gatekit

import (
	"fmt"
	"strings"
	"testing"
)

// recorder captures what a gate would have reported, so a test can assert on
// the report instead of failing with it. testing.TB is embedded for its
// unexported methods; every method this package calls is overridden.
type recorder struct {
	testing.TB
	errs []string
}

func (r *recorder) Errorf(format string, args ...any) {
	r.errs = append(r.errs, fmt.Sprintf(format, args...))
}

func (r *recorder) Helper() {}

func (r *recorder) joined() string { return strings.Join(r.errs, "\n") }

func TestWaivedRatifiesAKnownSubjectAndRefusesAnUnknownOne(t *testing.T) {
	w := Waive(map[string]string{
		"record_grant": "no RBAC object may name it, so the row is refused for everyone forever",
	})
	rec := &recorder{TB: t}
	if !w.Waived(rec, "record_grant") {
		t.Error("a ratified subject was not waived")
	}
	if w.Waived(rec, "person") {
		t.Error("an unratified subject was waived")
	}
	if len(rec.errs) != 0 {
		t.Errorf("a well-formed waiver reported: %s", rec.joined())
	}
}

func TestAReasonlessWaiverFailsWhereItIsReliedOn(t *testing.T) {
	w := Waive(map[string]string{"enrich": "enrich"})
	rec := &recorder{TB: t}
	if !w.Waived(rec, "enrich") {
		t.Error("the subject stays waived — the reason is the defect, not the ratification")
	}
	if len(rec.errs) != 1 || !strings.Contains(rec.joined(), "what it costs") {
		t.Errorf("a reason that only repeats its key was accepted: %s", rec.joined())
	}
}

func TestAWaiverMatchingNothingIsReportedAsStale(t *testing.T) {
	w := Waive(map[string]string{
		"live":  "reached by the lookup below, so it must not be reported",
		"stale": "describes a subject that no longer exists anywhere in the tree",
	})
	rec := &recorder{TB: t}
	if !w.Waived(rec, "live") {
		t.Fatal("setup: the live subject was not waived")
	}
	w.AssertAllMatched(rec)
	if len(rec.errs) != 1 {
		t.Fatalf("want exactly one stale report, got %d: %s", len(rec.errs), rec.joined())
	}
	if !strings.Contains(rec.errs[0], "stale") {
		t.Errorf("the report names the wrong entry: %s", rec.errs[0])
	}
}

// Enumeration is deterministic because a gate that walks its waivers reports
// findings in that order, and an unstable order makes a failure unreadable.
func TestSubjectsEnumeratesInADeterministicOrder(t *testing.T) {
	w := Waive(map[string]string{
		"c": "the third subject, ratified for the reason stated right here",
		"a": "the first subject, ratified for the reason stated right here",
		"b": "the second subject, ratified for the reason stated here",
	})
	for range 8 {
		if got := w.Subjects(); strings.Join(got, ",") != "a,b,c" {
			t.Fatalf("Subjects() = %v, want a,b,c in every call", got)
		}
	}
}

// Reading a reason is relying on the waiver, so it counts as a match — a gate
// that reports its waivers must not then be told they are all stale.
func TestReasonMarksTheSubjectMatched(t *testing.T) {
	w := Waive(map[string]string{"a": "ratified for the reason stated right here in full"})
	rec := &recorder{TB: t}
	if _, ok := w.Reason(rec, "a"); !ok {
		t.Fatal("Reason did not find a ratified subject")
	}
	w.AssertAllMatched(rec)
	if len(rec.errs) != 0 {
		t.Errorf("reading a reason left the subject unmatched: %s", rec.joined())
	}
}

// A nil set behaves as an empty one. Gates hold these in case tables where a
// nil map means "no exceptions", and Go lets them read a nil map freely — the
// type has to preserve that, or converting such a gate replaces a passing test
// with a panic that takes its whole binary down.
func TestANilWaiversBehavesAsAnEmptySet(t *testing.T) {
	var w *Waivers[string]
	rec := &recorder{TB: t}
	if w.Waived(rec, "anything") {
		t.Error("a nil waiver set ratified a subject")
	}
	if _, ok := w.Reason(rec, "anything"); ok {
		t.Error("a nil waiver set produced a reason")
	}
	if got := w.Subjects(); len(got) != 0 {
		t.Errorf("Subjects() on a nil set = %v, want empty", got)
	}
	w.AssertAllMatched(rec)
	if len(rec.errs) != 0 {
		t.Errorf("a nil waiver set reported: %s", rec.joined())
	}
}

func TestWaiveCopiesItsInputSoALaterMutationCannotWidenTheSet(t *testing.T) {
	entries := map[string]string{"a": "ratified for the reason stated right here in full"}
	w := Waive(entries)
	entries["b"] = "smuggled in after ratification, which must not be honoured"
	rec := &recorder{TB: t}
	if w.Waived(rec, "b") {
		t.Error("a post-construction mutation widened the waiver set")
	}
}
