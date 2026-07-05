// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package privacy

// Right-to-erasure (Art. 17, ADR-0011/A13). The shape is fixed:
// anonymize the normalized rows in place, purge raw capture and
// embeddings, hash the identifiers onto the suppression list so
// re-capture cannot resurrect the subject, and prove it all with a
// PII-FREE audit tombstone — the tombstone must never re-store what it
// certifies gone. One erasure spans people, capture and retrieval
// tables in ONE transaction on purpose: erasure must reach every store
// that holds the data subject, and atomicity IS the guarantee — a
// per-module cascade could commit half an erasure (the decisions/0011
// single-transaction exception).

import (
	"context"
	"fmt"
	"strings"

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

		if err := anonymizeSubjectRows(ctx, tx, personID, emails); err != nil {
			return err
		}
		activitiesRedacted, err := redactSubjectTimeline(ctx, tx, personID)
		if err != nil {
			return err
		}
		rawPurged, err := purgeDerivedTraces(ctx, tx, personID, emails)
		if err != nil {
			return err
		}

		// The tombstone: action=erase with counts only — proof without
		// PII. The paired event tells consumers the subject is gone.
		auditID, err := storekit.Audit(ctx, tx, "erase", "person", personID, nil, map[string]any{
			"reason": reason, "emails_suppressed": len(emails), "raw_rows_purged": rawPurged,
			"activities_redacted": activitiesRedacted,
		})
		if err != nil {
			return err
		}
		return storekit.Emit(ctx, tx, auditID, "retention.applied", "person", personID, map[string]any{
			"action": "erase", "reason": reason,
		})
	})
}

// anonymizeSubjectRows wipes the subject's PII in place: the person row
// keeps its skeleton (business records other subjects appear in still
// reference it), the email/phone child rows delete outright, the
// SEGREGATED lead twin — the lead they were promoted from, and any lead
// row carrying one of their addresses — anonymizes the same way, and
// the subject's own embeddings drop.
func anonymizeSubjectRows(ctx context.Context, tx pgx.Tx, personID ids.UUID, emails []string) error {
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
	// Anonymize the lead twins and drop their field-level provenance in
	// one pass: the provenance rows describe WHO captured WHICH of the
	// subject's fields from WHERE — subject-linked metadata that must not
	// outlive the fields it annotates. The CTE runs the UPDATE first and
	// feeds the touched lead ids to the DELETE, so the email match still
	// sees the pre-anonymize addresses.
	if _, err := tx.Exec(ctx, `
		WITH wiped AS (
		  UPDATE lead SET full_name = 'Anonymized Lead', email = NULL, title = NULL,
		    company_name = NULL, candidate_org_key = NULL, raw = NULL,
		    archived_at = coalesce(archived_at, now())
		  WHERE promoted_person_id = $1
		     OR id IN (SELECT converted_from_lead_id FROM person WHERE id = $1 AND converted_from_lead_id IS NOT NULL)
		     OR (email IS NOT NULL AND lower(email) = ANY($2))
		  RETURNING id
		)
		DELETE FROM field_provenance
		WHERE object_type = 'lead' AND object_id IN (SELECT id FROM wiped)`,
		personID, lowercased(emails)); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx,
		`DELETE FROM embedding WHERE entity_type = 'person' AND entity_id = $1`, personID); err != nil {
		return err
	}
	_, err := tx.Exec(ctx,
		`DELETE FROM field_provenance WHERE object_type = 'person' AND object_id = $1`, personID)
	return err
}

// subjectOnlyActivities selects timeline rows linked to the erased
// person and to no OTHER person — the emails, call notes and meeting
// bodies whose free text is about the subject alone. Rows shared with
// another person on the thread are excluded on purpose: redacting them
// would erase a different subject's record.
const subjectOnlyActivities = `
	SELECT l.activity_id FROM activity_link l
	WHERE l.person_id = $1
	  AND NOT EXISTS (
	    SELECT 1 FROM activity_link o
	    WHERE o.activity_id = l.activity_id
	      AND o.person_id IS NOT NULL AND o.person_id <> $1)`

// redactSubjectTimeline erases the subject's free text from the activity
// timeline: subject/body of every subject-only activity are wiped (the
// GENERATED search_tsv refreshes from the now-empty text, so the erased
// name is no longer full-text searchable), and every attachment hung off
// the subject or one of those activities is deleted. Mirrors the
// retention engine's activity/erase redaction — the on-demand Art. 17
// path must reach the timeline the nightly evaluator already reaches.
func redactSubjectTimeline(ctx context.Context, tx pgx.Tx, personID ids.UUID) (int64, error) {
	tag, err := tx.Exec(ctx, `
		UPDATE activity SET subject = $2, body = NULL,
		  archived_at = coalesce(archived_at, now())
		WHERE id IN (`+subjectOnlyActivities+`)`, personID, erasedName)
	if err != nil {
		return 0, err
	}
	// Files live in object storage; the DB row is the only reference the
	// system holds, so deleting it detaches the file. No upload path
	// stores objects in the PoC — when one lands, the storage object must
	// be purged here too (the erasure/SAR PII-reach fitness test keeps this
	// row honest).
	if _, err := tx.Exec(ctx, `
		DELETE FROM attachment
		WHERE (entity_type = 'person' AND entity_id = $1)
		   OR (entity_type = 'activity' AND entity_id IN (`+subjectOnlyActivities+`))`,
		personID); err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// purgeDerivedTraces removes what the system DERIVED from the subject
// and arms the suppression list. Raw capture is purged by identifier
// match: any stored provider payload carrying one of the subject's
// addresses goes — crude on purpose, over-deleting evidence is
// recoverable by re-sync, under-deleting PII is a violation. Embeddings
// of activities on the subject's timeline embed text ABOUT them; the
// vector store must not keep what a similarity probe could partially
// reconstruct.
func purgeDerivedTraces(ctx context.Context, tx pgx.Tx, personID ids.UUID, emails []string) (int64, error) {
	var rawPurged int64
	for _, email := range emails {
		tag, err := tx.Exec(ctx,
			`DELETE FROM raw_capture WHERE payload::text ILIKE '%' || $1 || '%' ESCAPE '\'`,
			storekit.EscapeLike(email))
		if err != nil {
			return 0, err
		}
		rawPurged += tag.RowsAffected()
	}
	if _, err := tx.Exec(ctx, `
		DELETE FROM embedding e USING activity_link l
		WHERE e.entity_type = 'activity' AND l.person_id = $1 AND e.entity_id = l.activity_id`,
		personID); err != nil {
		return 0, err
	}
	for _, email := range emails {
		if _, err := tx.Exec(ctx, `
			INSERT INTO erasure_suppression (workspace_id, kind, value_hash)
			VALUES (NULLIF(current_setting('app.workspace_id', true), '')::uuid, 'email', $1)
			ON CONFLICT DO NOTHING`, storekit.SuppressionHash(email)); err != nil {
			return 0, err
		}
	}
	return rawPurged, nil
}

// lowercased normalizes identifiers for SQL ANY matching.
func lowercased(values []string) []string {
	out := make([]string, len(values))
	for i, v := range values {
		out[i] = strings.ToLower(strings.TrimSpace(v))
	}
	return out
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
