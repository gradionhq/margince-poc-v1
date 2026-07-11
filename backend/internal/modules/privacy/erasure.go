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
	"github.com/gradionhq/margince/backend/internal/platform/blobstore"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// erasedName replaces every naming field: recognizable as a tombstone,
// carrying nothing of the subject.
const erasedName = "Erased Subject"

// actionErase names the Art. 17 scrub in both vocabularies it crosses:
// the retention policy's action column and the audit spine's verb. The
// field-history projection cuts at audit rows carrying it.
const actionErase = "erase"

// Eraser executes the shared erase path both the DSR surface and the
// retention engine's 'erase' action ride.
type Eraser struct {
	pool *pgxpool.Pool
	// blob purges the subject's attachment objects (Art. 17 reaches the
	// bytes, not only the row). nil in a deployment with no object store —
	// where no upload path could have stored an object either.
	blob blobstore.Store
}

func NewEraser(pool *pgxpool.Pool) *Eraser { return &Eraser{pool: pool} }

// WithBlobstore returns an eraser that also purges attachment objects.
// Compose passes the object store so erasure reaches the bytes behind the
// attachment rows it deletes.
func (e *Eraser) WithBlobstore(blob blobstore.Store) *Eraser {
	clone := *e
	clone.blob = blob
	return &clone
}

// ErasePerson removes the subject's PII in ONE transaction: person row
// anonymized, email/phone child rows deleted, raw capture purged,
// embeddings dropped, identifiers hashed onto the suppression list,
// tombstone written. Deleting a person row outright would cascade into
// business records other subjects appear in; anonymize-in-place is the
// A13 posture.
//
// personID stays untyped ids.UUID: this is the consent.Eraser seam
// (compose injects it into the DSR handler) and the retention engine's
// polymorphic due-list — both hand over a bare UUID. The subject is
// widened to a typed person id once here and threaded typed from then on.
func (e *Eraser) ErasePerson(ctx context.Context, personID ids.UUID, reason string) error {
	if err := auth.Require(ctx, "person", principal.ActionDelete); err != nil {
		return err
	}
	subject := ids.From[ids.PersonKind](personID)
	return database.WithWorkspaceTx(ctx, e.pool, func(tx pgx.Tx) error {
		if err := auth.EnsureVisible(ctx, tx, "person", subject.UUID); err != nil {
			return err
		}
		var held bool
		if err := tx.QueryRow(ctx,
			`SELECT legal_hold FROM person WHERE id = $1`, subject).Scan(&held); err != nil {
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
			`SELECT email FROM person_email WHERE person_id = $1`, subject)
		if err != nil {
			return err
		}

		leadsWiped, err := anonymizeSubjectRows(ctx, tx, subject, emails)
		if err != nil {
			return err
		}
		activitiesRedacted, err := redactSubjectTimeline(ctx, tx, subject)
		if err != nil {
			return err
		}
		if err := tombstoneCollateralScrubs(ctx, tx, "lead", leadsWiped, reason); err != nil {
			return err
		}
		if err := tombstoneCollateralScrubs(ctx, tx, "activity", activitiesRedacted, reason); err != nil {
			return err
		}
		// Purge the subject's attachment bytes and rows together, inside the
		// transaction (objects first). A failure here — including a
		// misconfigured store — rolls the whole erasure back, so it stays
		// retryable and never commits a half-erasure.
		if err := e.eraseAttachments(ctx, tx, subjectAttachmentsWhere, subject); err != nil {
			return err
		}
		rawPurged, err := purgeDerivedTraces(ctx, tx, subject, emails)
		if err != nil {
			return err
		}

		// The tombstone: action=erase with counts only — proof without
		// PII. The counts are evidence ABOUT the scrub, so they ride the
		// evidence column; before/after stay empty — they are reserved for
		// field images, and the record-history read serves a tombstone's
		// images verbatim. The paired event tells consumers the subject is
		// gone.
		auditID, err := storekit.AuditWithEvidence(ctx, tx, actionErase, "person", subject.UUID, nil, nil, map[string]any{
			"reason": reason, "emails_suppressed": len(emails), "raw_rows_purged": rawPurged,
			"activities_redacted": len(activitiesRedacted),
		})
		if err != nil {
			return err
		}
		return storekit.Emit(ctx, tx, auditID, "retention.applied", "person", subject.UUID, map[string]any{
			"action": actionErase, "reason": reason,
		})
	})
}

