// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package compose

// Erasure and subject-access over the QUOTATIONS a staged proposal carries.
//
// Evidence (core 0244) is the material a claim was read out of, quoted verbatim
// so the human confirming it checks the text instead of trusting the model. For
// a transcript proposal that quote is up to 500 characters of the meeting's own
// lines — which makes a staged approval a second copy of a body every other
// scrub reaches only through the activity.
//
// Nothing else in this package can find it. A proposal read from a meeting is
// filed against the ACTIVITY, never the person, so the target arms of
// subjectApprovalMatch cannot fire; and people are quoted in meetings by NAME,
// so the address patterns usually cannot either.
//
// The two directions are tested apart, because they are different obligations
// that a single "the words are gone" assertion would conflate. What the cascade
// DESTROYED it must destroy everywhere (the quote goes with the body). What the
// cascade KEPT it must not touch — a meeting shared with somebody else, or one
// under a hold, survives, and a proposal read out of it belongs to whoever is
// still on it.

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/compose/integration"
	"github.com/gradionhq/margince/backend/internal/modules/approvals"
	"github.com/gradionhq/margince/backend/internal/modules/privacy"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// The line the proposal was read out of. It names the subject the way a meeting
// does — by name, in something they said — and carries no address at all, which
// is what puts it beyond every arm of the erasure's subject match.
const quotedTranscriptLine = "Mara Kessler: I will send the revised quote over to you before Friday."

// transcriptSubject seeds a subject, a transcript of a meeting they alone were
// in, the reading of it, and one staged proposal quoting a line of it.
//
// Linked to the subject and to nobody else on purpose: that is what makes the
// transcript the subject's to erase. A meeting shared with a second person is
// another person's record too, and the cascade leaves it alone — which is the
// case TestErasureLeavesTheQuotationOfAMeetingItMayNotDestroy covers.
func transcriptSubject(t *testing.T, e *integration.Env) (ids.PersonID, ids.UUID, ids.ApprovalID) {
	t.Helper()
	person := e.SeedPerson(t, "Mara Kessler", nil)
	e.WsExec(t, `
		INSERT INTO person_email (person_id, email, is_primary, source, captured_by)
		VALUES ( $1, 'mara.kessler@example.com', true, 'test', 'human:seed')`, person)

	activityID := seedTranscript(t, e, "1: Tom: Where did we land on pricing?\n2: "+quotedTranscriptLine)
	e.WsExec(t, `
		INSERT INTO activity_link (activity_id, entity_type, person_id)
		VALUES ( $1, 'person', $2)`, activityID, person)
	e.WsExec(t, `
		INSERT INTO transcript_read (id, activity_id, status, line_count, requested_by, started_at, finished_at)
		VALUES ($1, $2, 'done', 2, 'human:seed', now(), now())`, ids.NewV7(), activityID)

	return ids.From[ids.PersonKind](person), activityID, stageProposalQuoting(t, e, activityID, quotedTranscriptLine)
}

// seedTranscript writes a meeting transcript whose stored body IS the text the
// proposal quotes — a quotation that does not appear in its own source is a
// shape production cannot produce, and it hides the asymmetry these tests turn
// on: destroying a quote while the record it came from still reads the same.
func seedTranscript(t *testing.T, e *integration.Env, body string) ids.UUID {
	t.Helper()
	activityID := ids.NewV7()
	e.WsExec(t, `
		INSERT INTO activity (id, kind, subject, body, source_system, occurred_at, source, captured_by)
		VALUES ($1, 'meeting', 'Quarterly review', $2, 'transcript', now(), 'manual', 'human:seed')`,
		activityID, body)
	return activityID
}

// stageProposalQuoting stages one proposal through the real staging path, so
// the evidence under test is written the way production writes it.
//
// Neither the summary nor the payload carries the quoted words — as in a real
// transcript proposal, where the summary is the next step the model states and
// the quotation is the line it read that from.
func stageProposalQuoting(t *testing.T, e *integration.Env, activityID ids.UUID, snippet string) ids.ApprovalID {
	t.Helper()
	return stageQuotingProposal(t, e, activityID, snippet, "Send the revised quote")
}

