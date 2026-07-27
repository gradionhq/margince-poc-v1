// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The one comparison every field-grounding certification case makes: what its
// gate let through, against what its scenario says the fixture grounds.
//
// It lives beside the cases rather than inside any one of them because three
// sites ask the same question — the page extraction, the signature read and the
// site profile all ground NAMED fields against evidence — and three copies of a
// comparison drift. The copy that drifts is the one that stops failing, so the
// site whose scenarios quietly went green is the site nobody looks at.
//
// The classify case deliberately does not use this: it answers about a position
// in a batch rather than a named field, and folding a second question into this
// one would make both harder to read than the duplication was.

import (
	"fmt"
	"maps"
	"slices"
)

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
