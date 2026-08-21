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
// What it deliberately does NOT match is EVIDENCE, and that asymmetry is the
// point rather than an omission. Evidence quotes a source record verbatim, so
// the erasure's entitlement to destroy a quotation is exactly its entitlement
// to destroy what was quoted — which redactApprovalsCitingActivities decides by
// CITATION, from the ids the timeline scrub actually redacted, already filtered
// for legal hold and the statutory floor. Matching the quoted TEXT here would
// add precisely the rows those filters excluded: a proposal read out of a
// held, floor-shielded or shared meeting, destroyed on an erasure that leaves
// the meeting itself standing — another subject's live work gone, spoliating
// held evidence, and the address still readable in the source it was read from.
// The export widens onto evidence instead (sarmessages.go), where
// over-inclusion is admin-mediated and recoverable and over-destruction is not.
//
// $1 person, $2 lead ids, $3 ANCHORED address patterns from addressPatterns.
const subjectApprovalMatch = `
	   (target_entity_type = 'person' AND target_entity_id = $1)
	OR (target_entity_type = 'lead'   AND target_entity_id = ANY($2::uuid[]))
	OR proposed_change::text ~* ANY($3::text[])
	OR summary               ~* ANY($3::text[])`

// blankStagedProposal is what emptying a staged proposal IS: the payload, the
// summary a human reads, and the material it was read out of.
//
// One spelling because three statements in this package perform it, and a
// content column added to one of them is a content column the other two leave
// behind — which is exactly how the quotation came to outlive an erasure that
// emptied everything beside it.
const blankStagedProposal = `proposed_change = '{}'::jsonb,
	       summary         = '',
	       evidence        = NULL`