func stageQuotingProposal(t *testing.T, e *integration.Env, activityID ids.UUID, snippet, summary string) ids.ApprovalID {
	t.Helper()
	id, err := approvals.NewService(e.DB()).Stage(e.Admin(), approvals.StageInput{
		Kind:           TranscriptProposalKind,
		ProposedChange: json.RawMessage(`{"activity_id":"` + activityID.String() + `","summary":"` + summary + `"}`),
		DiffHash:       "quote-" + ids.NewV7().String(),
		TargetType:     transcriptTargetType,
		TargetID:       activityID,
		Summary:        summary,
		Evidence: []approvals.Evidence{{
			Snippet:     snippet,
			SourceType:  transcriptTargetType,
			SourceID:    activityID,
			SourceLines: []int{2},
		}},
	})
	if err != nil {
		t.Fatalf("Stage → %v", err)
	}
	return id
}

// countQuoting reports how many approval rows still hold the given words in any
// column that can carry them. Asserted on the WORDS rather than on the evidence
// column, because what the subject asked to be destroyed is the sentence.
//
// Every column is coalesced: `summary` is nullable, and concatenating a NULL
// yields NULL, so a row would drop out of this count while its evidence still
// quoted the subject — an absence assertion passing for free.
func countQuoting(t *testing.T, e *integration.Env, words string) int {
	t.Helper()
	return e.WsCount(t, `SELECT count(*) FROM approval
		WHERE coalesce(evidence::text, '') || coalesce(proposed_change::text, '') || coalesce(summary, '')
		      ILIKE '%' || $1 || '%'`, words)
}

// The case the address patterns cannot reach: a meeting quotes the subject by
// name, and the proposal read out of it is filed against the transcript rather
// than against them.
func TestErasureEmptiesTheQuotationAProposalWasReadFrom(t *testing.T) {
	e := integration.Setup(t)
	subject, _, approvalID := transcriptSubject(t, e)

	if n := countQuoting(t, e, quotedTranscriptLine); n != 1 {
		t.Fatalf("the seeded proposal does not quote the transcript (%d rows) — the test proves nothing", n)
	}
	if err := privacy.NewEraser(e.DB()).ErasePerson(e.Admin(), subject.UUID, "subject request"); err != nil {
		t.Fatalf("ErasePerson → %v", err)
	}

	if n := countQuoting(t, e, quotedTranscriptLine); n != 0 {
		t.Error("a staged proposal still quotes the erased transcript verbatim — the timeline row is a tombstone while a card in the inbox reads out what was said in the meeting")
	}
	// The card is inert, proven by asking it to be approved rather than by
	// reading its status column: a blanked proposal a colleague can still
	// confirm would create a task from evidence that no longer exists.
	if _, err := approvals.NewService(e.DB()).Decide(e.Admin(), approvalID, true, nil); err == nil {
		t.Error("a proposal whose evidence was erased was still approvable")
	}
	// Withdrawn under its own reason, so the inbox can say why this card went:
	// the meeting it was read from is gone, not the person who asked to be.
	if n := e.WsCount(t, `SELECT count(*) FROM approval WHERE id = $1 AND decision_reason = $2`,
		approvalID, privacy.ErasedSourceWithdrawal); n != 1 {
		t.Error("the withdrawal does not say the record it was read from was erased")
	}
}

// The reading is a record OF the body: how many lines it addressed, and what it
// produced from them. Its schema means it to go by cascade, and that cascade
// has never fired, because no engine deletes an activity.
func TestErasureDropsTheReadingOfTheTranscriptItErased(t *testing.T) {
	e := integration.Setup(t)
	subject, activityID, _ := transcriptSubject(t, e)

	if err := privacy.NewEraser(e.DB()).ErasePerson(e.Admin(), subject.UUID, "subject request"); err != nil {
		t.Fatalf("ErasePerson → %v", err)
	}

	if n := e.WsCount(t, `SELECT count(*) FROM transcript_read WHERE activity_id = $1`, activityID); n != 0 {
		t.Error("the reading outlived the transcript it read — it still answers how many lines the erased meeting ran to")
	}
}

