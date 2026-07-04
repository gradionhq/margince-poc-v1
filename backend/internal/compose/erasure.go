package compose

// Right-to-erasure (Art. 17, ADR-0011/A13), composed here because one
// erasure spans people, capture, retrieval and audit. The shape is
// fixed: anonymize the normalized rows in place, purge raw capture and
// embeddings, hash the identifiers onto the suppression list so
// re-capture cannot resurrect the subject, and prove it all with a
// PII-FREE audit tombstone — the tombstone must never re-store what it
// certifies gone.

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// erasedName replaces every naming field: recognizable as a tombstone,
// carrying nothing of the subject.
const erasedName = "Erased Subject"

// Eraser executes the shared erase path both the DSR surface and the
// retention engine's 'erase' action ride.
type Eraser struct {
	pool *pgxpool.Pool
}

func NewEraser(pool *pgxpool.Pool) *Eraser { return &Eraser{pool: pool} }

// ErasePerson removes the subject's PII in ONE transaction: person row
// anonymized, email/phone child rows deleted, raw capture purged,
// embeddings dropped, identifiers hashed onto the suppression list,
// tombstone written. Deleting a person row outright would cascade into
// business records other subjects appear in; anonymize-in-place is the
// A13 posture.
func (e *Eraser) ErasePerson(ctx context.Context, personID ids.UUID, reason string) error {
	if err := auth.Require(ctx, "person", principal.ActionDelete); err != nil {
		return err
	}
	return database.WithWorkspaceTx(ctx, e.pool, func(tx pgx.Tx) error {
		if err := auth.EnsureVisible(ctx, tx, "person", personID); err != nil {
			return err
		}
		var held bool
		if err := tx.QueryRow(ctx,
			`SELECT legal_hold FROM person WHERE id = $1`, personID).Scan(&held); err != nil {
			if err == pgx.ErrNoRows {
				return apperrors.ErrNotFound
			}
			return err
		}
		if held {
			return fmt.Errorf("erasing a person under legal hold: %w", apperrors.ErrConflict)
		}

		// Collect identifiers BEFORE wiping — the suppression list needs
		// their hashes, and afterwards nothing holds them.
		emails, err := collectStrings(ctx, tx,
			`SELECT email FROM person_email WHERE person_id = $1`, personID)
		if err != nil {
			return err
		}

		if _, err := tx.Exec(ctx, `
			UPDATE person SET first_name = NULL, last_name = NULL, full_name = $2,
			  title = NULL, social = '{}'::jsonb, address = NULL, raw = NULL,
			  archived_at = coalesce(archived_at, now())
			WHERE id = $1`, personID, erasedName); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM person_email WHERE person_id = $1`, personID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM person_phone WHERE person_id = $1`, personID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx,
			`DELETE FROM embedding WHERE entity_type = 'person' AND entity_id = $1`, personID); err != nil {
			return err
		}

		// Raw capture is purged by identifier match: any stored provider
		// payload carrying one of the subject's addresses goes. Crude on
		// purpose — over-deleting evidence is recoverable by re-sync,
		// under-deleting PII is a violation.
		var rawPurged int64
		for _, email := range emails {
			tag, err := tx.Exec(ctx,
				`DELETE FROM raw_capture WHERE payload::text ILIKE '%' || $1 || '%' ESCAPE '\'`,
				storekit.EscapeLike(email))
			if err != nil {
				return err
			}
			rawPurged += tag.RowsAffected()
		}
		// Embeddings of activities on the subject's timeline embed text
		// ABOUT them; the vector store must not keep what a similarity
		// probe could partially reconstruct.
		if _, err := tx.Exec(ctx, `
			DELETE FROM embedding e USING activity_link l
			WHERE e.entity_type = 'activity' AND l.person_id = $1 AND e.entity_id = l.activity_id`,
			personID); err != nil {
			return err
		}

		for _, email := range emails {
			if _, err := tx.Exec(ctx, `
				INSERT INTO erasure_suppression (workspace_id, kind, value_hash)
				VALUES (NULLIF(current_setting('app.workspace_id', true), '')::uuid, 'email', $1)
				ON CONFLICT DO NOTHING`, storekit.SuppressionHash(email)); err != nil {
				return err
			}
		}

		// The tombstone: action=erase with counts only — proof without
		// PII. The paired event tells consumers the subject is gone.
		auditID, err := storekit.Audit(ctx, tx, "erase", "person", personID, nil, map[string]any{
			"reason": reason, "emails_suppressed": len(emails), "raw_rows_purged": rawPurged,
		})
		if err != nil {
			return err
		}
		return storekit.Emit(ctx, tx, auditID, "retention.applied", "person", personID, map[string]any{
			"action": "erase", "reason": reason,
		})
	})
}

func collectStrings(ctx context.Context, tx pgx.Tx, query string, args ...any) ([]string, error) {
	rows, err := tx.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}
