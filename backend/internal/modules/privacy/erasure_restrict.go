// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package privacy

// The erasure's OTHER outcome (A165/ADR-0114). A Handelsbrief inside its
// statutory window is not destroyed when its subject asks to be erased — but
// it is not left in use either. It is RESTRICTED: the identifiers that are the
// subject go, the commercial substance stays, the row leaves every ordinary
// read path, and the deadline at which the suspended erasure completes is
// pinned onto it. This file is that step; erasuretimeline.go's destroy step
// and this one select by the same floor, negated on one side, so a row is
// always exactly one of erased or held.

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// Audit verbs of the restriction lifecycle (0287, A167/ADR-0116 §6): an
// erasure shielded a record instead of destroying it; the window elapsed and
// the sweep completed the suspended erasure.
const (
	actionRestrict = "restrict"
	actionExpire   = "expire"
)

// retentionClassCorrespondence is the one class the schema admits (0288's
// CHECK), spelled here for the writers in this module; the activities module
// spells it once more for the stamp it writes at qualification.
const retentionClassCorrespondence = "commercial_correspondence"

// statutoryBasisCorrespondence is what the audit tombstone and the
// controller's list cite as the obligation. Not free text: it names the
// statute, and it is the same string every restricted record carries.
const statutoryBasisCorrespondence = "§257 HGB / §147 AO"

// restrictedRecord is one row the erasure held instead of destroying — what
// the tombstone and the outbox event are written from.
type restrictedRecord struct {
	ID              ids.UUID
	RestrictedUntil time.Time
	Class           string
}

// subjectTimelineIDs is the union of the three id sets an erasure walks — the
// subject's own linked rows, the unlinked mail about them, the unlinked
// channel messages from them — over the activity aliased `a`. Both the destroy
// statement and the restrict statements select from it, with the SAME
// placeholder numbering: $1 person, $2 addresses, $6 the `provider:account`
// channel keys.
var subjectTimelineIDs = `(a.id IN (` + subjectOnlyActivities + `)
		    OR a.id IN (` + unlinkedSubjectMail + `)
		    OR a.id IN (` + unlinkedSubjectChannel + `))`

// channelRowOf answers whether the activity aliased `a` is one of the
// subject's channel messages, whose source_id and thread_key ARE the subject
// (the capture writes the account id into both). $6 is the channel-key list.
const channelRowOf = `a.source_system || ':' || split_part(coalesce(a.thread_key, ''), ':', 3) = ANY($6)`

// restrictShieldedTimeline holds every Handelsbrief on the subject's timeline
// that the floor shields, in place of destroying it. Three writes, in order,
// inside the erasure's transaction:
//
//   - rows captured before the stamp existed are STAMPED now, with the
//     evidence the guard demands, from the deal links that are their only
//     evidence — the derived rule's one legitimate use (A165: a floor for
//     legacy data, not the rule);
//   - the shielded rows are restricted: identifiers redacted per datum, the
//     window end pinned, the row archived out of the default lists;
//   - the deliveries behind them lose their addressing and keep their substance.
//
// It returns what it held so the caller can drop the derived copies (vectors,
// readings, proposals) and write the tombstone and the event per record. The
// placeholders are erasuretimeline.go's: $1 person, $2 addresses, $3/$4 the
// floor interval and anchor, $5 the class, $6 the channel keys.
func restrictShieldedTimeline(ctx context.Context, tx pgx.Tx, personID ids.PersonID, emails []string, channelKeys []string, floorInterval string, floorAnchor bool) ([]restrictedRecord, error) {
	args := []any{personID, emails, floorInterval, floorAnchor, retentionClassCorrespondence, channelKeys}
	if err := stampLegacyHandelsbriefe(ctx, tx, args); err != nil {
		return nil, err
	}
	// The SET list reads the row's OLD values, which is what lets
	// redacted_fields name only the columns that actually held something: a
	// field that was already empty was not redacted, and saying it was would
	// be as false as the silence the column exists to prevent.
	rows, err := tx.Query(ctx, `
		UPDATE activity a SET
		  restricted_at = now(), restricted_reason = $5,
		  restricted_until = `+floorWindowEnd(3, 4)+`,
		  raw = NULL, counterparty_email = NULL,
		  source_id  = CASE WHEN `+channelRowOf+` THEN NULL ELSE a.source_id END,
		  thread_key = CASE WHEN `+channelRowOf+` THEN NULL ELSE a.thread_key END,
		  redacted_fields = a.redacted_fields || ARRAY(SELECT c FROM unnest(ARRAY[
		      CASE WHEN a.raw IS NOT NULL THEN 'raw' END,
		      CASE WHEN a.counterparty_email IS NOT NULL THEN 'counterparty_email' END,
		      CASE WHEN `+channelRowOf+` AND a.source_id IS NOT NULL THEN 'source_id' END,
		      CASE WHEN `+channelRowOf+` AND a.thread_key IS NOT NULL THEN 'thread_key' END]) AS c
		    WHERE c IS NOT NULL),
		  archived_at = coalesce(a.archived_at, now())
		WHERE `+subjectTimelineIDs+`
		  AND a.restricted_at IS NULL
		  AND `+handelsbriefShielded(3, 4)+`
		RETURNING a.id, a.restricted_until, a.retention_class`, args...)
	if err != nil {
		return nil, fmt.Errorf("restricting the subject's shielded correspondence: %w", err)
	}
	held, err := pgx.CollectRows(rows, pgx.RowToStructByPos[restrictedRecord])
	if err != nil {
		return nil, err
	}
	if len(held) == 0 {
		return nil, nil
	}
	if err := redactDeliveryAddressing(ctx, tx, recordIDs(held)); err != nil {
		return nil, err
	}
	return held, nil
}

