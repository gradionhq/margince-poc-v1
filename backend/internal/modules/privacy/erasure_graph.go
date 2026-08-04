// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package privacy

// The subject's traces in the relationship graph (ADR-0078): who they were
// recorded as being in a conversation with, the ghosts of them in a
// colleague's imported network, and the interaction projection folded out of
// both. This is the SAME Art. 17 transaction the rest of the cascade runs in —
// a separate file, not a separate obligation, and reached only from
// anonymizeSubjectRows. erasureCascadeFiles in the PII-coverage gate lists this
// file alongside erasure.go, so moving a scrub here does not take the tables it
// purges out of that gate's sight.
//
// Every clause here reaches the subject by IDENTIFIER as well as by person id,
// because the graph structures exist precisely to hold a party who never
// became a record: that is what the address arm of a participant row and a
// LinkedIn ghost both ARE. A person-keyed sweep alone leaves the subject
// named, reachable and re-matchable.

import (
	"context"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// scrubSubjectFromGraph removes the subject from the relationship graph.
// subjectName and linkedInHandles are the caller's, read before the
// anonymization that overwrote them: the ghost sweep identifies rows by both,
// and by the time it runs neither the person row nor person_social still holds
// them.
func scrubSubjectFromGraph(
	ctx context.Context, tx pgx.Tx, personID ids.PersonID,
	emails []string, subjectName string, linkedInHandles []string,
) error {
	if err := scrubSubjectFromParticipants(ctx, tx, personID, emails); err != nil {
		return err
	}
	if err := deleteSubjectLinkedInGhosts(ctx, tx, personID, emails, subjectName, linkedInHandles); err != nil {
		return err
	}
	return deleteSubjectInteractionEdges(ctx, tx, personID)
}

// scrubSubjectFromParticipants clears the subject off the interaction
// participants (ACT-DDL-3), which name them twice over: by person_id, and by
// the raw ADDRESS a message carried — a row that exists precisely for the
// party who never became a record, so it survives the person_email purge and
// would keep the erased subject's address readable and re-matchable.
func scrubSubjectFromParticipants(ctx context.Context, tx pgx.Tx, personID ids.PersonID, emails []string) error {
	// Delete first, then null. A participant row must name SOMEBODY (the
	// ACT-DDL-3 identity CHECK), so a row whose only identity is the subject
	// cannot be blanked — it has to go. A row that also names one of our
	// users is a different matter: the colleague was in that conversation and
	// that is not the subject's data to erase, so the subject's arms are
	// nulled and the row stands.
	if _, err := tx.Exec(ctx, `
		DELETE FROM activity_participant
		 WHERE user_id IS NULL
		   AND (person_id = $1 OR (address IS NOT NULL AND address = ANY($2)))`,
		personID, emails); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `
		UPDATE activity_participant SET person_id = NULL, address = NULL
		 WHERE user_id IS NOT NULL
		   AND (person_id = $1 OR (address IS NOT NULL AND address = ANY($2)))`,
		personID, emails)
	return err
}

// deleteSubjectLinkedInGhosts drops the subject's LinkedIn ghosts (CG-DDL-2).
// A ghost can BE the subject: it holds their name, employer and — on CSV rows
// — their address, imported from a colleague's export without the subject ever
// being asked. That is exactly the data an Art. 17 request is about, and it is
// invisible to every other clause in the cascade because a ghost is not a
// person row.
//
// It deletes on SUGGESTION-GRADE evidence, not just on a confirmed match, and
// that asymmetry is deliberate. Matching errs toward caution because a wrong
// link attaches a stranger to a customer record. Deletion errs the other way,
// because the two mistakes do not cost the same: deleting one ghost too many
// costs a re-import of a file the colleague still has, while keeping one too
// few leaves a named person's data behind after we certified it destroyed.
//
// So: matched to them, or carrying their address, or bearing their name at an
// employer they actually work for — the same evidence that would have produced
// a suggestion.
func deleteSubjectLinkedInGhosts(
	ctx context.Context, tx pgx.Tx, personID ids.PersonID,
	emails []string, subjectName string, linkedInHandles []string,
) error {
	_, err := tx.Exec(ctx, `
		DELETE FROM linkedin_connection g
		 WHERE g.matched_person_id = $1
		    OR (g.email IS NOT NULL AND g.email = ANY($2))
		    -- The LinkedIn address, passed in rather than joined: person_social
		    -- is cleared earlier in this same transaction, so a join here would
		    -- read an empty table and miss every ghost identified only by URL.
		    OR (g.profile_url IS NOT NULL AND g.profile_url = ANY($4))
		    -- Name + employer, matched on the NAME the ghost carries rather
		    -- than on its derived matched_org_id. That column is set by a
		    -- matcher that runs on upload: a ghost imported before its account
		    -- existed, or one whose matcher pass failed, has it NULL and would
		    -- survive an erasure it plainly belongs in. The employer is
		    -- compared as text, which is the evidence the ghost actually holds.
		    OR (g.normalized_company IS NOT NULL
		        AND lower(f_unaccent($3)) = g.normalized_name
		        AND EXISTS (
		            SELECT 1 FROM relationship r
		              JOIN organization o ON o.id = r.organization_id
		             WHERE r.person_id = $1 AND r.kind = 'employment'
		               AND r.archived_at IS NULL
		               AND (r.organization_id = g.matched_org_id
		                    OR lower(f_unaccent(o.display_name)) = g.normalized_company
		                    OR lower(f_unaccent(o.display_name)) LIKE g.normalized_company || ' %')))`,
		personID, emails, subjectName, linkedInHandles)
	return err
}

// deleteSubjectInteractionEdges drops the subject's rows in the interaction
// projection (CG-DDL-1). It is derived, but derived from data that is now gone
// — and it holds who corresponded with the subject, how often and how
// recently. It is dropped HERE, in the erasure transaction, rather than left
// to the cg:graph-edge consumer: an erasure obligation that depends on an
// event being delivered is an obligation that fails silently when the bus is
// behind or the handler is wrong. It was in fact wrong — the consumer listened
// for a `person.erased` event this path has never emitted, so the edges
// outlived every erasure.
func deleteSubjectInteractionEdges(ctx context.Context, tx pgx.Tx, personID ids.PersonID) error {
	_, err := tx.Exec(ctx,
		`DELETE FROM graph_interaction_edge WHERE person_id = $1`, personID)
	return err
}
