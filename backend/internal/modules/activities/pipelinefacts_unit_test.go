// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package activities

import (
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/pipelinetrace"
)

// Why a message was not classified, and why saying the WRONG why is worse than
// saying none: a member who reads "the classifier reads email only" about an
// archived email goes looking for a transport problem that does not exist.

func TestTheClassifyReasonNamesTheExclusionThatApplied(t *testing.T) {
	const connector = "connector:gmail"
	for _, tc := range []struct {
		name            string
		label, kind     string
		capturedBy      string
		archived        bool
		senderUndecided bool
		want            pipelinetrace.Reason
	}{
		{
			"a labelled message ran and reports no exclusion",
			"meeting", "email", connector, false, false, "",
		},
		{
			"a chat message is not the classifier's transport",
			"", "message", connector, false, false, pipelinetrace.ReasonTransportNotRead,
		},
		{
			"a hand-logged activity was never connector-captured",
			"", "email", "human:someone", false, false, pipelinetrace.ReasonNotConnectorCaptured,
		},
		{
			"an archived email is out of the backlog",
			"", "email", connector, true, false, pipelinetrace.ReasonArchived,
		},
		{
			"an undecided sender holds the message back (ADR-0072 §5)",
			"", "email", connector, false, true, pipelinetrace.ReasonSenderUndecided,
		},
		{
			"an eligible message is simply waiting for the batch",
			"", "email", connector, false, false, pipelinetrace.ReasonAwaitingBatch,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := classifyReason(classifySubject{
				label: tc.label, kind: tc.kind, capturedBy: tc.capturedBy,
				archived: tc.archived, senderUndecided: tc.senderUndecided,
			})
			if got != tc.want {
				t.Errorf("classifyReason() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestTheEarliestExclusionWins(t *testing.T) {
	// A chat message that is ALSO archived has two true exclusions. The reason
	// reported is the one the backlog predicate acts on first, so a member is
	// told what actually kept their message out rather than whichever condition
	// happened to be evaluated last.
	got := classifyReason(classifySubject{
		kind: "message", capturedBy: "connector:telegram", archived: true, senderUndecided: true,
	})
	if got != pipelinetrace.ReasonTransportNotRead {
		t.Errorf("classifyReason() = %q, want the transport exclusion to win", got)
	}
}

func TestEveryClassifyReasonIsOneTheRegistryClosed(t *testing.T) {
	// The reason is interpolated into a catalog key at the client. A value the
	// registry has not closed would render as a raw identifier at a member —
	// which is how a missing key stayed invisible on this surface once already.
	reg, ok := pipelinetrace.Lookup(pipelinetrace.StageAttentionLabel)
	if !ok {
		t.Fatal("the attention-label stage is not registered")
	}
	closed := map[pipelinetrace.Reason]bool{}
	for _, r := range reg.Reasons {
		closed[r] = true
	}
	for _, produced := range []pipelinetrace.Reason{
		classifyReason(classifySubject{kind: "message", capturedBy: "connector:gmail"}),
		classifyReason(classifySubject{kind: "email", capturedBy: "human:x"}),
		classifyReason(classifySubject{kind: "email", capturedBy: "connector:gmail", archived: true}),
		classifyReason(classifySubject{kind: "email", capturedBy: "connector:gmail", senderUndecided: true}),
		classifyReason(classifySubject{kind: "email", capturedBy: "connector:gmail"}),
	} {
		if !closed[produced] {
			t.Errorf("classifyReason produced %q, which the attention-label stage "+
				"does not declare — it would render as a raw key at a member", produced)
		}
	}
}

func TestTheBacklogPredicateIsTheOneTheReaderAsks(t *testing.T) {
	// Not a substitute for the database-backed agreement test, which proves the
	// two queries SELECT the same rows. This proves the cheaper half no test
	// would otherwise cover: that the reader did not quietly grow its own copy
	// of the predicate's text.
	for _, clause := range []string{
		"capture_label IS NULL", "captured_by LIKE 'connector:%'", "kind = 'email'",
		"archived_at IS NULL", "capture_pending_counterparty",
	} {
		if !strings.Contains(ClassifyBacklogPredicate, clause) {
			t.Errorf("the shared predicate no longer contains %q — if the backlog's "+
				"rule changed, the trace's explanation of it must change with it", clause)
		}
	}
}
