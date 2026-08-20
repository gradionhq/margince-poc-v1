// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package activities

import (
	"testing"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
)

// The three conditions that decide whether a landed activity is read.
//
// The extraction lane was fully built and had never run once — transcript_read
// held zero rows — because the ONLY thing that enqueued it was somebody asking
// over REST, and nothing asked. A capability nobody can reach is not one.
func TestOnlyATranscriptWithABodyStartsAReading(t *testing.T) {
	body, empty := "00:00 Matthias: we agreed on a small first package", ""
	transcript, plaud := transcriptSourceSystem, "plaud"
	for name, tc := range map[string]struct {
		activity crmcontracts.Activity
		created  bool
		want     bool
	}{
		"a transcript with a body is read": {
			crmcontracts.Activity{SourceSystem: &transcript, Body: &body}, true, true,
		},
		// `plaud` is the honest name of where a recording came from, and it is
		// what a real session logged. It is not the marker, which is why the
		// record-fields notes now say which value is.
		"another source system is stored, not read": {
			crmcontracts.Activity{SourceSystem: &plaud, Body: &body}, true, false,
		},
		"a transcript with no body has nothing to read": {
			crmcontracts.Activity{SourceSystem: &transcript, Body: &empty}, true, false,
		},
		"no source system at all": {
			crmcontracts.Activity{Body: &body}, true, false,
		},
		// An idempotent replay must not queue a second reading of a transcript
		// already read. uq_transcript_read_inflight would refuse it anyway;
		// refusing here means no conflict error on a call that did nothing
		// wrong.
		"a replay of an existing activity is not re-read": {
			crmcontracts.Activity{SourceSystem: &transcript, Body: &body}, false, false,
		},
	} {
		t.Run(name, func(t *testing.T) {
			if got := startsAReading(tc.activity, tc.created); got != tc.want {
				t.Errorf("startsAReading = %v, want %v", got, tc.want)
			}
		})
	}
}
