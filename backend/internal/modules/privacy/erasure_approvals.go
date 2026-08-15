// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package privacy

// The messages nobody has decided yet, and the runs that produced them.
//
// A staged approval holds a frozen proposal — for a held draft that is
// an addressee and the whole body of a message written to somebody — and a
// workflow run holds what its automation planned and produced. Neither is
// reachable by the rest of this package: every other outbound scrub keys off
// the activity a message became, and a message waiting in an inbox has none.
//
// It is the same gap scheduledsends.go closes for the timer, one step earlier
// in the message's life, and it fails the same way. Left alone, a subject who
// exercises Art. 17 tonight has their draft released by a colleague at nine
// tomorrow, from a system that has just certified their data destroyed. The
// rows nobody ever decides are worse: an approval that expires and a run that
// blocks both keep their payloads forever, with nothing that would look at them
// again.
//
// Kept in its own file for the reason scheduledsends.go is: it belongs to
// neither destructive engine, and both reach it.

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// subjectApprovalMatch is the WHERE clause naming every staging that holds the
// subject, shared by the scrubs below and by the Art. 15 export so they cannot
// disagree about which rows are the subject's. A row that erasure destroys and
// the export never lists is a subject given two different answers about what is
// held about them.
//
// Three ways a proposal names somebody, because there are three ways one is
// written. It may be ABOUT them — the staging's target is their person record,
// or the LEAD row that is the same human before promotion — or it may merely
// CONTAIN them, which is how a held draft carries an addressee and a body: the
// payload is per-kind JSON this package cannot parse, so it is matched as text.
//
// The lead arm is not decoration. A staging targeting the subject's lead twin
// carries their name and phone and frequently no email string at all, so the
// text arm never sees it and the person arm names the wrong row — the sibling
// copy of the case under review, which is the recurring miss this codebase has
// a rule about.
//
// $1 person, $2 lead ids, $3 ANCHORED address patterns from addressPatterns.
const subjectApprovalMatch = `
	   (target_entity_type = 'person' AND target_entity_id = $1)
	OR (target_entity_type = 'lead'   AND target_entity_id = ANY($2::uuid[]))
	OR proposed_change::text ~* ANY($3::text[])
	OR summary               ~* ANY($3::text[])`

// addressPatterns turns the subject's addresses into regexes that match each
// address AND NOTHING ELSE.
//
// Escaping the address is not enough, and this is the trap: a neutralised
// address dropped into '%addr%' still matches as a SUBSTRING. `m@acme.com` is a
// valid address and a suffix of `tim@acme.com`, so erasing the first would blank
// and withdraw every staged message written to the second — a third party's
// live work destroyed by a request that was never about them — and would hand
// that third party's whole message body to the wrong subject in an Art. 15
// export. Neither is recoverable the way an over-broad deletion of captured
// provider payloads is.
//
// So the pattern is ANCHORED on both sides against the characters an address
// may contain: a match must not be preceded by something that could extend the
// local part, nor followed by something that could extend the domain. The
// address itself is quoted, which subsumes the LIKE-metacharacter problem —
// `_` and `%` are ordinary characters to a regex, and QuoteMeta neutralises
// every regex metacharacter an address could carry.
//
// Built in Go rather than assembled in SQL so there is ONE spelling of it: the
// export reaches the same rows through the same patterns, and an escaping rule
// written twice is one that gets hardened once.
func addressPatterns(emails []string) []string {
	patterns := make([]string, 0, len(emails))
	for _, email := range emails {
		if email == "" {
			continue
		}
		patterns = append(patterns,
			`(^|[^a-z0-9._%+-])`+regexp.QuoteMeta(strings.ToLower(email))+`($|[^a-z0-9.-])`)
	}
	return patterns
}

