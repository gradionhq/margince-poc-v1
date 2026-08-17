// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// Art. 17 reaching the 24-hour capture trace, through the real eraser.
//
// The trace's sweep bounds exposure to a day; it does not ANSWER a request made
// inside that day, and an erasure honoured everywhere except one diagnostic
// table is not honoured. Driven through privacy.Eraser rather than by running
// its DELETE here: a test that pastes the production statement stays green if
// the cascade stops calling it.

import (
	"testing"

	"github.com/gradionhq/margince/backend/internal/modules/privacy"
)

func TestErasureReachesTheCaptureTracePayloads(t *testing.T) {
	e := Setup(t)
	personID := seedSubject(t, e)

	// Two traced messages under the operator's payload posture: one from the
	// subject, one from somebody else. A purge that took both would be as wrong
	// as one that took neither.
	e.WsExec(t, `
		INSERT INTO capture_trace (user_id, connector, source_system, source_id,
		                           stage, outcome, counterparty, subject)
		VALUES (NULL, 'gmail', 'gmail', 'erasure-subject', 'tier_ladder', 'captured', $1, 'Quarterly numbers'),
		       (NULL, 'gmail', 'gmail', 'erasure-control', 'tier_ladder', 'captured', 'someone@else.test', 'Unrelated')`,
		subjectEmail)

	if err := privacy.NewEraser(e.DB()).ErasePerson(e.Admin(), personID, "test"); err != nil {
		t.Fatal(err)
	}

	if n := e.WsCount(t, `SELECT count(*) FROM capture_trace WHERE source_id = 'erasure-subject'`); n != 0 {
		t.Errorf("the erased subject's trace row survived: %d rows remain", n)
	}
	if n := e.WsCount(t, `SELECT count(*) FROM capture_trace WHERE source_id = 'erasure-control'`); n != 1 {
		t.Errorf("another sender's trace row = %d, want 1 kept", n)
	}
}
