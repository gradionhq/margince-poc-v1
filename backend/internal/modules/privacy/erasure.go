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
// per-module cascade could commit half an erasure (the sanctioned
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

// evidenceKeyRetentionAction is the audit-evidence key naming which
// retention action ran; one spelling across every sweep.
const evidenceKeyRetentionAction = "retention_action"

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
// anonymized, email/phone/channel-identity child rows deleted, raw
// capture purged, embeddings dropped, identifiers hashed onto the
// suppression list, tombstone written. Deleting a person row outright would cascade into
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
	// The statutory correspondence floor the retention engine applies to its
	// activity selectors applies here too: erasing the person a Handelsbrief
	// hangs off must not destroy the correspondence itself below its floor.
	floorInterval, floorAnchor := statutoryFloorArgs()
	return database.WithWorkspaceTx(ctx, e.pool, func(tx pgx.Tx) error {
		if err := auth.EnsureVisibleForSubjectRights(ctx, tx, "person", subject.UUID); err != nil {
			return err
		}
		if err := refusePersonUnderLegalHold(ctx, tx, subject); err != nil {
			return err
		}
		emails, identities, err := subjectIdentifiers(ctx, tx, subject)
		if err != nil {
			return err
		}
		// Taken before the first statement that purges or suppresses by
		// channel identity, and held until the commit: an inbound message
		// from one of these accounts must land entirely before this erasure
		// or entirely after it, never inside it — storekit.LockChannelIdentities
		// states what landing inside it costs.
		if err := storekit.LockChannelIdentities(ctx, tx, channelIdentityLockKeys(identities)); err != nil {
			return err
		}
		// Refused BEFORE the first destructive statement: everything below
		// this line suppresses and purges by IDENTIFIER, and a rival record
		// holding the same identifier would be left named, reachable, and
		// stripped of the evidence that was never this request's to destroy
		// (erasure_rivals.go).
		if err := refuseRivalIdentifierHolders(ctx, tx, subject, emails, identities); err != nil {
			return err
		}

		leadsWiped, err := anonymizeSubjectRows(ctx, tx, subject, emails)
		if err != nil {
			return err
		}
		activitiesRedacted, err := redactSubjectTimeline(ctx, tx, subject, emails, channelActivityKeys(identities), floorInterval, floorAnchor)
		if err != nil {
			return err
		}
		if err := tombstoneCollateralScrubs(ctx, tx, "lead", leadsWiped, reason); err != nil {
			return err
		}
		if err := purgeRedactedActivityTraces(ctx, tx, activitiesRedacted, reason); err != nil {
			return err
		}
		// Purge the subject's attachment bytes and rows together, inside the
		// transaction (objects first). A failure here — including a
		// misconfigured store — rolls the whole erasure back, so it stays
		// retryable and never commits a half-erasure.
		if err := e.eraseAttachments(ctx, tx, subjectAttachmentsWhere, subject, floorInterval, floorAnchor); err != nil {
			return err
		}
		rawPurged, aiPayloadsPurged, err := purgeDerivedTraces(ctx, tx, subject, emails, identities)
		if err != nil {
			return err
		}
		channelsSuppressed, err := eraseChannelIdentities(ctx, tx, identities)
		if err != nil {
			return err
		}

		return tombstonePersonErasure(ctx, tx, subject, reason, personErasureCounts{
			emailsSuppressed: len(emails), rawRowsPurged: rawPurged, aiPayloadsPurged: aiPayloadsPurged,
			activitiesRedacted: len(activitiesRedacted), channelIdentitiesSuppressed: channelsSuppressed,
		})
	})
}

// refusePersonUnderLegalHold refuses an erasure the workspace is obliged to
// refuse: a legal hold says somebody must keep this record, which outranks the
// subject's Art. 17 request until the hold is lifted.
func refusePersonUnderLegalHold(ctx context.Context, tx pgx.Tx, personID ids.PersonID) error {
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
	return nil
}