// redactStagedApprovals empties every staged proposal that names the subject,
// withdraws the ones a human could still act on, and ends the automation runs
// waiting behind those.
//
// Pending rows are EXPIRED rather than merely emptied, and that is the half
// that matters. A blanked proposal still in the inbox is a card a colleague can
// approve, and approving it would run its effect against an empty payload — a
// send to nobody, or worse an effect whose gates cannot refuse it because there
// is no longer anyone named to refuse on behalf of. Expiring is what makes the
// card inert, and it is the same terminal state a window closing produces.
//
// The runs behind them are blocked IN THE SAME STATEMENT, and that is not
// tidiness. Everywhere else a withdrawal reaches its parked run by riding the
// approval.decided event; this path emits none, and the expiry sweep cannot
// repair it either because that sweep scans for `pending` and these rows are
// already terminal. Without this the erasure would leave a run parked in
// requires_approval for good — created, this time, by the destruction that was
// supposed to leave nothing behind.
//
// Decided rows are emptied and keep their verdict. What a human approved or
// rejected is a fact about that human, not about the subject, and rewriting it
// would falsify the record of a decision that really happened.
func redactStagedApprovals(ctx context.Context, tx pgx.Tx, subject ids.PersonID, leads []ids.UUID, emails []string) error {
	// The address match needs at least one pattern to look for; a subject with
	// no address is still matched by the target arms above.
	addresses := addressPatterns(emails)
	if addresses == nil {
		addresses = []string{}
	}
	leadIDs := leads
	if leadIDs == nil {
		leadIDs = []ids.UUID{}
	}

	// Pending first, and separately from the decided rows: only these carry a
	// run that has to be ended, and separating them is what lets the statement
	// RETURN exactly the ids that were still live.
	rows, err := tx.Query(ctx, `
		WITH withdrawn AS (
			UPDATE approval
			   SET proposed_change = '{}'::jsonb,
			       summary = '',
			       evidence = NULL,
			       status = 'expired',
			       decision_reason = 'withdrawn: the person it names exercised erasure',
			       decided_at = now()
			 WHERE status = 'pending' AND (`+subjectApprovalMatch+`)
			RETURNING id
		)
		UPDATE workflow_run
		   SET status = 'blocked',
		       detail = jsonb_build_object('reason',
		           'the approval it waited on was withdrawn: the person it names exercised erasure')
		 WHERE status = 'requires_approval'
		   AND detail->>'approval_id' IN (SELECT id::text FROM withdrawn)`,
		subject.UUID, leadIDs, addresses)
	if err != nil {
		return fmt.Errorf("withdrawing the staged approvals naming the subject: %w", err)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("withdrawing the staged approvals naming the subject: %w", err)
	}

	// The agent runs waiting behind those same approvals, for exactly the reason
	// the workflow runs above are ended here. A Surface-B run parks in
	// awaiting_approval and is only ever resumed by an approval.decided event
	// (compose/runnerservice.go); this withdrawal emits none, and the run's own
	// stuck-run sweep only looks at 'running'. So the run would wait forever,
	// holding a second copy of the payload — pending carries the staged call's
	// arguments, which for a send IS the recipient and the body this scrub
	// exists to destroy. Same defect as the workflow run, one table over, and
	// the sibling copy is the miss this codebase has a rule about.
	if _, err := tx.Exec(ctx, `
		UPDATE agent_run
		   SET status = 'failed',
		       pending = NULL,
		       trace = '[]'::jsonb,
		       degrade_reason = 'the approval it waited on was withdrawn: the person it names exercised erasure'
		 WHERE status = 'awaiting_approval'
		   AND approval_id IN (
		         SELECT id FROM approval
		          WHERE status = 'expired'
		            AND decision_reason = 'withdrawn: the person it names exercised erasure')`); err != nil {
		return fmt.Errorf("ending the agent runs waiting on the withdrawn approvals: %w", err)
	}

	// Then the ones somebody already decided: payload gone, verdict intact.
	if _, err := tx.Exec(ctx, `
		UPDATE approval
		   SET proposed_change = '{}'::jsonb,
		       summary = '',
		       evidence = NULL
		 WHERE status <> 'pending' AND (`+subjectApprovalMatch+`)`,
		subject.UUID, leadIDs, addresses); err != nil {
		return fmt.Errorf("emptying the decided approvals naming the subject: %w", err)
	}
	return nil
}

// redactWorkflowRuns empties what an automation planned and produced for the
// subject.
//
// A run's `planned` and `applied` columns hold whatever its actions carried, and
// for a draft_email firing that is the composed message: the greeting by name,
// the body, the intent. The run is not a record anybody addresses — it is
// history — so nothing is withdrawn here and no status moves. The columns are
// emptied and the run keeps saying what it did.
//
// Emptied to an ARRAY, not an object: every real writer marshals a list of
// actions into these columns, so a `{}` would be the one shape in the table no
// reader was written against — and today's readers swallow the decode error, so
// the disagreement would surface first in production, on erased rows only.
//
// Matched on text for the same reason the approvals scrub is: the columns are
// per-handler JSON with no shape this package may assume, and a subject named
// inside one is data held about them however the handler chose to write it. The
// addresses are anchored for the same reason too.
func redactWorkflowRuns(ctx context.Context, tx pgx.Tx, emails []string) error {
	patterns := addressPatterns(emails)
	if len(patterns) == 0 {
		// Nothing to match on, and nothing else to match BY: unlike the approval
		// scrub this table carries no target column, so a run that names the
		// subject only by person id or by name alone is out of reach here. That
		// is a real bound, stated rather than hidden behind an early return that
		// reads as "nothing to do" — a subject with no recorded address gets no
		// run scrub, and the gate below says so when it stops being true.
		return nil
	}
	if _, err := tx.Exec(ctx, `
		UPDATE workflow_run
		   SET planned = '[]'::jsonb,
		       applied = CASE WHEN applied IS NULL THEN NULL ELSE '[]'::jsonb END
		 WHERE planned::text ~* ANY($1::text[])
		    OR coalesce(applied::text, '') ~* ANY($1::text[])`,
		patterns); err != nil {
		return fmt.Errorf("redacting the automation runs naming the subject: %w", err)
	}
	return nil
}
