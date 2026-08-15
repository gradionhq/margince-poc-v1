// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package compose

// Erasure and subject-access, over the messages nobody has decided yet.
//
// A staged approval holds a whole composed message — an addressee, a subject
// line, a body — before any scheduled row or activity exists. Every other
// outbound scrub keys off the activity a message became, so until now nothing
// reached it: a subject could exercise Art. 17 tonight and have that draft
// released by a colleague in the morning, from a system that had just certified
// their data destroyed.
//
// The assertion that matters is not that the payload is blank. It is that the
// card cannot be acted on afterwards — a blanked proposal still sitting in an
// inbox is one somebody can approve, and approving it runs its effect against
// an empty payload.

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/compose/integration"
	"github.com/gradionhq/margince/backend/internal/modules/approvals"
	"github.com/gradionhq/margince/backend/internal/modules/privacy"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// erasureSubject seeds a person with an address and one staged approval that
// names them in its payload, the way a held draft does.
func erasureSubject(t *testing.T, e *integration.Env) (ids.PersonID, ids.ApprovalID) {
	t.Helper()
	person := e.SeedPerson(t, "Anna Weber", nil)
	const addr = "anna.erasure@example.com"
	e.WsExec(t, `
		INSERT INTO person_email (workspace_id, person_id, email, is_primary, source, captured_by)
		VALUES ($1, $2, $3, true, 'test', 'human:seed')`, e.WS, person, addr)

	id, err := approvals.NewService(e.DB()).Stage(e.Admin(), approvals.StageInput{
		Kind: "held_draft",
		ProposedChange: json.RawMessage(`{"to":"` + addr + `",` +
			`"subject":"Re: Kickoff","body":"Hi Anna - here is what we agreed.",` +
			`"consent_purpose":"business_correspondence"}`),
		DiffHash: "erasure-" + ids.NewV7().String(),
		Summary:  "an automation drafted a reply to " + addr,
	})
	if err != nil {
		t.Fatal(err)
	}
	return ids.From[ids.PersonKind](person), id
}

func TestErasureEmptiesAStagedDraftAndMakesItUnapprovable(t *testing.T) {
	e := integration.Setup(t)
	subject, approvalID := erasureSubject(t, e)

	if err := privacy.NewEraser(e.DB()).ErasePerson(e.Admin(), subject.UUID, "subject request"); err != nil {
		t.Fatalf("ErasePerson → %v", err)
	}

	// The body and the addressee are gone.
	if n := e.WsCount(t, `SELECT count(*) FROM approval
		WHERE id = $1 AND proposed_change::text ILIKE '%anna.erasure@example.com%'`, approvalID); n != 0 {
		t.Error("the staged draft still carries the erased subject's address")
	}
	if n := e.WsCount(t, `SELECT count(*) FROM approval
		WHERE id = $1 AND proposed_change::text ILIKE '%here is what we agreed%'`, approvalID); n != 0 {
		t.Error("the staged draft still carries the message body written to the erased subject")
	}

	// And the card is inert. This is the half a payload-only scrub would miss:
	// an emptied proposal a colleague can still approve is an effect about to
	// run with nobody named in it.
	if n := e.WsCount(t, `SELECT count(*) FROM approval
		WHERE id = $1 AND status = 'pending'`, approvalID); n != 0 {
		t.Fatal("the emptied draft is still pending — a colleague can approve a message with no recipient and no words")
	}
	if _, err := approvals.NewService(e.DB()).Decide(e.Admin(), approvalID, true, nil); err == nil {
		t.Error("an erased draft was still approvable")
	}
}

// A decision somebody already took is a fact about that human, not about the
// subject. Emptying its payload is right; rewriting its verdict would falsify
// the record of a decision that really happened.
func TestErasureKeepsTheVerdictOnAnAlreadyDecidedApproval(t *testing.T) {
	e := integration.Setup(t)
	subject, approvalID := erasureSubject(t, e)
	e.WsExec(t, `UPDATE approval SET status = 'rejected', decided_at = now() WHERE id = $1`, approvalID)

	if err := privacy.NewEraser(e.DB()).ErasePerson(e.Admin(), subject.UUID, "subject request"); err != nil {
		t.Fatal(err)
	}

	if n := e.WsCount(t, `SELECT count(*) FROM approval
		WHERE id = $1 AND status = 'rejected'`, approvalID); n != 1 {
		t.Error("erasure rewrote a verdict a human had already given")
	}
	if n := e.WsCount(t, `SELECT count(*) FROM approval
		WHERE id = $1 AND proposed_change::text ILIKE '%anna.erasure@example.com%'`, approvalID); n != 0 {
		t.Error("a decided approval kept the erased subject's data")
	}
}