// stampLegacyHandelsbriefe gives a pre-stamp row the class and the evidence
// it would have received had the stamp writer existed when it qualified. It
// reads the links NOW because for these rows the links are the only evidence
// there is — and it writes what it reads down, so from this moment the
// record's own class decides its fate and the links may move.
//
// One basis row per fact, not per row: a deal that is won AND carries a sent
// offer is two facts, and an activity filed under both a qualifying deal and a
// project is two more — exactly as the stamp writers record them.
//
// Every arm must stay in step with handelsbriefArm. A row this misses is a row
// the restrict step selects as shielded and the
// activity_restriction_needs_evidence trigger then refuses, failing the whole
// erasure — so an arm missing here is not an under-stamped record, it is an
// erasure that cannot run at all.
//
// The arms join the qualifying record where handelsbriefArm only tests the
// link, which looks like a gap and is closed by the schema rather than by the
// query: activity_link.deal_id and activity_link.project_id are both ON DELETE
// CASCADE, so a link to a record that no longer exists is not a state the
// database holds. Changing either to SET NULL would open exactly the divergence
// above, which is why the dependency is written here and not left to be
// rediscovered.
func stampLegacyHandelsbriefe(ctx context.Context, tx pgx.Tx, args []any) error {
	if _, err := tx.Exec(ctx, `
		WITH legacy AS (
		  SELECT a.id FROM activity a
		  WHERE `+subjectTimelineIDs+`
		    AND a.restricted_at IS NULL AND a.retention_class IS NULL
		    AND `+handelsbriefShielded(3, 4)+`
		), stamped AS (
		  UPDATE activity a SET retention_class = $5, retention_class_at = now()
		  WHERE a.id IN (SELECT id FROM legacy)
		)
		INSERT INTO activity_retention_evidence (activity_id, basis, qualified_at, deal_id, deal_name, project_id, project_name)
		SELECT l.id, 'deal_won', now(), hd.id, hd.name, NULL::uuid, NULL::text
		  FROM legacy l
		  JOIN activity_link hl ON hl.activity_id = l.id AND hl.entity_type = 'deal'
		  JOIN deal hd ON hd.id = hl.deal_id
		 WHERE hd.status = 'won'
		UNION ALL
		SELECT l.id, 'offer_beyond_draft', now(), hd.id, hd.name, NULL::uuid, NULL::text
		  FROM legacy l
		  JOIN activity_link hl ON hl.activity_id = l.id AND hl.entity_type = 'deal'
		  JOIN deal hd ON hd.id = hl.deal_id
		 WHERE EXISTS (SELECT 1 FROM offer o WHERE o.deal_id = hd.id AND o.status <> 'draft')
		UNION ALL
		SELECT l.id, 'project_linked', now(), NULL::uuid, NULL::text, hp.id, hp.name
		  FROM legacy l
		  JOIN activity_link pl ON pl.activity_id = l.id AND pl.entity_type = 'project'
		  JOIN project hp ON hp.id = pl.project_id
		ON CONFLICT DO NOTHING`, args...); err != nil {
		return fmt.Errorf("stamping the subject's pre-stamp correspondence: %w", err)
	}
	return nil
}

