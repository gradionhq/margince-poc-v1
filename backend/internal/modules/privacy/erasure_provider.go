// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package privacy

// The provider arm of an erasure: what a licensed data provider was PAID to
// tell us about the subject, and the runs that bought it (ADR-0101).
//
// It is its own file for the reason erasure_rivals.go and
// erasure_channels.go are: each arm answers a different question about what
// "gone" means for a different kind of holding, and the answers do not read
// as one list.

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// purgeProviderPurchases removes the purchased values and detaches the runs.
//
// The two halves are treated differently on purpose. A CLAIM is the value
// itself, so nulling it would leave a row asserting something about a person
// nobody may now assert anything about — it goes. A RUN is what the
// installation spent, so it is scrubbed rather than deleted: once it names
// nobody it is an accounting fact about the installation, not the subject's
// data (PI-AC-8), and that is what keeps a spend history stable across an
// erasure.
//
// Both statements run over every person row that IS this subject, not just
// the one named. An ARCHIVED duplicate legitimately holds the same human's
// address (uq_person_email_dedupe is partial on archived_at IS NULL), and
// with it their purchased email, mobile number and job history — plus a
// provider_job_id that would let the provider be re-asked for exactly the
// answer this erasure destroyed. A live duplicate never reaches here:
// refuseRivalIdentifierHolders refuses the erasure outright.
func purgeProviderPurchases(ctx context.Context, tx pgx.Tx, subjects []ids.UUID) error {
	if _, err := tx.Exec(ctx,
		`DELETE FROM person_provider_claim WHERE person_id = ANY($1)`, subjects); err != nil {
		return fmt.Errorf("deleting the subject's purchased claims: %w", err)
	}
	// The SET clause is storekit's, shared with the per-provider delete-data
	// action so the six identifying columns cannot drift apart again — they
	// did once, and the erasure cleaned two of the six the settings toggle
	// cleaned. The statement stays here so the fitness gates that prove
	// erasure reaches a table can still see which table this erases.
	if _, err := tx.Exec(ctx,
		`UPDATE provider_run SET`+storekit.ScrubProviderRunColumns+` WHERE person_id = ANY($1)`,
		subjects); err != nil {
		return fmt.Errorf("scrubbing the runs that bought the subject's data: %w", err)
	}
	return nil
}

// subjectPersonIDs resolves every person row that IS this subject: the one
// being erased, plus any archived duplicate holding one of their addresses.
// The identifier-scoped statements elsewhere in the cascade (person_email,
// the lead wipe) already work this way; anything keyed on person_id alone
// erases one row of a person who exists as two.
func subjectPersonIDs(ctx context.Context, tx pgx.Tx, personID ids.PersonID, emails []string) ([]ids.UUID, error) {
	rows, err := tx.Query(ctx, `
		SELECT DISTINCT p.id FROM person p
		 WHERE p.id = $1
		    OR EXISTS (SELECT 1 FROM person_email pe
		                WHERE pe.person_id = p.id AND lower(pe.email) = ANY($2))`,
		personID, lowercased(emails))
	if err != nil {
		return nil, fmt.Errorf("resolving the subject's person rows: %w", err)
	}
	return pgx.CollectRows(rows, pgx.RowTo[ids.UUID])
}