// A run records what its automation planned and produced, which for a drafted
// email is the message itself.
func TestErasureEmptiesTheAutomationRunThatComposedTheDraft(t *testing.T) {
	e := integration.Setup(t)
	subject, _ := erasureSubject(t, e)
	handler := "erasure_probe_" + ids.NewV7().String()[:8]
	e.WsExec(t, `
		INSERT INTO workflow_run (handler, idempotency_key, trigger_event, planned, applied, status)
		VALUES ($1, $2, $3, $4, $5, 'requires_approval')`,
		handler, handler+":1", ids.NewV7(),
		[]byte(`{"actions":[{"Kind":"draft_email"}]}`),
		[]byte(`[{"Kind":"draft_email","Args":{"draft_body":"Hi Anna - anna.erasure@example.com"}}]`))

	if err := privacy.NewEraser(e.DB()).ErasePerson(e.Admin(), subject.UUID, "subject request"); err != nil {
		t.Fatal(err)
	}
	if n := e.WsCount(t, `SELECT count(*) FROM workflow_run
		WHERE handler = $1 AND coalesce(applied::text, '') ILIKE '%anna.erasure@example.com%'`, handler); n != 0 {
		t.Error("the automation run still holds the message it composed for the erased subject")
	}
}

// The export half. A message somebody wrote to the subject and has not yet
// agreed to send is data held about them, and Art. 15 owes them sight of it.
func TestSubjectAccessIncludesAStagedMessageNobodyHasDecided(t *testing.T) {
	e := integration.Setup(t)
	subject, _ := erasureSubject(t, e)

	pkg, err := privacy.AssembleSAR(e.Admin(), e.DB(), subject)
	if err != nil {
		t.Fatalf("AssembleSAR → %v", err)
	}
	// Asserted on the CONTENT rather than the id: what Art. 15 owes the subject
	// is the message somebody wrote to them, and an id match would pass over a
	// section that returned the row with its payload projected away.
	found := false
	for _, row := range pkg.StagedMessages {
		rendered := fmt.Sprint(row["proposed_change"]) + fmt.Sprint(row["summary"])
		if strings.Contains(rendered, "anna.erasure@example.com") &&
			strings.Contains(rendered, "here is what we agreed") {
			found = true
		}
	}
	if !found {
		t.Errorf("the export lists %d staged messages and none carrying the draft written to the subject — a message about to be sent to them is invisible to their own access request",
			len(pkg.StagedMessages))
	}
}

// Withdrawing a staged approval must end the run waiting behind it.
//
// This is the defect the rest of this PR abolishes, reappearing through the
// destructive path. Everywhere else a withdrawal reaches its parked run by
// riding approval.decided; erasure emits no such event, and the expiry sweep
// cannot repair it either because that sweep scans for pending and an erased
// row is already terminal. Left alone the run waits forever — created by the
// destruction that was supposed to leave nothing behind.
func TestErasureBlocksTheRunWaitingOnTheApprovalItWithdraws(t *testing.T) {
	e := integration.Setup(t)
	subject, approvalID := erasureSubject(t, e)
	handler := "wf_erasure_" + ids.NewV7().String()
	e.WsExec(t, `
		INSERT INTO workflow_run (handler, idempotency_key, trigger_event, planned, status, detail)
		VALUES ($1, $2, $3, '[]'::jsonb, 'requires_approval', jsonb_build_object('approval_id', $4::text))`,
		handler, handler+":1", ids.NewV7(), approvalID.String())

	if err := privacy.NewEraser(e.DB()).ErasePerson(e.Admin(), subject.UUID, "subject request"); err != nil {
		t.Fatalf("ErasePerson → %v", err)
	}

	if n := e.WsCount(t, `SELECT count(*) FROM workflow_run
		WHERE handler = $1 AND status = 'requires_approval'`, handler); n != 0 {
		t.Error("the run is still waiting on an approval erasure withdrew — it will wait forever, and nothing downstream will ever look at it again")
	}
	if n := e.WsCount(t, `SELECT count(*) FROM workflow_run
		WHERE handler = $1 AND status = 'blocked'`, handler); n != 1 {
		t.Error("the run did not end as blocked — a firing whose approval was withdrawn is a firing that never happened")
	}
}

// An address is matched as itself, not as a pattern.
//
// `_` is legal and common in a local part, and unescaped in a LIKE it matches
// any single character. Erasing t_m@ would then blank and withdraw the staged
// message written to tim@ — destroying a colleague's pending work on a request
// that was never about them, and putting their message in the wrong person's
// Art. 15 export.
func TestErasureLeavesALookalikeAddressAlone(t *testing.T) {
	e := integration.Setup(t)
	subject := e.SeedPerson(t, "Pattern Subject", nil)
	e.WsExec(t, `
		INSERT INTO person_email (workspace_id, person_id, email, is_primary, source, captured_by)
		VALUES ($1, $2, 't_m@example.com', true, 'test', 'human:seed')`, e.WS, subject)

	bystander, err := approvals.NewService(e.DB()).Stage(e.Admin(), approvals.StageInput{
		Kind:           "held_draft",
		ProposedChange: json.RawMessage(`{"to":"tim@example.com","subject":"Lunch","body":"see you at one"}`),
		DiffHash:       "lookalike-" + ids.NewV7().String(),
		Summary:        "a draft to tim@example.com, who is not the subject",
	})
	if err != nil {
		t.Fatal(err)
	}

	if err := privacy.NewEraser(e.DB()).ErasePerson(e.Admin(),
		ids.From[ids.PersonKind](subject).UUID, "subject request"); err != nil {
		t.Fatal(err)
	}

	if n := e.WsCount(t, `SELECT count(*) FROM approval
		WHERE id = $1 AND status = 'pending'
		  AND proposed_change::text ILIKE '%see you at one%'`, bystander); n != 1 {
		t.Error("erasing t_m@example.com destroyed the staged message written to tim@example.com — the wildcard was never escaped")
	}
}