// redactDeliveryAddressing is the restriction's half of redactDeliveries: the
// delivery behind a restricted activity loses WHO it named and keeps WHAT it
// said, on the same per-datum terms as the activity (A165 §3). Shape-aware for
// the reason redactDeliveries states — a mail row's address lists empty to
// `[]`, a channel row's channel_user_id empties to ” because it is also the
// row's shape discriminator, and `reason` goes because a provider's refusal
// routinely quotes the address it refused. A delivery still pending is parked:
// the subject who just exercised Art. 17 must not receive the message the
// erasure held.
func redactDeliveryAddressing(ctx context.Context, tx pgx.Tx, activityIDs []ids.UUID) error {
	if _, err := tx.Exec(ctx, `
		UPDATE comms_outbound
		   SET recipients = '[]'::jsonb, cc = '[]'::jsonb, bcc = '[]'::jsonb,
		       list_unsubscribe = NULL,
		       redacted_fields = redacted_fields || ARRAY(SELECT c FROM unnest(ARRAY[
		           CASE WHEN recipients <> '[]'::jsonb THEN 'recipients' END,
		           CASE WHEN cc <> '[]'::jsonb THEN 'cc' END,
		           CASE WHEN coalesce(bcc, '[]'::jsonb) <> '[]'::jsonb THEN 'bcc' END,
		           CASE WHEN list_unsubscribe IS NOT NULL THEN 'list_unsubscribe' END,
		           CASE WHEN reason IS NOT NULL THEN 'reason' END]) AS c
		         WHERE c IS NOT NULL),
		       status = CASE WHEN status = 'pending' THEN 'parked' ELSE status END,
		       reason = CASE WHEN status = 'pending' THEN $2 ELSE NULL END
		 WHERE activity_id = ANY($1) AND channel_user_id IS NULL`,
		activityIDs, parkedByPrivacyScrub); err != nil {
		return fmt.Errorf("redacting the addressing of restricted deliveries: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE comms_outbound
		   SET channel_user_id = '',
		       redacted_fields = redacted_fields || ARRAY(SELECT c FROM unnest(ARRAY[
		           CASE WHEN channel_user_id <> '' THEN 'channel_user_id' END,
		           CASE WHEN reason IS NOT NULL THEN 'reason' END]) AS c
		         WHERE c IS NOT NULL),
		       status = CASE WHEN status = 'pending' THEN 'parked' ELSE status END,
		       reason = CASE WHEN status = 'pending' THEN $2 ELSE NULL END
		 WHERE activity_id = ANY($1) AND channel_user_id IS NOT NULL`,
		activityIDs, parkedByPrivacyScrub); err != nil {
		return fmt.Errorf("redacting the addressing of restricted channel deliveries: %w", err)
	}
	return nil
}

// tombstoneRestrictions writes the split decision down, per record: the audit
// row is the PROOF a supervisory authority is shown, the outbox event is how
// projections learn that content left the readable world (A165 §2 — an audit
// row alone is not enough). The evidence carries the obligation and the
// transactions it rests on, by id, and no PII: the tombstone must never
// re-store what the restriction is hiding.
func tombstoneRestrictions(ctx context.Context, tx pgx.Tx, held []restrictedRecord) error {
	for _, r := range held {
		dealIDs, err := collectStrings(ctx, tx, `
			SELECT DISTINCT deal_id::text FROM activity_retention_evidence
			 WHERE activity_id = $1 AND deal_id IS NOT NULL ORDER BY 1`, r.ID)
		if err != nil {
			return err
		}
		auditID, err := storekit.AuditWithEvidence(ctx, tx, actionRestrict, "activity", r.ID, nil, nil, map[string]any{
			evidenceKeyCause: "person_erasure", evidenceKeyClass: r.Class, evidenceKeyBasis: statutoryBasisCorrespondence,
			"restricted_until": r.RestrictedUntil, "deal_ids": dealIDs,
		})
		if err != nil {
			return fmt.Errorf("tombstoning restricted activity: %w", err)
		}
		until := r.RestrictedUntil
		class := r.Class
		if err := storekit.EmitEvent(ctx, tx, auditID, r.ID, crmcontracts.PublicEventRetentionRestricted{
			Action:          crmcontracts.Restrict,
			ActivityId:      openapi_types.UUID(r.ID),
			RestrictedUntil: &until,
			RetentionClass:  &class,
		}); err != nil {
			return err
		}
	}
	return nil
}

// holdShieldedTimeline is the erasure's restrict arm end to end: restrict the
// shielded rows, drop the copies DERIVED from their bodies (a vector or a
// reading of a hidden record is the record in another shape, reachable by a
// similarity probe or a proposal), and write the tombstone and the event per
// record. It answers with what it held so the caller can withdraw the
// proposals citing those rows and count them on the person's tombstone.
func holdShieldedTimeline(ctx context.Context, tx pgx.Tx, personID ids.PersonID, emails []string, channelKeys []string, floorInterval string, floorAnchor bool) ([]ids.UUID, error) {
	held, err := restrictShieldedTimeline(ctx, tx, personID, emails, channelKeys, floorInterval, floorAnchor)
	if err != nil || len(held) == 0 {
		return nil, err
	}
	heldIDs := recordIDs(held)
	if _, err := tx.Exec(ctx, `
		DELETE FROM embedding WHERE entity_type = 'activity' AND entity_id = ANY($1)`, heldIDs); err != nil {
		return nil, err
	}
	if err := purgeTranscriptReadings(ctx, tx, heldIDs); err != nil {
		return nil, err
	}
	if err := tombstoneRestrictions(ctx, tx, held); err != nil {
		return nil, err
	}
	return heldIDs, nil
}

func recordIDs(held []restrictedRecord) []ids.UUID {
	out := make([]ids.UUID, 0, len(held))
	for _, r := range held {
		out = append(out, r.ID)
	}
	return out
}