// The opposite obligation, and the one an over-eager text match breaks.
//
// A meeting shared with a second person is that person's record too, so the
// cascade leaves its body standing — deliberately, under the same rule that
// shields a legal hold and the statutory floor. A proposal read out of it must
// therefore survive as well: destroying it would take a colleague's live work
// on an erasure that was never about their meeting, and it would buy nothing,
// because the subject's address is still readable in the source it was quoted
// from.
func TestErasureLeavesTheQuotationOfAMeetingItMayNotDestroy(t *testing.T) {
	e := integration.Setup(t)
	subject := e.SeedPerson(t, "Mara Kessler", nil)
	const addr = "mara.kessler@example.com"
	e.WsExec(t, `
		INSERT INTO person_email (person_id, email, is_primary, source, captured_by)
		VALUES ( $1, $2, true, 'test', 'human:seed')`, subject, addr)
	colleague := e.SeedPerson(t, "Bob Ferrer", nil)

	quote := "Tom: loop in " + addr + " on the renewal."
	shared := seedTranscript(t, e, "1: "+quote)
	for _, participant := range []any{subject, colleague} {
		e.WsExec(t, `
			INSERT INTO activity_link (activity_id, entity_type, person_id)
			VALUES ( $1, 'person', $2)`, shared, participant)
	}
	approvalID := stageQuotingProposal(t, e, shared, quote, "Loop in the renewal contact")

	if err := privacy.NewEraser(e.DB()).ErasePerson(e.Admin(), ids.From[ids.PersonKind](subject).UUID, "subject request"); err != nil {
		t.Fatalf("ErasePerson → %v", err)
	}

	// The premise: the cascade kept the meeting, because it is not the
	// subject's alone. Without this the rest asserts nothing.
	if n := e.WsCount(t, `SELECT count(*) FROM activity WHERE id = $1 AND body ILIKE '%' || $2 || '%'`,
		shared, addr); n != 1 {
		t.Fatalf("the shared meeting was redacted after all — this test can no longer tell over-deletion from correct deletion")
	}
	if n := countQuoting(t, e, quote); n != 1 {
		t.Error("the erasure destroyed a proposal read out of a meeting it deliberately left standing — another person's pending work, on a request that was never about their record, while the quoted address survives in the meeting anyway")
	}
	if n := e.WsCount(t, `SELECT count(*) FROM approval WHERE id = $1 AND status = 'pending'`, approvalID); n != 1 {
		t.Error("the erasure withdrew a colleague's proposal about a meeting it kept")
	}
}

