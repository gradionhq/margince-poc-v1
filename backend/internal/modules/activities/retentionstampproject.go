// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package activities

// Stamping a project's correspondence as commercial (D5).
//
// The deal writer beside this one (retentionstamp.go) stamps a whole deal's
// correspondence at the instant the deal qualifies — one event, many
// activities. A project never has such an instant: it is a commercial
// engagement that qualifies from the moment it exists and keeps accumulating
// correspondence for years. So the LINK is the event, and this stamps one
// activity at the moment it is filed under a project.
//
// That difference is why the two are separate functions rather than one with a
// flag: they are triggered by different things and read different rows. What
// they share is the shape of the write — class onto the activity, evidence
// beside it, one statement, inside the caller's transaction — and that shape is
// the part that must not diverge, which is why this file sits next to its
// sibling rather than in the file that files links.

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// BasisProjectLinked is the derived basis for correspondence that qualifies by
// being filed under a project. It is spelled once here and once in the
// migration's CHECK; the CHECK is what makes a typo a failed write rather than
// an unshielded record.
const BasisProjectLinked = "project_linked"

// StampCorrespondenceForProject marks one activity as commercial
// correspondence because it has just been filed under a project, and records
// what qualified it, inside the caller's transaction.
//
// It runs INSIDE the transaction that writes the link, not on the event bus,
// for the reason the deal seam already states: the gap between qualifying and
// stamping is a window in which an erasure sees unclassified correspondence and
// destroys it, and there is no recovering from that.
//
// IDEMPOTENT. An activity may be filed, unfiled and filed again under the same
// project, and it may already carry a class from a deal that also qualified it.
// The class is write-once in the database (activity_refuse_restricted_mutation),
// so this only stamps a row carrying no class yet, and the evidence insert
// tolerates the row it already wrote.
//
// PERMANENT. Relinking the activity away from the project does not unstamp it,
// and neither does archiving or closing the project. The class is monotonic and
// the evidence is frozen: over-retention is an argument to have with a
// supervisory authority, destruction is irreversible.
func StampCorrespondenceForProject(ctx context.Context, tx pgx.Tx, activityID ids.ActivityID, projectID ids.UUID) error {
	// The project's name is frozen into the evidence at the moment it
	// qualifies, because a rename or a delete must not take the proof with it.
	var projectName string
	if err := tx.QueryRow(ctx,
		`SELECT name FROM project WHERE id = $1`, projectID).Scan(&projectName); err != nil {
		return fmt.Errorf("read qualifying project: %w", err)
	}

	// One statement for both, so a crash between them is impossible: the CTE
	// stamps the activity if nothing has classified it yet, and the insert
	// records why — including for an activity a deal already stamped, which
	// still needs THIS basis on the record.
	if _, err := tx.Exec(ctx, `
		WITH stamped AS (
		  UPDATE activity a
		     SET retention_class = $2, retention_class_at = now()
		   WHERE a.id = $1 AND a.retention_class IS NULL
		)
		INSERT INTO activity_retention_evidence (activity_id, basis, qualified_at, project_id, project_name)
		VALUES ($1, $3, now(), $4, $5)
		ON CONFLICT DO NOTHING`,
		activityID, retentionClassCorrespondence, BasisProjectLinked, projectID, projectName); err != nil {
		return fmt.Errorf("stamp project correspondence: %w", err)
	}
	return nil
}
