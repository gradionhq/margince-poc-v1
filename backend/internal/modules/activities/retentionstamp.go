// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package activities

// Stamping a deal's correspondence as commercial (A165/ADR-0114, A167/ADR-0116).
//
// This module owns `activity`, so the write lives here; deals calls it through
// a seam compose injects, because a module never imports a sibling (ADR-0054).

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// retentionClassCorrespondence is the only class the schema admits today. It
// is spelled once here and once in the migration's CHECK; the CHECK is what
// makes a typo a failed write rather than an unshielded record.
const retentionClassCorrespondence = "commercial_correspondence"

// StampCorrespondenceForDeal marks every activity linked to dealID as
// commercial correspondence and records what qualified it, inside the caller's
// transaction.
//
// Two properties the callers depend on:
//
// IDEMPOTENT. A deal can qualify twice — an offer leaves draft and later the
// deal is won — and the second qualification must not fail the transaction
// that concluded it. The stamp is write-once in the database (the
// activity_refuse_restricted_mutation trigger), so this only stamps rows that
// carry no class yet, and the evidence insert tolerates the row it already
// wrote.
//
// EVIDENCE FIRST. The restriction guard refuses a restriction with no evidence
// behind it, and evidence is what a supervisory authority is shown. Both land
// in this transaction or neither does.
func StampCorrespondenceForDeal(ctx context.Context, tx pgx.Tx, dealID ids.DealID, basis string) error {
	// The deal's name is frozen into the evidence at the moment it qualifies,
	// because a rename or a delete must not take the proof with it.
	var dealName string
	if err := tx.QueryRow(ctx,
		`SELECT name FROM deal WHERE id = $1`, dealID.UUID).Scan(&dealName); err != nil {
		return fmt.Errorf("read qualifying deal: %w", err)
	}

	// One statement for both, so a crash between them is impossible: the CTE
	// stamps the unstamped activities and the insert records why, for every
	// activity linked to this deal — including ones stamped by an earlier
	// qualification, which still need THIS basis on the record.
	if _, err := tx.Exec(ctx, `
		WITH linked AS (
		  SELECT DISTINCT l.activity_id AS id
		    FROM activity_link l
		   WHERE l.deal_id = $1 AND l.entity_type = 'deal'
		), stamped AS (
		  UPDATE activity a
		     SET retention_class = $2, retention_class_at = now()
		   WHERE a.id IN (SELECT id FROM linked)
		     AND a.retention_class IS NULL
		)
		INSERT INTO activity_retention_evidence (activity_id, basis, qualified_at, deal_id, deal_name)
		SELECT id, $3, now(), $1, $4 FROM linked
		ON CONFLICT DO NOTHING`,
		dealID.UUID, retentionClassCorrespondence, basis, dealName); err != nil {
		return fmt.Errorf("stamp deal correspondence: %w", err)
	}
	return nil
}
