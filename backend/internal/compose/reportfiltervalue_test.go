// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// What a filter VALUE may be. The vocabulary check answers which filter names a
// report accepts; this answers what may be bound to one, which is a separate
// question and used to have no answer at all.

import (
	"errors"
	"strings"
	"testing"
)

// A filter value the engine cannot bind must be refused as the caller's
// mistake. It used to reach Postgres and fail there, which httperr can only
// mask as an opaque 500 — so the caller most likely to hit it (one who spelled
// a year the way JSON spells a year) was sent looking for an outage.
func TestAFilterValueTheEngineCannotBindIsRefusedNotRun(t *testing.T) {
	for name, value := range map[string]any{
		"a number":  float64(2026),
		"an object": map[string]any{"gte": "2026"},
		"an array":  []any{"2026"},
	} {
		_, err := reportFilterValue("period_year", value)
		var refused *FilterValueNotAllowedError
		if !errors.As(err, &refused) {
			t.Errorf("%s → %v, want FilterValueNotAllowedError", name, err)
			continue
		}
		if refused.Filter != "period_year" {
			t.Errorf("%s: refusal names %q, want period_year", name, refused.Filter)
		}
	}
}

// The refusal has to say what to do. A caller who sent 2026 needs to be told to
// send "2026"; naming the accepted SHAPES is what turns a retry into a success
// instead of the same call again.
func TestTheFilterValueRefusalSaysHowToFixIt(t *testing.T) {
	_, err := reportFilterValue("period_year", float64(2026))
	var refused *FilterValueNotAllowedError
	if !errors.As(err, &refused) {
		t.Fatalf("got %v", err)
	}
	code, message := refused.MessageFault()
	if code != reportFieldNotAllowedCode {
		t.Errorf("code = %q, want the contract's one declared 422 code", code)
	}
	if !strings.Contains(message, "period_year") || !strings.Contains(message, "quote") {
		t.Errorf("the refusal does not name the filter and the fix: %s", message)
	}
}

// Text and booleans both bind: a report's filters are text-valued columns and
// boolean-valued expressions alike (deals-by-stage's partner_sourced/stalled),
// and narrowing to text alone would break the ones that already work.
func TestTextAndBooleanFilterValuesBind(t *testing.T) {
	for name, value := range map[string]any{
		"text":    "won",
		"boolean": true,
	} {
		bound, err := reportFilterValue("whatever", value)
		if err != nil {
			t.Errorf("%s was refused: %v", name, err)
		}
		if bound != value {
			t.Errorf("%s bound as %v, want it unchanged", name, bound)
		}
	}
}

// The refusal speaks the caller's vocabulary, not Go's. A 422 naming float64 or
// map[string]interface {} fails to locate the mistake of someone who wrote JSON
// and has never heard of either, and it says more about how this server is
// built than a client is owed.
func TestTheFilterValueRefusalNamesNoGoType(t *testing.T) {
	for _, value := range []any{float64(2026), map[string]any{"a": 1}, []any{"x"}} {
		_, err := reportFilterValue("period_year", value)
		var refused *FilterValueNotAllowedError
		if !errors.As(err, &refused) {
			t.Fatalf("%v was not refused", value)
		}
		_, message := refused.MessageFault()
		for _, leak := range []string{"float64", "int64", "interface {}", "map[string]", "[]interface"} {
			if strings.Contains(message, leak) {
				t.Errorf("the refusal leaks the Go type %q: %s", leak, message)
			}
		}
	}
}