// subjectIdentifiers collects the addresses and channel accounts the cascade
// suppresses and purges by. Read BEFORE anything is wiped — the suppression
// list needs their hashes, and afterwards nothing holds them.
func subjectIdentifiers(ctx context.Context, tx pgx.Tx, personID ids.PersonID) ([]string, []channelIdentity, error) {
	emails, err := collectStrings(ctx, tx,
		`SELECT email FROM person_email WHERE person_id = $1`, personID)
	if err != nil {
		return nil, nil, err
	}
	// Same reason: read before eraseChannelIdentities deletes the table.
	identities, err := personChannelIdentities(ctx, tx, personID)
	if err != nil {
		return nil, nil, err
	}
	return emails, identities, nil
}

// purgeRedactedActivityTraces finishes off the activities the timeline redaction
// just emptied: their vectors, their own audit spines, and the transmitted copy
// in the send log.
func purgeRedactedActivityTraces(ctx context.Context, tx pgx.Tx, activities []ids.UUID, reason string) error {
	// The vectors go with the text they were built from. purgeDerivedTraces
	// reaches embeddings through activity_link, which by construction cannot
	// see the unlinked mail redactSubjectTimeline now covers — and
	// an embedding of erased text is the erased text in another shape, which
	// a similarity probe can still reach.
	if len(activities) > 0 {
		if _, err := tx.Exec(ctx, `
			DELETE FROM embedding WHERE entity_type = 'activity' AND entity_id = ANY($1)`,
			activities); err != nil {
			return err
		}
	}
	if err := tombstoneCollateralScrubs(ctx, tx, "activity", activities, reason); err != nil {
		return err
	}
	// The transmitted copy of every activity just redacted. Without this
	// the timeline row is a tombstone while the send log still holds the
	// address, the subject line and the body of the same message.
	return redactDeliveries(ctx, tx, activities, erasedName)
}

// personErasureCounts is what an erasure tombstone certifies: how much each arm
// of the cascade removed, and nothing whatsoever about whom.
type personErasureCounts struct {
	emailsSuppressed            int
	rawRowsPurged               int64
	aiPayloadsPurged            int64
	activitiesRedacted          int
	channelIdentitiesSuppressed int
}

// tombstonePersonErasure closes the cascade with action=erase and counts only —
// proof without PII. The counts are evidence ABOUT the scrub, so they ride the
// evidence column; before/after stay empty — they are reserved for field
// images, and the record-history read serves a tombstone's images verbatim. The
// paired event tells consumers the subject is gone.
func tombstonePersonErasure(ctx context.Context, tx pgx.Tx, subject ids.PersonID, reason string, counts personErasureCounts) error {
	auditID, err := storekit.AuditWithEvidence(ctx, tx, actionErase, "person", subject.UUID, nil, nil, map[string]any{
		"reason": reason, "emails_suppressed": counts.emailsSuppressed, "raw_rows_purged": counts.rawRowsPurged,
		"ai_payloads_purged": counts.aiPayloadsPurged, "activities_redacted": counts.activitiesRedacted,
		"channel_identities_suppressed": counts.channelIdentitiesSuppressed,
	})
	if err != nil {
		return err
	}
	return storekit.EmitEventForEntity(ctx, tx, auditID, "person", subject.UUID, retentionAppliedPayload(actionErase, nil, &reason))
}

