// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package orgdossier

// What survives the trip onto the wire.
//
// The dossier's whole promise is that every sentence it renders can be opened.
// The grounding filter enforces that against the records the assembly was
// written from; this is the second half, where a citation becomes a chip. A
// sentence that arrives here with a citation naming no record must not be
// rendered with the citation quietly removed — the prose would survive and the
// reader would have no way to tell it lost its backing.

import (
	"testing"

	"github.com/gradionhq/margince/backend/internal/compose/claims"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

func citingFields(text string, ids ...string) claims.Sentence {
	cited := make([]claims.Evidence, 0, len(ids))
	for _, id := range ids {
		cited = append(cited, claims.Evidence{EntityType: citeProfileField, EntityID: id})
	}
	return claims.Sentence{Text: text, Nature: natureFact, Evidence: cited}
}

func TestASentenceWhoseCitationNamesNoRecordIsDroppedWhole(t *testing.T) {
	good := ids.NewV7().String()
	out := wireSentences([]claims.Sentence{
		citingFields("They sell load-shifting software.", good),
		citingFields("They run on SAP.", good, "not-a-record-id"),
	})

	if len(out) != 1 {
		t.Fatalf("kept %d sentences, want only the one whose every citation resolves", len(out))
	}
	if out[0].Text != "They sell load-shifting software." {
		t.Errorf("kept %q — the surviving sentence is the fully cited one", out[0].Text)
	}
}

// The rule stated the other way round, because dropping the chip and keeping
// the prose is the failure this exists to stop: it renders an unbacked claim.
func TestNoRenderedSentenceIsLeftWithoutEvidence(t *testing.T) {
	out := wireSentences([]claims.Sentence{citingFields("They run on SAP.", "not-a-record-id")})

	for _, sentence := range out {
		if len(sentence.Evidence) == 0 {
			t.Errorf("sentence %q rendered with no evidence", sentence.Text)
		}
	}
	if len(out) != 0 {
		t.Errorf("kept %d sentences, want none — the only citation names no record", len(out))
	}
}
