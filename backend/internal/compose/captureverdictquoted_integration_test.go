// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package compose

// The verdict path end to end when the bound model reports its confidence as a
// JSON STRING rather than a number.
//
// The decoder's own tests prove a quoted score parses. These prove what that is
// FOR: the ledger row is decided, the sender becomes the record capture
// withheld, and the model is asked once. A quoted confidence used to fail the
// unmarshal, so the row was deferred with backoff — fail-closed and correct, and
// the reason the feature was substantially non-functional against a model that
// quotes. Nothing at the decoder level can show that, because the disposition is
// where the cost was paid.
//
// The control matters as much: a confidence that genuinely cannot be read must
// still take the deferring branch. Tolerating the wrapper must not tolerate the
// value.

import (
	"context"
	"fmt"
	"log/slog"
	"testing"

	"github.com/gradionhq/margince/backend/internal/compose/integration"
	"github.com/gradionhq/margince/backend/internal/modules/capture"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/model"
)

// literalConfidenceBrain answers the verdict call with a hand-built payload so
// the confidence is emitted EXACTLY as written — a Go float64 could never
// produce the quoted form the bound model actually sends.
type literalConfidenceBrain struct {
	verdict    string
	confidence string // written into the JSON verbatim, quotes and all
	calls      int
}

func (b *literalConfidenceBrain) Complete(_ context.Context, req model.Request) (model.Response, error) {
	b.calls++
	askedFor := fencedIDs(req.System, req.Messages[0].Content, "id")
	if len(askedFor) != 1 {
		return model.Response{}, fmt.Errorf("verdict prompt fenced %d senders, want 1", len(askedFor))
	}
	return model.Response{Text: fmt.Sprintf(
		`{"results":[{"id":%q,"verdict":%q,"confidence":%s}]}`,
		askedFor[0], b.verdict, b.confidence)}, nil
}

func TestVerdictDecidesOnAQuotedConfidence(t *testing.T) {
	e := integration.Setup(t)
	activityID := seedCapturedMail(t, e, "ada@quoted.example", "quote request")
	dispositionID := seedPendingDisposition(t, e, "ada@quoted.example", "quoted.example", activityID)

	brain := &literalConfidenceBrain{verdict: capture.PendingStatusReal, confidence: `"0.9"`}
	engine := NewCounterpartyVerdictEngine(e.Pool, brain, slog.Default())
	if err := engine.RunWorkspace(principal.WithWorkspaceID(context.Background(), e.WS), 0); err != nil {
		t.Fatalf("verdict pass: %v", err)
	}

	if got := dispositionStatus(t, e, dispositionID); got != capture.PendingStatusReal {
		t.Fatalf("disposition status = %q, want %q — a quoted 0.9 must be READ and clear the 0.7 floor, not defer the row",
			got, capture.PendingStatusReal)
	}
	if brain.calls != 1 {
		t.Errorf("the model was asked %d times, want 1 — a re-ask means the first answer was not read", brain.calls)
	}
	if n := countIn(t, e, `
		SELECT count(*) FROM person p JOIN person_email pe ON pe.person_id = p.id
		 WHERE pe.email = 'ada@quoted.example'`); n != 1 {
		t.Fatalf("%d persons created, want 1 — the verdict did not reach the records capture withheld", n)
	}
}

// The control: a confidence the decoder genuinely cannot read takes the branch
// a quoted number used to take — the row is left undecided for a later pass.
func TestVerdictDefersAnUnreadableConfidence(t *testing.T) {
	e := integration.Setup(t)
	activityID := seedCapturedMail(t, e, "ada@unreadable.example", "quote request")
	dispositionID := seedPendingDisposition(t, e, "ada@unreadable.example", "unreadable.example", activityID)

	brain := &literalConfidenceBrain{verdict: capture.PendingStatusReal, confidence: `"very high"`}
	engine := NewCounterpartyVerdictEngine(e.Pool, brain, slog.Default())
	if err := engine.RunWorkspace(principal.WithWorkspaceID(context.Background(), e.WS), 0); err != nil {
		t.Fatalf("verdict pass: %v", err)
	}

	if got := dispositionStatus(t, e, dispositionID); got != capture.PendingStatusPending {
		t.Fatalf("disposition status = %q, want %q — an unreadable confidence must defer, not decide",
			got, capture.PendingStatusPending)
	}
	if n := countIn(t, e, `
		SELECT count(*) FROM person p JOIN person_email pe ON pe.person_id = p.id
		 WHERE pe.email = 'ada@unreadable.example'`); n != 0 {
		t.Fatalf("%d persons created from an unreadable reply, want 0", n)
	}
	attempts := countIn(t, e,
		`SELECT attempts FROM capture_pending_counterparty WHERE id = $1`, dispositionID)
	if attempts == 0 {
		t.Error("the deferral did not charge the row an attempt")
	}
}
