// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package capture

import "testing"

// The evidence span names where the text mentions the project, in runes, key
// before name, subject before body — and nothing when nothing does.
func TestLocateProjectMentionAnswersRuneOffsets(t *testing.T) {
	project := LiveProject{Name: "ERP replacement", Key: "ERP"}
	span := locateProjectMention(project, ActivityFields{Subject: "Über das erp", Body: "ERP replacement kickoff"})
	if span == nil || span.Field != "subject" || span.Start != 9 || span.End != 12 {
		t.Fatalf("span = %+v, want subject[9,12) — offsets count runes, and the key wins over the name", span)
	}
	span = locateProjectMention(project, ActivityFields{Subject: "status", Body: "the erp replacement plan"})
	if span == nil || span.Field != "body" || span.Start != 4 || span.End != 7 {
		t.Fatalf("span = %+v, want body[4,7)", span)
	}
	if span := locateProjectMention(project, ActivityFields{Subject: "lunch", Body: "Thursday?"}); span != nil {
		t.Fatalf("span = %+v for a text naming nothing, want none", span)
	}
}