// subjectAttachmentsWhere selects the attachments Art. 17 erasure removes for
// a person: those hung off the person and those on the person's subject-only
// activities. $1 is the person id throughout.
const subjectAttachmentsWhere = `(entity_type = 'person' AND entity_id = $1)
	   OR (entity_type = 'activity' AND entity_id IN (` + subjectOnlyActivities + `))`

// eraseAttachments purges the matched attachments' objects and deletes their
// rows within the caller's transaction, objects FIRST: the keys live in the
// rows, so purging before the DELETE means any failure (a store error, or no
// store configured while objects exist) rolls the transaction back with the
// keys intact — a retry re-purges idempotently, and no bytes are ever
// orphaned with their only key gone. Erasure is rare and not latency-bound,
// so the brief object-store I/O held under the transaction is an acceptable
// trade for that durability guarantee.
func (e *Eraser) eraseAttachments(ctx context.Context, tx pgx.Tx, where string, args ...any) error {
	rows, err := tx.Query(ctx, `SELECT storage_key FROM attachment WHERE `+where, args...)
	if err != nil {
		return err
	}
	var keys []string
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			rows.Close()
			return err
		}
		keys = append(keys, key)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if len(keys) > 0 {
		if e.blob == nil {
			return fmt.Errorf("privacy: %d attachment object(s) to purge but no object store is configured", len(keys))
		}
		for _, key := range keys {
			if err := e.blob.Delete(ctx, key); err != nil {
				return fmt.Errorf("privacy: purging attachment object: %w", err)
			}
		}
	}
	if _, err := tx.Exec(ctx, `DELETE FROM attachment WHERE `+where, args...); err != nil {
		return err
	}
	return nil
}

