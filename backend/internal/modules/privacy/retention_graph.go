// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package privacy

// The time-based sweep's reach into the relationship graph (ADR-0078). It runs
// inside the retention engine's per-record transaction and is reached only from
// the person/anonymize action — a separate file, not a separate obligation.

import (
	"context"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// scrubPersonGraphTraces removes the anonymized subject from the relationship
// graph. Those structures hold the subject as surely as the person columns do,
// and the time-based sweep reaches them for the same reason the request-driven
// eraser does: an anonymized person who is still named on a participant row,
// still counted in an interaction edge, or still listed in an imported address
// book is not anonymized. This sweep is the path nobody asks for, which is
// exactly why it must not be the thinner one.
//
// subjectEmails and subjectName are the caller's, read before the
// anonymization overwrote them.
func scrubPersonGraphTraces(ctx context.Context, tx pgx.Tx, id ids.UUID, subjectEmails []string, subjectName string) error {
	// Delete then null, in that order and for the reason the
	// eraser documents: a participant row must name somebody, so a
	// row whose only identity is the subject cannot be blanked,
	// while one that also names a colleague is not the subject's
	// to remove.
	_, err := tx.Exec(ctx, `
		DELETE FROM activity_participant
		 WHERE user_id IS NULL
		   AND (person_id = $1 OR (address IS NOT NULL AND address = ANY($2)))`,
		id, subjectEmails)
	if err == nil {
		_, err = tx.Exec(ctx, `
			UPDATE activity_participant SET person_id = NULL, address = NULL
			 WHERE user_id IS NOT NULL
			   AND (person_id = $1 OR (address IS NOT NULL AND address = ANY($2)))`,
			id, subjectEmails)
	}
	if err == nil {
		_, err = tx.Exec(ctx,
			`DELETE FROM graph_interaction_edge WHERE person_id = $1`, id)
	}
	if err == nil {
		// The same reach the request-driven eraser uses, including the
		// name-and-employer arm. Most exported rows carry no address,
		// so a person-and-email-only sweep leaves the common case
		// behind — and this is the path nobody asks for, which is
		// exactly why it must not be the thinner one.
		_, err = tx.Exec(ctx, `
			DELETE FROM linkedin_connection g
			 WHERE g.matched_person_id = $1
			    OR (g.email IS NOT NULL AND g.email = ANY($2))
			    OR (g.normalized_company IS NOT NULL
			        AND g.normalized_name = lower(f_unaccent($3))
			        AND EXISTS (
			            SELECT 1 FROM relationship r
			              JOIN organization o ON o.id = r.organization_id
			             WHERE r.person_id = $1 AND r.kind = 'employment'
			               AND r.archived_at IS NULL
			               AND (r.organization_id = g.matched_org_id
			                    OR lower(f_unaccent(o.display_name)) = g.normalized_company)))`,
			id, subjectEmails, subjectName)
	}
	return err
}
