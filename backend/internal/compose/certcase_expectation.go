// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// What every field-grounding certification case says about its gate: the one
// comparison of what the gate let through against what the scenario says the
// fixture grounds, and the one rendering of what the gate refused.
//
// Both live beside the cases rather than inside any one of them because several
// sites ask the same question — the page extraction, the signature read, the site
// profile and the page facts all ground NAMED fields against evidence — and
// copies of a shared answer drift. The copy that drifts is the one that stops
// failing, so the site whose scenarios quietly went green is the site nobody
// looks at. Shared machinery kept in the first site that needed it is the same
// hazard wearing a filename: the second caller inherits an owner that has no
// reason to keep the shape it depends on.
//
// The classify case deliberately does not use the comparison: it answers about a
// position in a batch rather than a named field, and folding a second question
// into this one would make both harder to read than the duplication was.

import (
	"fmt"
	"maps"
	"slices"
)

// gateRefusals renders the gate's own drops in the gate's own vocabulary. A
// whole-reply refusal carries no field name, so it reads as one.
func gateRefusals(dropped []droppedFinding) []string {
	out := make([]string, 0, len(dropped))
	for _, d := range dropped {
		if d.Field == "" {
			out = append(out, "the gate refused the whole reply: "+d.Reason)
			continue
		}
		out = append(out, fmt.Sprintf("the gate dropped %s: %s", d.Field, d.Reason))
	}
	return out
}

// groundedValues keys a gate's surviving fields by name — the shape the
// comparison asks about, since a scenario names a field and never a position.
//
// Every gate that feeds this drops a repeated field before returning, so a
// second entry for one name is not something a reply can produce here; a later
// one would win, which is the same thing the gates' own first-wins rule already
// prevents.
func groundedValues(fields []evidencedField) map[string]string {
	out := make(map[string]string, len(fields))
	for _, f := range fields {
		out[f.Field] = f.Value
	}
	return out
}

// expectationDisagreements names every expected field the grounded result does
// not carry. All of them, not the first: a run that read one field right and two
// wrong is not the near miss one line would read as.
//
// It is a SUBSET claim. A grounded field the scenario never named is not a
// disagreement, because a real page grounds more than a scenario cares to pin and
// demanding exhaustiveness would fail a read for being richer than its author
// imagined.
//
// Values compare under normalizeEvidence — the same presentation-only relaxation
// the gates apply to evidence — so a scenario neither fails on a straightened
// apostrophe nor passes on a reworded value.
//
// Sorted so a run with two disagreements names them in the same order every time.
func expectationDisagreements(expected, grounded map[string]string) []string {
	var out []string
	for _, name := range slices.Sorted(maps.Keys(expected)) {
		value, survived := grounded[name]
		switch {
		case !survived:
			out = append(out, fmt.Sprintf("no surviving %s, which the scenario expects", name))
		case normalizeEvidence(value) != normalizeEvidence(expected[name]):
			out = append(out, fmt.Sprintf("%s reads %q where the scenario expects %q", name, value, expected[name]))
		}
	}
	return out
}
