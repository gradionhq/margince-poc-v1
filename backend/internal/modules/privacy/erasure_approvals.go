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

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// redactStagedApprovals empties every staged proposal that names the subject,
// and withdraws the ones a human could still act on.
//
// Two ways a proposal names somebody, because there are two ways one is
// written. It may be ABOUT them — the staging's target is their record — or it
// may merely contain them, which is how a held draft carries an addressee and a
// body: the payload is per-kind JSON this package cannot parse, so it is matched
// as text against the subject's own addresses. The second is the loose one, and
// deliberately: a proposal that mentions the subject anywhere is a proposal that
// holds their data, whatever shape its kind gave it.
//
// Pending rows are EXPIRED rather than merely emptied, and that is the half
// that matters. A blanked proposal still in the inbox is a card a colleague can
// approve, and approving it would run its effect against an empty payload — a
// send to nobody, or worse an effect whose gates cannot refuse it because there
// is no longer anyone named to refuse on behalf of. Expiring is what makes the
// card inert, and it is the same terminal state a window closing produces, so
// nothing downstream needs a new outcome to understand.
//
// Decided rows are emptied and keep their verdict. What a human approved or
// rejected is a fact about that human, not about the subject, and rewriting it
// would falsify the record of a decision that really happened.
func redactStagedApprovals(ctx context.Context, tx pgx.Tx, subject ids.PersonID, emails []string) error {
	// The address match needs at least one address to look for; a subject with
	// none is still matched by target below.
	addresses := emails
	if addresses == nil {
		addresses = []string{}
	}
	if _, err := tx.Exec(ctx, `
		UPDATE approval
		   SET proposed_change = '{}'::jsonb,
		       summary = '',
		       evidence = NULL,
		       status = CASE WHEN status = 'pending' THEN 'expired' ELSE status END,
		       decision_reason = CASE WHEN status = 'pending'
		                              THEN 'withdrawn: the person it names exercised erasure'
		                              ELSE decision_reason END,
		       decided_at = CASE WHEN status = 'pending' THEN now() ELSE decided_at END
		 WHERE (target_entity_type = 'person' AND target_entity_id = $1)
		    OR EXISTS (
		         SELECT 1 FROM unnest($2::text[]) AS addr
		          WHERE addr <> '' AND proposed_change::text ILIKE '%' || addr || '%')
		    OR EXISTS (
		         SELECT 1 FROM unnest($2::text[]) AS addr
		          WHERE addr <> '' AND summary ILIKE '%' || addr || '%')`,
		subject.UUID, addresses); err != nil {
		return fmt.Errorf("redacting the staged approvals naming the subject: %w", err)
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
// Matched on text for the same reason the approvals scrub is: the columns are
// per-handler JSON with no shape this package may assume, and a subject named
// inside one is data held about them however the handler chose to write it.
func redactWorkflowRuns(ctx context.Context, tx pgx.Tx, emails []string) error {
	if len(emails) == 0 {
		return nil
	}
	if _, err := tx.Exec(ctx, `
		UPDATE workflow_run
		   SET planned = '{}'::jsonb,
		       applied = CASE WHEN applied IS NULL THEN NULL ELSE '{}'::jsonb END
		 WHERE EXISTS (
		         SELECT 1 FROM unnest($1::text[]) AS addr
		          WHERE addr <> ''
		            AND (planned::text ILIKE '%' || addr || '%'
		              OR coalesce(applied::text, '') ILIKE '%' || addr || '%'))`,
		emails); err != nil {
		return fmt.Errorf("redacting the automation runs naming the subject: %w", err)
	}
	return nil
}
