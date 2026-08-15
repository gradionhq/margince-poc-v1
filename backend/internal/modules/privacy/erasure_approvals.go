// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package privacy

// The messages nobody has decided yet, and the runs that produced them.
//
// A staged approval holds a frozen proposal — for a held draft (#707) that is
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
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// subjectApprovalMatch is the WHERE clause naming every staging that holds the
// subject, shared by the two scrubs below so they cannot disagree about which
// rows are the subject's.
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
// $1 person, $2 lead ids, $3 addresses ALREADY LIKE-ESCAPED by likeEscaped.
const subjectApprovalMatch = `
	   (target_entity_type = 'person' AND target_entity_id = $1)
	OR (target_entity_type = 'lead'   AND target_entity_id = ANY($2::uuid[]))
	OR EXISTS (
	     SELECT 1 FROM unnest($3::text[]) AS addr
	      WHERE addr <> ''
	        AND (proposed_change::text ILIKE '%' || addr || '%' ESCAPE '\'
	          OR summary               ILIKE '%' || addr || '%' ESCAPE '\'))`

// likeEscaped neutralises the LIKE metacharacters in an address so it matches
// itself and nothing else.
//
// `_` is legal and COMMON in an email local part, and unescaped it matches any
// single character — so erasing t_m@corp.com would blank and withdraw the
// stagings addressed to tim@corp.com and tom@corp.com, destroying a colleague's
// pending work on a request that was never about them, and would hand their
// message body to the wrong person in an Art. 15 export. `%` is rarer and
// worse. The backslash goes first, or escaping the others would re-escape it.
func likeEscaped(emails []string) []string {
	out := make([]string, 0, len(emails))
	for _, email := range emails {
		e := strings.ReplaceAll(email, `\`, `\\`)
		e = strings.ReplaceAll(e, `%`, `\%`)
		e = strings.ReplaceAll(e, `_`, `\_`)
		out = append(out, e)
	}
	return out
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
// already terminal. Without this the erasure would leave exactly the
// permanently-parked run #1304 exists to abolish — created, this time, by the
// destruction that was supposed to leave nothing behind.
//
// Decided rows are emptied and keep their verdict. What a human approved or
// rejected is a fact about that human, not about the subject, and rewriting it
// would falsify the record of a decision that really happened.
func redactStagedApprovals(ctx context.Context, tx pgx.Tx, subject ids.PersonID, leads []ids.UUID, emails []string) error {
	// The address match needs at least one address to look for; a subject with
	// none is still matched by the target arms above.
	addresses := likeEscaped(emails)
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
// addresses are LIKE-escaped for the same reason too.
func redactWorkflowRuns(ctx context.Context, tx pgx.Tx, emails []string) error {
	if len(emails) == 0 {
		return nil
	}
	if _, err := tx.Exec(ctx, `
		UPDATE workflow_run
		   SET planned = '[]'::jsonb,
		       applied = CASE WHEN applied IS NULL THEN NULL ELSE '[]'::jsonb END
		 WHERE EXISTS (
		         SELECT 1 FROM unnest($1::text[]) AS addr
		          WHERE addr <> ''
		            AND (planned::text ILIKE '%' || addr || '%' ESCAPE '\'
		              OR coalesce(applied::text, '') ILIKE '%' || addr || '%' ESCAPE '\'))`,
		likeEscaped(emails)); err != nil {
		return fmt.Errorf("redacting the automation runs naming the subject: %w", err)
	}
	return nil
}