// anonymizeSubjectRows wipes the subject's PII in place: the person row
// keeps its skeleton (business records other subjects appear in still
// reference it), the email/phone child rows delete outright, the
// SEGREGATED lead twin — the lead they were promoted from, and any lead
// row carrying one of their addresses — anonymizes the same way, and
// the subject's own embeddings drop. Both anonymizing UPDATEs also NULL
// every catalog-defined cf_ column, retired included — a custom column
// holds subject data exactly like a core one (see subjectcolumns.go).
// It returns the wiped lead ids so the caller can tombstone each twin's
// own audit spine.
func anonymizeSubjectRows(ctx context.Context, tx pgx.Tx, personID ids.PersonID, emails []string) ([]ids.UUID, error) {
	personCustom, err := subjectCustomColumns(ctx, tx, "person")
	if err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, fmt.Sprintf(`
		UPDATE person SET first_name = NULL, last_name = NULL, full_name = $2,
		  title = NULL, raw = NULL,
		  address_line1 = NULL, address_line2 = NULL, address_city = NULL,
		  address_region = NULL, address_postal_code = NULL, address_country = NULL,
		  archived_at = coalesce(archived_at, now())%s
		WHERE id = $1`, nullColumnAssignments(personCustom)), personID, erasedName); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM person_social WHERE person_id = $1`, personID); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM person_email WHERE person_id = $1`, personID); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM person_phone WHERE person_id = $1`, personID); err != nil {
		return nil, err
	}
	// Anonymize the lead twins and drop their field-level provenance in
	// one pass: the provenance rows describe WHO captured WHICH of the
	// subject's fields from WHERE — subject-linked metadata that must not
	// outlive the fields it annotates. The CTE runs the UPDATE first and
	// feeds the touched lead ids to the DELETE, so the email match still
	// sees the pre-anonymize addresses; the same ids flow back out for
	// the per-twin tombstones.
	leadCustom, err := subjectCustomColumns(ctx, tx, "lead")
	if err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx, fmt.Sprintf(`
		WITH wiped AS (
		  UPDATE lead SET full_name = 'Anonymized Lead', email = NULL, title = NULL,
		    company_name = NULL, candidate_org_key = NULL, raw = NULL,
		    archived_at = coalesce(archived_at, now())%s
		  WHERE promoted_person_id = $1
		     OR id IN (SELECT converted_from_lead_id FROM person WHERE id = $1 AND converted_from_lead_id IS NOT NULL)
		     OR (email IS NOT NULL AND lower(email) = ANY($2))
		  RETURNING id
		), pruned AS (
		  DELETE FROM field_provenance
		  WHERE object_type = 'lead' AND object_id IN (SELECT id FROM wiped)
		)
		SELECT id FROM wiped`, nullColumnAssignments(leadCustom)),
		personID, lowercased(emails))
	if err != nil {
		return nil, err
	}
	wiped, err := pgx.CollectRows(rows, pgx.RowTo[ids.UUID])
	if err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx,
		`DELETE FROM embedding WHERE entity_type = 'person' AND entity_id = $1`, personID); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx,
		`DELETE FROM field_provenance WHERE object_type = 'person' AND object_id = $1`, personID); err != nil {
		return nil, err
	}
	return wiped, nil
}

// tombstoneCollateralScrubs stamps a per-record erase tombstone for each
// record the erasure scrubbed alongside the subject. The field-history
// projection cuts a record's timeline at ITS OWN newest erase row — the
// person's tombstone cannot bound a lead twin's or an activity's spine,
// so without these the scrubbed records' historical audit images (a lead
// create's email, an activity create's subject line) would project the
// erased PII straight back out. The scrub context rides evidence, like
// the person tombstone's counts — before/after stay empty, because a
// tombstone must never re-store what it certifies gone and its images
// are served verbatim by the record-history read. No
// paired outbox event on purpose: the erasure's single retention.applied
// on the person is the bus-visible fact, and the collateral scrubs have
// never announced themselves per record.
func tombstoneCollateralScrubs(ctx context.Context, tx pgx.Tx, entityType string, records []ids.UUID, reason string) error {
	for _, id := range records {
		if _, err := storekit.AuditWithEvidence(ctx, tx, actionErase, entityType, id, nil, nil, map[string]any{
			"reason": reason, "cause": "person_erasure",
		}); err != nil {
			return fmt.Errorf("tombstoning scrubbed %s: %w", entityType, err)
		}
	}
	return nil
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
// name is no longer full-text searchable). The subject's attachments are
// purged separately by eraseAttachments (objects first); this handles only
// the timeline text and its field-level provenance. It returns the
// redacted activity ids so the caller can tombstone each record's own
// audit spine.
func redactSubjectTimeline(ctx context.Context, tx pgx.Tx, personID ids.PersonID) ([]ids.UUID, error) {
	rows, err := tx.Query(ctx, `
		UPDATE activity SET subject = $2, body = NULL,
		  archived_at = coalesce(archived_at, now())
		WHERE id IN (`+subjectOnlyActivities+`)
		RETURNING id`, personID, erasedName)
	if err != nil {
		return nil, err
	}
	redacted, err := pgx.CollectRows(rows, pgx.RowTo[ids.UUID])
	if err != nil {
		return nil, err
	}
	// The redacted rows' field-level provenance goes with the fields it
	// annotated — origin metadata must not outlive the erased text.
	if _, err := tx.Exec(ctx, `
		DELETE FROM field_provenance
		WHERE object_type = 'activity' AND object_id IN (`+subjectOnlyActivities+`)`,
		personID); err != nil {
		return nil, err
	}
	return redacted, nil
}

// purgeDerivedTraces removes what the system DERIVED from the subject
// and arms the suppression list. Raw capture is purged by identifier
// match: any stored provider payload carrying one of the subject's
// addresses goes — crude on purpose, over-deleting evidence is
// recoverable by re-sync, under-deleting PII is a violation. Embeddings
// of activities on the subject's timeline embed text ABOUT them; the
// vector store must not keep what a similarity probe could partially
// reconstruct.
func purgeDerivedTraces(ctx context.Context, tx pgx.Tx, personID ids.PersonID, emails []string) (int64, error) {
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