// An address is matched as a whole address, not as a substring of one.
//
// Escaping the LIKE metacharacters was only half the fix: a neutralised address
// dropped into '%addr%' still matches anywhere inside a longer one.
// `m@example.com` is a valid address and a suffix of `tim@example.com`, so
// erasing the first would destroy every staged message written to the second —
// live work belonging to somebody the request was never about — and hand their
// message body to the wrong subject in an Art. 15 export.
//
// The lookalike test above proves the wildcard case; this proves the substring
// case, and they fail for different reasons.
func TestErasureLeavesAnAddressThatMerelyContainsTheSubjectsAlone(t *testing.T) {
	e := integration.Setup(t)
	subject := e.SeedPerson(t, "Suffix Subject", nil)
	e.WsExec(t, `
		INSERT INTO person_email (workspace_id, person_id, email, is_primary, source, captured_by)
		VALUES ($1, $2, 'm@example.com', true, 'test', 'human:seed')`, e.WS, subject)

	bystander, err := approvals.NewService(e.DB()).Stage(e.Admin(), approvals.StageInput{
		Kind:           "held_draft",
		ProposedChange: json.RawMessage(`{"to":"tim@example.com","subject":"Q3","body":"numbers attached"}`),
		DiffHash:       "suffix-" + ids.NewV7().String(),
		Summary:        "a draft to tim@example.com, who merely contains the subject",
	})
	if err != nil {
		t.Fatal(err)
	}

	if err := privacy.NewEraser(e.DB()).ErasePerson(e.Admin(),
		ids.From[ids.PersonKind](subject).UUID, "subject request"); err != nil {
		t.Fatal(err)
	}

	if n := e.WsCount(t, `SELECT count(*) FROM approval
		WHERE id = $1 AND status = 'pending'
		  AND proposed_change::text ILIKE '%numbers attached%'`, bystander); n != 1 {
		t.Error("erasing m@example.com destroyed the staged message written to tim@example.com — the match was never anchored to an address boundary")
	}
}

// And the subject's own message is still reached, which is the half an
// over-tightened anchor would silently break: a scrub that destroys nothing is
// indistinguishable from one that works, until somebody exercises Art. 17.
func TestErasureStillReachesTheSubjectsOwnStagedMessage(t *testing.T) {
	e := integration.Setup(t)
	subject, approvalID := erasureSubject(t, e)

	if err := privacy.NewEraser(e.DB()).ErasePerson(e.Admin(), subject.UUID, "subject request"); err != nil {
		t.Fatal(err)
	}
	if n := e.WsCount(t, `SELECT count(*) FROM approval
		WHERE id = $1 AND status = 'expired' AND proposed_change = '{}'::jsonb`, approvalID); n != 1 {
		t.Error("the subject's own staged message survived erasure — the anchor is too tight and the scrub reaches nothing")
	}
}

// The bound redactWorkflowRuns admits to, held as a test so it cannot widen
// silently. A run is reached by the addresses it names and by nothing else —
// workflow_run carries no target column — so a subject with no recorded address
// gets no run scrub. If that ever stops being true this fails, which is the
// point: the limitation should be discovered here rather than by a regulator.
func TestAnAutomationRunIsReachedByAddressAndNothingElse(t *testing.T) {
	e := integration.Setup(t)
	subject := e.SeedPerson(t, "No Address At All", nil)
	handler := "wf_noaddr_" + ids.NewV7().String()
	e.WsExec(t, `
		INSERT INTO workflow_run (handler, idempotency_key, trigger_event, planned, status)
		VALUES ($1, $2, $3, $4, 'applied')`,
		handler, handler+":1", ids.NewV7(),
		[]byte(`[{"Kind":"draft_email","Args":{"note":"about No Address At All"}}]`))

	if err := privacy.NewEraser(e.DB()).ErasePerson(e.Admin(),
		ids.From[ids.PersonKind](subject).UUID, "subject request"); err != nil {
		t.Fatal(err)
	}
	if n := e.WsCount(t, `SELECT count(*) FROM workflow_run
		WHERE handler = $1 AND planned::text ILIKE '%No Address At All%'`, handler); n != 1 {
		t.Log("run scrub now reaches a subject named without an address — widen the doc bound on redactWorkflowRuns, it is out of date")
		t.Fail()
	}
}
