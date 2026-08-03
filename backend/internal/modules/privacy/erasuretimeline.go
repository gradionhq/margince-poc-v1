// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package privacy

// What an Art. 17 erasure does to the subject's TIMELINE and to everything
// derived from it. Kept apart from the subject's own rows (erasure.go) because
// the questions differ: those rows are the subject's record, while these are
// other people's records that happen to contain them, plus the machine
// artifacts built on top — a raw capture, an embedding, an interaction edge.
// Nothing here can simply be deleted without asking what else it holds.

import (
	"context"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// redactSubjectTimeline erases the subject's free text from the activity
// timeline: subject/body of every subject-only activity are wiped (the
// GENERATED search_tsv refreshes from the now-empty text, so the erased
// name is no longer full-text searchable). The subject's attachments are
// purged separately by eraseAttachments (objects first); this handles only
// the timeline text and its field-level provenance. It returns the
// redacted activity ids so the caller can tombstone each record's own
// audit spine.
func redactSubjectTimeline(ctx context.Context, tx pgx.Tx, personID ids.PersonID, emails []string, channelKeys []string, floorInterval string, floorAnchor bool) ([]ids.UUID, error) {
	// Redact the subject's own timeline rows, the unlinked mail about them — in
	// both directions, captured and sent (unlinkedSubjectMail) — and the
	// unlinked CHANNEL messages from them (unlinkedSubjectChannel), but shield
	// commercial correspondence younger than the
	// statutory floor: the floor filters the row being updated (aliased `a`),
	// so it covers all three id sets in one pass. $1 person, $2 addresses, $3/$4
	// the floor interval + anchor, $5 the tombstone name, $6 the subject's
	// `provider:account` channel keys.
	//
	// source_id and thread_key are cleared for channel rows and ONLY for channel
	// rows, because for those two columns the identifier IS the subject: the
	// capture writes source_id `botID:accountID:messageID` and thread_key
	// `provider:botID:accountID`, and a private chat's id is the human's own
	// Telegram id. Leaving them was a silent Art. 17 hole on the ordinary path —
	// no race required — that emptying subject/body/raw did nothing about. A
	// mail row keeps its natural key: a message-id does not name the subject.
	//
	// Clearing them costs nothing that is still reachable. The pair is the
	// capture idempotency key, but the same erasure arms the suppression list,
	// and Sink.Upsert refuses a suppressed account before it writes — so there
	// is no redelivery left for the key to deduplicate.
	rows, err := tx.Query(ctx, `
		UPDATE activity a SET subject = $5, body = NULL, raw = NULL,
		  counterparty_email = NULL,
		  source_id = CASE WHEN a.source_system || ':' || split_part(coalesce(a.thread_key, ''), ':', 3) = ANY($6)
		                   THEN NULL ELSE a.source_id END,
		  thread_key = CASE WHEN a.source_system || ':' || split_part(coalesce(a.thread_key, ''), ':', 3) = ANY($6)
		                    THEN NULL ELSE a.thread_key END,
		  archived_at = coalesce(a.archived_at, now())
		WHERE (a.id IN (`+subjectOnlyActivities+`)
		    OR a.id IN (`+unlinkedSubjectMail+`)
		    OR a.id IN (`+unlinkedSubjectChannel+`))
		  `+correspondenceFloorPredicate(3, 4)+`
		RETURNING a.id`, personID, emails, floorInterval, floorAnchor, erasedName, channelKeys)
	if err != nil {
		return nil, err
	}
	redacted, err := pgx.CollectRows(rows, pgx.RowTo[ids.UUID])
	if err != nil {
		return nil, err
	}
	// The redacted rows' field-level provenance goes with the fields it
	// annotated — origin metadata must not outlive the erased text. A
	// floor-shielded correspondence row is excluded here too: its provenance
	// stays with the evidence it annotates.
	if _, err := tx.Exec(ctx, `
		DELETE FROM field_provenance
		WHERE object_type = 'activity' AND object_id IN (`+subjectOnlyDestroyable+`)`,
		personID, floorInterval, floorAnchor); err != nil {
		return nil, err
	}
	return redacted, nil
}

// purgeDerivedTraces removes what the system DERIVED from the subject and
// arms the suppression list. Raw capture is purged two ways: by email here
// (crude on purpose — over-deleting evidence is recoverable, under-deleting
// PII is not) and by channel identity in purgeChannelRawCapture
// (erasure_channels.go, kept apart for file length). Embeddings of
// activities on the subject's timeline embed text ABOUT them; the vector
// store must not keep what a similarity probe could partially reconstruct.
func purgeDerivedTraces(ctx context.Context, tx pgx.Tx, personID ids.PersonID, emails []string, identities []channelIdentity) (rawPurged, aiPayloadsPurged int64, err error) {
	for _, email := range emails {
		tag, execErr := tx.Exec(ctx,
			`DELETE FROM raw_capture WHERE payload::text ILIKE '%' || $1 || '%' ESCAPE '\'`,
			storekit.EscapeLike(email))
		if execErr != nil {
			return 0, 0, execErr
		}
		rawPurged += tag.RowsAffected()
	}
	channelRawPurged, err := purgeChannelRawCapture(ctx, tx, identities)
	if err != nil {
		return 0, 0, err
	}
	rawPurged += channelRawPurged
	if _, err := tx.Exec(ctx, `
		DELETE FROM embedding e USING activity_link l
		WHERE e.entity_type = 'activity' AND l.person_id = $1 AND e.entity_id = l.activity_id`,
		personID); err != nil {
		return 0, 0, err
	}
	// Captured AI payloads (Layer 3) are purged by the same identifier
	// match as raw_capture's email lane: any opt-in request/response body
	// whose content names one of the subject's addresses goes, and its
	// ai_call metadata row survives (the FK is ON DELETE CASCADE from
	// ai_call, never the reverse). This reaches ONLY payloads whose text
	// mentions the subject — a call that never named them keeps no PII and
	// ages out anyway via the 365d ai_call_payload retention erase; there is
	// no subject FK to scope by, so a content match is the reachable
	// boundary, crude on purpose (over-deleting captured content is
	// recoverable, under-deleting PII is a violation).
	//
	// No channel-identity lane here, unlike raw_capture above — see
	// purgeChannelRawCapture's comment (erasure_channels.go) for why
	// ai_call_payload cannot safely take the same match.
	for _, email := range emails {
		tag, execErr := tx.Exec(ctx, `
			DELETE FROM ai_call_payload
			WHERE request_payload::text ILIKE '%' || $1 || '%' ESCAPE '\'
			   OR response_payload::text ILIKE '%' || $1 || '%' ESCAPE '\'`,
			storekit.EscapeLike(email))
		if execErr != nil {
			return 0, 0, execErr
		}
		aiPayloadsPurged += tag.RowsAffected()
	}
	for _, email := range emails {
		if _, err := tx.Exec(ctx, `
			INSERT INTO erasure_suppression (workspace_id, kind, value_hash)
			VALUES (NULLIF(current_setting('app.workspace_id', true), '')::uuid, 'email', $1)
			ON CONFLICT DO NOTHING`, storekit.SuppressionHash(email)); err != nil {
			return 0, 0, err
		}
	}
	return rawPurged, aiPayloadsPurged, nil
}
