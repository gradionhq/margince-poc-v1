// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package backendarch

// Job args carry REFERENCES, never content. The erasure engine neutralizes an
// in-flight job by scrubbing the row the job names — comms_outbound goes to
// `parked` and the waking job finds nothing to send. That only works while the
// job holds an id and not a copy: args carrying a body or an address would be a
// second store of subject data that Art. 17 never reaches, sitting in a table
// with no workspace column and no RLS.
//
// TWO arms, and they answer different questions. Neither subsumes the other,
// which is why the second was not dropped when the first was written.
//
// COVERAGE — every field of every COMPILED args struct this build registers
// must be declared in api/jobs.yaml, as an id or as a scalar with the reason it
// is safe. Nothing is inferred from a field's name here, so Snippet, Note and
// Domain are under the same rule as Body, which is the gap a word list cannot
// close. This is the positive assertion: it is total over the fields that
// exist, not over the names somebody thought of.
//
// SUSPICION — a field whose NAME reads like content must carry a rationale even
// when it is declared an id. Coverage alone would let `Body: id` through in
// silence, and that is exactly the line a reviewer should have to argue for. A
// word list is a poor detector and a fine prompt: it cannot decide whether a
// field is safe, and it can insist that somebody said so.
//
// A declared rationale is a WAIVER — a ratified exception to "an args field is
// an id" — so the reasons api/jobs.yaml carries are read as one, and held to
// what gatekit holds every other gate's exceptions to: a reason that states a
// cost, and an entry that still describes a field needing one.
//
// What holds the arms up underneath: gen-jobs refuses a declared field that is
// neither an id nor an argued-for scalar and refuses an empty mapping, and the
// census refuses a declaration and a struct that disagree.

import (
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/compose"
	"github.com/gradionhq/margince/backend/internal/shared/gatekit"
)

// contentWords name a payload rather than a pointer to one, matched as a
// case-insensitive substring of the field name. A field they match is not
// thereby wrong — RecipientEmail could genuinely hold an id — it is thereby
// UNARGUED, and the declaration is where the argument goes.
var contentWords = []string{
	"address", "body", "content", "email", "message",
	"name", "payload", "phone", "subject", "text",
}

func TestEveryJobArgsFieldIsAnIdOrAnArguedForScalar(t *testing.T) {
	census, err := compose.NewJobCensus()
	if err != nil {
		t.Fatalf("building the job census: %v", err)
	}
	argued := gatekit.Waive(declaredArgsRationales(census))

	kinds := map[string]struct{}{}
	for _, field := range census.ArgsFields() {
		kinds[field.Kind] = struct{}{}
		name := field.GoType + "." + field.Name

		if !field.Declared {
			t.Errorf("%s is not declared in api/jobs.yaml — say what it carries: `%s: id`, or a scalar with the reason a value that is not an id is safe in a table Art. 17 erasure never reaches.",
				name, field.Name)
			continue
		}
		word, suspect := contentWordIn(field.Name)
		switch {
		case field.Scalar:
			if !argued.Waived(t, name) {
				t.Errorf("%s is declared a scalar with no rationale — generation refuses that, so the declared table has been hand-edited; regenerate with `make gen`.", name)
			}
		case suspect:
			if !argued.Waived(t, name) {
				t.Errorf("%s is declared an id but its name reads like content (it contains %q), and nothing says why. A job names a row and the worker reads it; a copy in the args is a second store of subject data Art. 17 erasure never reaches. Give the field a reason in api/jobs.yaml — `%s: {reason: …}` — or make it carry an id.",
					name, word, field.Name)
			}
		}
	}

	if len(kinds) < jobArgsFloor {
		t.Fatalf("inspected the args of only %d job kinds, expected at least %d — the census matched almost nothing and this gate would pass vacuously", len(kinds), jobArgsFloor)
	}
	// Staleness is only meaningful once the sweep above actually ran: on the
	// vacuity Fatal every entry would report as unmatched, burying the one
	// failure that explains all of them. An entry reported here is a rationale
	// no arm asks for — an id whose name the word list does not flag — so it is
	// prose nobody is holding to anything; delete it from api/jobs.yaml.
	argued.AssertAllMatched(t)
}

// declaredArgsRationales is every args field the contract argues for, keyed as
// this gate names a field.
//
// It reads the same walk the gate does, so the waiver set and the subjects it
// is consulted over have exactly one population: a rationale on a kind this
// build does not register would otherwise report stale on a deployment that
// merely does not run that kind.
func declaredArgsRationales(census *compose.JobCensus) map[string]string {
	rationales := map[string]string{}
	for _, field := range census.ArgsFields() {
		if field.Reason != "" {
			rationales[field.GoType+"."+field.Name] = field.Reason
		}
	}
	return rationales
}

// contentWordIn reports the first content word a field name contains, and
// whether it contains one at all.
func contentWordIn(field string) (string, bool) {
	lower := strings.ToLower(field)
	for _, word := range contentWords {
		if strings.Contains(lower, word) {
			return word, true
		}
	}
	return "", false
}