// The reasons this package writes onto a proposal it withdraws. Three, because
// a colleague finding a card gone is owed the difference between a person who
// asked to be forgotten, a source record that was destroyed with them, and a
// policy that ended the material's window.
//
// subjectWithdrawal is written by the scrub below and READ BACK by the
// agent-run statement that ends the runs parked behind those rows, so it has to
// be one spelling rather than two literals that happen to agree: a single
// character of divergence stops the runs being ended, and nothing fails.
const (
	subjectWithdrawal = "withdrawn: the person it names exercised erasure"
	// ErasedSourceWithdrawal names a card withdrawn because the record it
	// quotes was destroyed by the Art. 17 cascade.
	ErasedSourceWithdrawal = "withdrawn: the record it was read from was erased"
	// AgedOutSourceWithdrawal names one withdrawn because that record reached
	// the end of its retention window.
	AgedOutSourceWithdrawal = "withdrawn: the record it was read from reached the end of its retention window"
)

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
			   SET `+blankStagedProposal+`,
			       status = 'expired',
			       decision_reason = '`+subjectWithdrawal+`',
			       decided_at = now()
			 WHERE status = 'pending' AND (`+subjectApprovalMatch+`)
			RETURNING id
		)
		UPDATE workflow_run
		   SET status = 'blocked',
		       detail = jsonb_build_object('reason',
		           'the approval it waited on was `+subjectWithdrawal+`')
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
		       -- A terminal run carries the instant it stopped, in the same
		       -- statement that makes it terminal. Every other writer of a
		       -- terminal status stamps it, and readers bound "what settled
		       -- today" on it (compose/agentactivity), so a row that skipped it
		       -- would be a run nobody can see ended.
		       finished_at = now(),
		       degrade_reason = 'the approval it waited on was `+subjectWithdrawal+`'
		 WHERE status = 'awaiting_approval'
		   AND approval_id IN (
		         SELECT id FROM approval
		          WHERE status = 'expired'
		            AND decision_reason = '`+subjectWithdrawal+`')`); err != nil {
		return fmt.Errorf("ending the agent runs waiting on the withdrawn approvals: %w", err)
	}

	// Then the ones somebody already decided: payload gone, verdict intact.
	if _, err := tx.Exec(ctx, `
		UPDATE approval
		   SET `+blankStagedProposal+`
		 WHERE status <> 'pending' AND (`+subjectApprovalMatch+`)`,
		subject.UUID, leadIDs, addresses); err != nil {
		return fmt.Errorf("emptying the decided approvals naming the subject: %w", err)
	}
	return nil
}

// evidenceArray is the evidence column read as the array it always is when a
// producer wrote it. jsonb_array_elements RAISES on a value that is not an
// array, and the predicates below run inside the destructive engines' single
// transactions: one malformed row would abort a whole cascade, turning a
// request the workspace must honour into an error nobody can clear from
// outside. NULL — every approval staged before core 0244 — reads as no
// evidence, which is what it is.
const evidenceArray = `CASE WHEN jsonb_typeof(evidence) = 'array' THEN evidence ELSE '[]'::jsonb END`

// evidenceCitesActivity matches a staging whose evidence quotes one of the
// activities named in $1, compared as text because that is how the citation is
// stored.
const evidenceCitesActivity = `
	EXISTS (
	  SELECT 1 FROM jsonb_array_elements(` + evidenceArray + `) AS item
	   WHERE item->>'source_type' = 'activity'
	     AND item->>'source_id' = ANY($1::text[]))`

// evidenceCitesSubjectActivity matches a staging whose evidence quotes any
// activity the subject ($1) is linked to.
//
// It exists so the Art. 15 export lists the rows the cascade destroys. Erasure
// reaches a quotation two ways — the address patterns, and the citation of an
// activity it redacted — and the export shares only the first, so without this
// a subject would be told nothing was staged about them and then have it
// destroyed on the strength of the same reading. That is precisely the
// two-different-answers failure subjectApprovalMatch is shared to prevent.
//
// The bound it does not cover, stated rather than implied: the cascade also
// redacts UNLINKED mail matched by the subject's address alone, and a staging
// quoting one of those rows is reachable here only if the quotation carries the
// address itself. There is no link to walk for a row that by definition has
// none.
const evidenceCitesSubjectActivity = `
	EXISTS (
	  SELECT 1 FROM jsonb_array_elements(` + evidenceArray + `) AS item
	  JOIN activity_link cited
	    ON cited.activity_id::text = item->>'source_id'
	   WHERE item->>'source_type' = 'activity' AND cited.person_id = $1)`

// redactApprovalsCitingActivities empties every proposal whose evidence quotes
// one of the activities whose content has just been destroyed, and expires the
// ones a human could still act on.
//
// Evidence is a VERBATIM quotation of the record a claim was read out of — for
// a transcript proposal, up to 500 characters of the meeting's own lines per
// claim (approvals/evidence.go) — and that is the point of it: a human confirms
// a proposal by checking the text rather than by trusting the model. It is
// therefore a second copy of the body the caller just nulled, and nothing else
// in either engine reaches it. A proposal read from a meeting targets the
// ACTIVITY, never the person, so the target arms of subjectApprovalMatch cannot
// fire; and a transcript quotes people by NAME, so the address patterns usually
// cannot either. Left alone, the timeline row is a tombstone while a card in
// the inbox still quotes what was said in the meeting.
//
// Keyed on what the evidence CITES rather than on what it says, because the
// citation is the tie that survives the redaction: once the body is NULL there
// is no text left to match against, and the quote is the only place those words
// still exist.
//
// Pending rows are expired for the reason the subject scrub expires them — a
// blanked card left decidable is one a colleague can still approve, and
// approving it would run its effect against an empty payload.
//
// What it does NOT do, stated because the scrub above makes a point of doing
// it: it ends no parked workflow_run or agent_run. A run parks behind an
// approval it STAGED, and every producer of evidence today is the transcript
// reader (compose/transcriptproposerun.go), which stages its proposals from a
// job and parks nothing behind them — so the set of rows this could orphan is
// empty by construction, not by luck. The day a kind that parks a run also
// fills evidence, the run teardown belongs here too, keyed off the ids expired
// rather than off a reason string. Until then the erasure's ordering carries
// it: this runs AFTER redactStagedApprovals, so a proposal that answers to both
// is withdrawn there, with its run ended, before this statement sees it.
func redactApprovalsCitingActivities(ctx context.Context, tx pgx.Tx, activities []ids.UUID, reason string) error {
	if len(activities) == 0 {
		return nil
	}
	cited := make([]string, 0, len(activities))
	for _, activity := range activities {
		cited = append(cited, activity.String())
	}
	if _, err := tx.Exec(ctx, `
		UPDATE approval
		   SET `+blankStagedProposal+`,
		       status          = CASE WHEN status = 'pending' THEN 'expired' ELSE status END,
		       decision_reason = CASE WHEN status = 'pending' THEN $2 ELSE decision_reason END,
		       decided_at      = CASE WHEN status = 'pending' THEN now() ELSE decided_at END
		 WHERE `+evidenceCitesActivity, cited, reason); err != nil {
		return fmt.Errorf("emptying the proposals quoting the destroyed records: %w", err)
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