// anonymizeSubjectRows wipes the subject's PII in place: the person row
// keeps its skeleton (business records other subjects appear in still
// reference it), the email/phone child rows and the preference-center
// token delete outright, the
// SEGREGATED lead twin — the lead they were promoted from, and any lead
// row carrying one of their addresses — anonymizes the same way, and
// the subject's own embeddings drop. Both anonymizing UPDATEs also NULL
// every catalog-defined cf_ column, retired included — a custom column
// holds subject data exactly like a core one (see subjectcolumns.go).
// It returns the wiped lead ids so the caller can tombstone each twin's
// own audit spine.
func anonymizeSubjectRows(ctx context.Context, tx pgx.Tx, personID ids.PersonID, emails []string) ([]ids.UUID, error) {
	// Read BEFORE the person row is anonymized below: the LinkedIn sweep at
	// the end of this function matches on the subject's name, and by then the
	// column holds the tombstone instead.
	var subjectName string
	if err := tx.QueryRow(ctx,
		`SELECT coalesce(full_name, '') FROM person WHERE id = $1`, personID).Scan(&subjectName); err != nil {
		return nil, err
	}
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
	linkedInHandles, err := deleteSubjectIdentifierRows(ctx, tx, personID, emails)
	if err != nil {
		return nil, err
	}
	if err := scrubSubjectFromGraph(ctx, tx, personID, emails, subjectName, linkedInHandles); err != nil {
		return nil, err
	}
	if err := deletePreferenceToken(ctx, tx, personID); err != nil {
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

// deleteSubjectIdentifierRows drops every row that stores an identifier the
// subject was reached by, and returns the LinkedIn profile URLs it removed:
// the ghost sweep identifies rows by them, and person_social no longer holds
// them once this returns.
func deleteSubjectIdentifierRows(ctx context.Context, tx pgx.Tx, personID ids.PersonID, emails []string) ([]string, error) {
	// Read BEFORE the delete: the LinkedIn ghost sweep identifies rows by this
	// address, and person_social is about to stop holding it.
	linkedInHandles, err := collectStrings(ctx, tx,
		`SELECT handle FROM person_social WHERE person_id = $1 AND platform = 'linkedin'`, personID)
	if err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM person_social WHERE person_id = $1`, personID); err != nil {
		return nil, err
	}
	// The capture disposition ledger keys on the subject's own address and
	// carries the display name a message arrived with, so an erasure that
	// stopped at person_email would leave both readable in the ledger — and
	// the address would keep answering the correspondence and pending gates.
	if _, err := tx.Exec(ctx, `
		DELETE FROM capture_pending_counterparty
		 WHERE email IN (SELECT email FROM person_email WHERE person_id = $1)`, personID); err != nil {
		return nil, err
	}
	// By ADDRESS as well as by person_id, for the reason eraseChannelIdentities
	// deletes by account (erasure_rivals.go): uq_person_email_dedupe is partial
	// on archived_at IS NULL, so an archived duplicate Person can hold the same
	// address, and leaving that row behind would keep the erased subject's
	// address stored under a record this erasure suppressed and purged for.
	// A LIVE duplicate never reaches here — the guard refuses the erasure.
	if _, err := tx.Exec(ctx,
		`DELETE FROM person_email WHERE person_id = $1 OR email = ANY($2)`, personID, emails); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM person_phone WHERE person_id = $1`, personID); err != nil {
		return nil, err
	}
	return linkedInHandles, nil
}

// deletePreferenceToken retires the subject's preference-center token. That
// token is a live CAPABILITY over their consent record, not a stored
// attribute of them: whoever holds the emailed List-Unsubscribe URL reads
// their per-purpose state, withdraws, and grants — on an edge that binds a
// system principal, so every RBAC gate downstream passes.
//
// Anonymize-in-place is why erasure has to reach it here rather than leaning
// on the schema: the person row survives, so 0048's ON DELETE CASCADE never
// fires, and an erased subject would keep accruing fresh person_consent,
// consent_event, audit and outbox rows through the exact capability this
// erasure certifies destroyed. Deleted rather than revoked, like the address
// and phone rows beside it — a revoked row still holds the subject's person
// link. The workspace predicate is explicit because preference_token is
// deliberately outside RLS (it IS the token→tenant resolver, 0048), so
// nothing else scopes this statement.
func deletePreferenceToken(ctx context.Context, tx pgx.Tx, personID ids.PersonID) error {
	_, err := tx.Exec(ctx, `
		DELETE FROM preference_token
		 WHERE workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid
		   AND person_id = $1`, personID)
	return err
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
