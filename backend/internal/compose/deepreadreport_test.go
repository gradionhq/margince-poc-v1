// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

import (
	"errors"
	"testing"
)

// The dossier's warnings are the last thing a human reads before confirming
// a draft, so each one has to state what actually happened. The legal gate
// withholds the trio for two unrelated causes; the caveat names the one that
// fired, never the other in its place.
func TestReadWarningsStateTheCauseTheLegalGateReported(t *testing.T) {
	one := []corpusLegalEntity{{Name: "Acme GmbH", SourceURL: seedURL + "/impressum"}}
	two := []corpusLegalEntity{
		{Name: "Acme GmbH", SourceURL: seedURL + "/impressum"},
		{Name: "Acme Pte. Ltd.", SourceURL: seedURL + "/impressum"},
	}

	incomplete := readWarnings(legalAbstentionOf(one, true).warning(), nil)
	if len(incomplete) != 1 || incomplete[0] != legalWarningCensusIncomplete {
		t.Fatalf("a failed legal page must be reported as one, not as a multi-entity domain: %v", incomplete)
	}

	multiple := readWarnings(legalAbstentionOf(two, false).warning(), nil)
	if len(multiple) != 1 || multiple[0] != legalWarningMultipleEntities {
		t.Fatalf("a domain that publishes two entities must still say so: %v", multiple)
	}

	if settled := readWarnings(legalAbstentionOf(one, false).warning(), nil); len(settled) != 0 {
		t.Fatalf("a settled census has nothing to caveat: %v", settled)
	}
}

// A partial extraction and a legal abstention are separate caveats: a read
// that hit both owes the human both sentences.
func TestReadWarningsKeepTheExtractionCaveatAlongsideTheLegalOne(t *testing.T) {
	two := []corpusLegalEntity{
		{Name: "Acme GmbH", SourceURL: seedURL + "/impressum"},
		{Name: "Acme Pte. Ltd.", SourceURL: seedURL + "/impressum"},
	}
	got := readWarnings(legalAbstentionOf(two, true).warning(), errors.New("lane died"))
	if len(got) != 2 || got[0] != legalWarningMultipleEntities {
		t.Fatalf("both caveats must reach the dossier, legal first: %v", got)
	}
}