// The other engine. A transcript ages out on its own schedule (365 days,
// DM-SEED), with no subject and no request behind it — and the sweep visits one
// exactly once, because its selector requires a body and the action removes it.
// A quotation left behind here is never revisited by anything.
func TestRetentionErasingATranscriptEmptiesTheProposalQuotingIt(t *testing.T) {
	e := integration.Setup(t)
	integration.SeedRetentionPolicies(t, e)

	overAge := seedTranscript(t, e, quotedTranscriptLine)
	e.WsExec(t, `UPDATE activity SET occurred_at = now() - interval '400 days' WHERE id = $1`, overAge)
	e.WsExec(t, `
		INSERT INTO transcript_read (id, activity_id, status, line_count, requested_by, started_at, finished_at)
		VALUES ($1, $2, 'done', 1, 'human:seed', now(), now())`, ids.NewV7(), overAge)
	approvalID := stageProposalQuoting(t, e, overAge, quotedTranscriptLine)

	svc := privacy.NewRetentionService(e.DB(), nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err := svc.EvaluateInstallation(integration.RetentionPassCtx(e.WS)); err != nil {
		t.Fatalf("EvaluateInstallation → %v", err)
	}

	if n := e.WsCount(t, `SELECT count(*) FROM activity WHERE id = $1 AND body IS NULL`, overAge); n != 1 {
		t.Fatalf("the sweep did not erase the over-age transcript body — the rest of this test would prove nothing")
	}
	if n := countQuoting(t, e, quotedTranscriptLine); n != 0 {
		t.Error("the proposal still quotes a transcript that has aged out of its retention window — the words outlived the policy that was supposed to end them")
	}
	if n := e.WsCount(t, `SELECT count(*) FROM transcript_read WHERE activity_id = $1`, overAge); n != 0 {
		t.Error("the reading outlived the transcript that aged out")
	}
	// A policy ending the material and a person asking for it to be destroyed
	// are different answers to "why did this card go", and the inbox shows the
	// reason it is given.
	if n := e.WsCount(t, `SELECT count(*) FROM approval WHERE id = $1 AND decision_reason = $2`,
		approvalID, privacy.AgedOutSourceWithdrawal); n != 1 {
		t.Error("the withdrawal does not say the record reached the end of its retention window")
	}
}

// The export half. A quotation is the part of a staging held in the subject's
// own words, and a subject told it was destroyed must have been able to see it
// was held.
func TestSubjectAccessHandsBackTheQuotationHeldAboutThem(t *testing.T) {
	e := integration.Setup(t)
	subject, _, _ := transcriptSubject(t, e)

	pkg, err := privacy.AssembleSAR(e.Admin(), e.DB(), subject)
	if err != nil {
		t.Fatalf("AssembleSAR → %v", err)
	}
	for _, row := range pkg.StagedMessages {
		if strings.Contains(fmt.Sprint(row["evidence"]), quotedTranscriptLine) {
			return
		}
	}
	t.Errorf("the export lists %d staged proposals and none carrying the line it was read from — the installation holds the subject's own sentence and their access request does not mention it",
		len(pkg.StagedMessages))
}

// And the bound on that. The row is FOUND by any arm — including the loose ones
// that match the subject's address in text this installation composed — but a
// quotation is not composed text. It is a raw line lifted out of some record,
// so the ones handed over are reduced to the ones that are the subject's to
// see. Otherwise an address appearing in a summary would disclose a verbatim
// sentence out of a meeting they were never part of, about people they have no
// relationship to.
func TestSubjectAccessWithholdsAQuotationFromARecordTheSubjectHasNoPartIn(t *testing.T) {
	e := integration.Setup(t)
	subject := e.SeedPerson(t, "Mara Kessler", nil)
	const addr = "mara.kessler@example.com"
	e.WsExec(t, `
		INSERT INTO person_email (person_id, email, is_primary, source, captured_by)
		VALUES ( $1, $2, true, 'test', 'human:seed')`, subject, addr)

	const theirsAlone = "Tom: Bob, hold the line at list price - Contoso got thirty percent and nobody is to know."
	elsewhere := seedTranscript(t, e, "1: "+theirsAlone)
	stageQuotingProposal(t, e, elsewhere, theirsAlone, "Send the pricing sheet to "+addr)

	pkg, err := privacy.AssembleSAR(e.Admin(), e.DB(), ids.From[ids.PersonKind](subject))
	if err != nil {
		t.Fatalf("AssembleSAR → %v", err)
	}
	if len(pkg.StagedMessages) != 1 {
		t.Fatalf("the export lists %d staged proposals, want the one naming the subject in its summary — the withholding below would pass for free",
			len(pkg.StagedMessages))
	}
	if quoted := fmt.Sprint(pkg.StagedMessages[0]["evidence"]); strings.Contains(quoted, theirsAlone) {
		t.Error("the export handed the subject a verbatim line out of a meeting they have no part in, about a third party's discount — matched only because their address appears in a summary this installation wrote")
	}
}
