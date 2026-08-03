// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package privacy

// The retention engine (data-model §3.4, ADR-0011): a nightly pass
// evaluates ONE workspace's enabled policies and applies the policy's
// single action to over-age records, one audited transaction per
// record. legal_hold rows are NEVER auto-acted, and an activity is
// held transitively when any linked person/organization/deal is held —
// a hold on the subject must cover the evidence about them.
//
// The fleet is somebody else's problem: this engine takes the workspace it
// is given, so a tenant's pass has one caller to fail to.

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/blobstore"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/ports/jurisdiction"
)

// retentionAppliedPayload builds the retention.applied wire payload — the
// subject travels separately (the caller's own entityType, passed to
// storekit.EmitEventForEntity), since this event's entity is dynamic
// (ai_call / voice_learning_signal / a policy's object type / person, one
// per site). policyID/reason are each nil where that site's
// action carries no such value — the union this schema's optional
// policy/reason fields exist for.
func retentionAppliedPayload(action string, policyID *ids.UUID, reason *string) crmcontracts.PublicEventRetentionApplied {
	payload := crmcontracts.PublicEventRetentionApplied{Action: action}
	if policyID != nil {
		policy := openapi_types.UUID(*policyID)
		payload.Policy = &policy
	}
	payload.Reason = reason
	return payload
}

// erasedActivitySubject is the tombstone the retention erase action leaves in
// an over-age activity's subject line — and, through redactDeliveries, in the
// subject line of the delivery that transmitted it. One spelling, because the
// two rows describe one message and must never read differently about it.
const erasedActivitySubject = "Erased"

// retentionBatch bounds how many rows one policy acts on per pass — a
// first run against years of backlog drains over successive nights
// instead of one giant transaction.
const retentionBatch = 200

// MaxRecordDuration is the allowance one record's action gets. It is a stated
// bound, not an enforced deadline — nothing in this engine cancels a record
// mid-transaction — so it exists for the scheduler that must cap the pass and
// has to know what a slow-but-healthy record costs. The heaviest action sets
// it: person/erase is a ~30-statement transaction that also deletes the
// subject's attachment objects from the object store over the network.
const MaxRecordDuration = 10 * time.Second

// MaxPassDuration is the ceiling on ONE workspace's retention pass: every
// batched stage it can run, times the batch bound, times the per-record
// allowance, because a pass applies its records sequentially, one audited
// transaction each. A scheduler that must cap the pass reads it from here
// rather than re-deriving it from constants it cannot see, so raising any bound
// moves the cap with it.
//
// The stage count is derived, not hand-counted. retention_policy is UNIQUE on
// (workspace_id, object_type, category), so a workspace configures at most one
// policy per scope the engine has a selector for, and a policy whose scope has
// no selector is skipped without ever claiming a batch — len(retentionSelectors)
// is therefore the whole policy ladder. aiRetentionStages adds the engine-owned
// AI stores, which batch the same way.
var MaxPassDuration = time.Duration(len(retentionSelectors)+aiRetentionStages) *
	(retentionBatch * MaxRecordDuration)

// embedCallRetention bounds how long an embedding-kind ai_call trace row
// survives (spec §4), in days. Unlike the retention_policy rows above,
// this is a fixed operational cap, not an admin-editable per-workspace
// setting: ai_call carries no subject content (it is telemetry — routing,
// spend, and identity facts, never a customer's data), so its age-out is
// engine-owned hygiene, the same footing as correspondenceFloorPredicate
// below rather than a §3.4 storage-limitation policy. Only the embedding
// kind is aged because its volume is different in kind, not in risk:
// every indexed record emits embed rows on every re-index, and past the
// spend ledger's monthly close they answer no question a completion row
// doesn't. Completion rows ARE the certification substrate (attempt
// ladders, served identity, config lineage) and stay until a spec
// retention rule says otherwise.
const embedCallRetention = 90

// RetentionService drives the evaluator over one bound workspace at a time;
// the worker role schedules a pass per tenant.
type RetentionService struct {
	pool   *pgxpool.Pool
	eraser *Eraser
	log    *slog.Logger
	// invalidateEdges keeps the relationship aggregates true as retention
	// removes the interactions beneath them. Injected by compose; nil in a
	// role that did not wire it, where the bus consumer is the fallback.
	invalidateEdges EdgeInvalidator
}

// NewRetentionService wires the nightly evaluator. blob lets its erase
// action purge attachment objects (Art. 17 reaches the bytes); pass nil in
// a deployment with no object store, where no attachment object can exist.
func NewRetentionService(pool *pgxpool.Pool, blob blobstore.Store, log *slog.Logger) *RetentionService {
	return &RetentionService{pool: pool, eraser: NewEraser(pool).WithBlobstore(blob), log: log}
}

// EdgeInvalidator re-folds the relationship aggregates an activity fed, inside
// the caller's transaction.
//
// It is a SEAM rather than a call because the fold lives in the search module
// and a module never imports a sibling; compose injects the real one. The
// alternative — writing the fold a second time here — was tried and was wrong
// in the way second copies are: it deleted the pairs that lost all their
// evidence and left every surviving pair still counting the removed
// interaction.
type EdgeInvalidator func(ctx context.Context, tx pgx.Tx, activityID ids.UUID) error

// WithEdgeInvalidator returns a service that keeps the relationship graph true
// as retention removes the interactions under it, IN THE SAME TRANSACTION.
//
// Synchronous on purpose. The consumer also handles `retention.applied`, and
// the recompute is idempotent so running twice costs nothing — but a bus is a
// thing that can be behind, and "the aggregate is corrected unless the queue is
// slow" is not a property worth having when the correction is a deletion
// obligation. The event path is the backstop; this is the guarantee.
func (s *RetentionService) WithEdgeInvalidator(fn EdgeInvalidator) *RetentionService {
	out := *s
	out.invalidateEdges = fn
	return &out
}

// invalidateGraph runs the injected invalidator, if the role wired one. A role
// that did not is not broken: the consumer still corrects the aggregate on the
// next delivery.
func (s *RetentionService) invalidateGraph(ctx context.Context, tx pgx.Tx, id ids.UUID) error {
	if s.invalidateEdges == nil {
		return nil
	}
	return s.invalidateEdges(ctx, tx, id)
}

// selectors name the records a (object_type, category) policy governs.
// The closed map is deliberate: a policy row with a scope the engine
// does not understand is skipped LOUDLY (logged every pass), never
// half-applied. Every query filters the hold column — and for
// activities, the holds of every linked record plus the statutory floor.
var retentionSelectors = map[string]string{
	"lead/unconverted": `SELECT id FROM lead
		WHERE status IN ('new','working') AND archived_at IS NULL AND NOT legal_hold
		  AND full_name IS DISTINCT FROM 'Anonymized Lead'
		  AND created_at < now() - make_interval(days => $1) LIMIT $2`,
	"activity/": `SELECT a.id FROM activity a
		WHERE a.archived_at IS NULL
		  AND a.occurred_at < now() - make_interval(days => $1)
		  ` + correspondenceFloorPredicate(3, 4) + `
		  AND NOT EXISTS (SELECT 1 FROM activity_link l
		        LEFT JOIN person p ON p.id = l.person_id
		        LEFT JOIN organization o ON o.id = l.organization_id
		        LEFT JOIN deal d ON d.id = l.deal_id
		        WHERE l.activity_id = a.id
		          AND (coalesce(p.legal_hold, false) OR coalesce(o.legal_hold, false) OR coalesce(d.legal_hold, false)))
		LIMIT $2`,
	"activity/transcript": `SELECT a.id FROM activity a
		WHERE a.source_system = 'transcript' AND a.body IS NOT NULL
		  AND a.occurred_at < now() - make_interval(days => $1)
		  ` + correspondenceFloorPredicate(3, 4) + `
		  AND NOT EXISTS (SELECT 1 FROM activity_link l
		        LEFT JOIN person p ON p.id = l.person_id
		        LEFT JOIN organization o ON o.id = l.organization_id
		        LEFT JOIN deal d ON d.id = l.deal_id
		        WHERE l.activity_id = a.id
		          AND (coalesce(p.legal_hold, false) OR coalesce(o.legal_hold, false) OR coalesce(d.legal_hold, false)))
		LIMIT $2`,
	"person/no_consent_no_deal": `SELECT p.id FROM person p
		WHERE p.archived_at IS NULL AND NOT p.legal_hold
		  AND p.full_name IS DISTINCT FROM 'Erased Subject'
		  AND p.created_at < now() - make_interval(days => $1)
		  AND NOT EXISTS (SELECT 1 FROM person_consent pc WHERE pc.person_id = p.id AND pc.state = 'granted')
		  AND NOT EXISTS (SELECT 1 FROM relationship r
		        WHERE r.kind = 'deal_stakeholder' AND r.person_id = p.id AND r.archived_at IS NULL)
		LIMIT $2`,
	"deal/lost": `SELECT id FROM deal
		WHERE status = 'lost' AND archived_at IS NULL AND NOT legal_hold
		  AND closed_at < now() - make_interval(days => $1) LIMIT $2`,
	"ai_call_payload/content": `SELECT id FROM ai_call_payload
		WHERE occurred_at < now() - make_interval(days => $1) LIMIT $2`,
}

type retentionPolicy struct {
	// ID stays ids.UUID: a retention policy is a config row, not a
	// first-class entity, so the kernel mints no kind for it.
	ID         ids.UUID
	ObjectType string
	Category   *string
	RetainDays int
	Action     string
}

// EvaluateWorkspace is ONE workspace's retention pass — the workspace the
// caller's context is already bound to, with no enumeration of its own. Its
// error is the tenant's verdict and belongs to whoever asked for the pass: a
// pass that failed and reported success leaves subject data stored past its
// policy with nothing recording that it happened.
func (s *RetentionService) EvaluateWorkspace(ctx context.Context) error {
	var policies []retentionPolicy
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT id, object_type, category, retain_days, action
			FROM retention_policy WHERE enabled ORDER BY object_type, retain_days`)
		if err != nil {
			return err
		}
		policies, err = pgx.CollectRows(rows, pgx.RowToStructByPos[retentionPolicy])
		return err
	})
	if err != nil {
		return err
	}

	// ONE reference instant per workspace pass: the strictest-floor
	// comparison anchors mixed-unit periods at a timestamp, so a fresh
	// time.Now() per policy could order the floors differently between
	// two activity policies of the same run.
	ref := time.Now()
	for _, pol := range policies {
		scope := pol.ObjectType + "/"
		if pol.Category != nil {
			scope += *pol.Category
		}
		selector, known := retentionSelectors[scope]
		if !known {
			s.log.Warn("retention: policy scope has no selector — skipped, not half-applied",
				"scope", scope, "policy", pol.ID)
			continue
		}
		args := []any{pol.RetainDays, retentionBatch}
		if pol.ObjectType == "activity" {
			floor := jurisdiction.RetentionClass{}
			if pol.Action != "archive" {
				floor = statutoryCorrespondenceFloor(ref)
			}
			args = append(args, floor.Keep.String(), floor.Anchor == jurisdiction.AnchorCalendarYearEnd)
		}
		// due stays untyped: the selector's entity varies by policy scope
		// (lead, activity, person, deal), so the id kind is only known one
		// dispatch deeper, in apply.
		var due []ids.UUID
		err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
			rows, err := tx.Query(ctx, selector, args...)
			if err != nil {
				return err
			}
			due, err = pgx.CollectRows(rows, pgx.RowTo[ids.UUID])
			return err
		})
		if err != nil {
			return fmt.Errorf("retention %s: select: %w", scope, err)
		}
		for _, id := range due {
			if err := s.apply(ctx, pol, id); err != nil {
				return fmt.Errorf("retention %s on %s: %w", scope, id, err)
			}
		}
	}
	if err := s.evaluateEmbedCallRetention(ctx); err != nil {
		return err
	}
	return s.evaluateVoiceSignalRetention(ctx)
}

// voiceSignalRetention note: the deadline itself is stamped per row
// (voice_learning_signal.retention_until, set at capture); this sweep only
// honors it — the window is the ai module's fixed operational floor, not a
// policy-configurable domain record.

// apply runs ONE action on ONE record in one audited transaction.
func (s *RetentionService) apply(ctx context.Context, pol retentionPolicy, id ids.UUID) error {
	if pol.ObjectType == "person" && pol.Action == actionErase {
		return s.eraser.ErasePerson(ctx, id, "retention")
	}
	return database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		switch pol.ObjectType + "/" + pol.Action {
		case "activity/archive":
			_, err = tx.Exec(ctx, `UPDATE activity SET archived_at = now() WHERE id = $1`, id)
			if err == nil {
				err = s.invalidateGraph(ctx, tx, id)
			}
		case "activity/erase":
			// Transcript free-text is the special-category risk; the
			// record of the meeting stays, its content goes — including any
			// attached recording/transcript file (objects first, so the
			// purge shares the person-erase durability guarantee).

			_, err = tx.Exec(ctx,
				`UPDATE activity SET body = NULL, subject = $2, archived_at = coalesce(archived_at, now()) WHERE id = $1`,
				id, erasedActivitySubject)
			if err == nil {
				_, err = tx.Exec(ctx,
					`DELETE FROM embedding WHERE entity_type = 'activity' AND entity_id = $1`, id)
			}
			if err == nil {
				err = s.invalidateGraph(ctx, tx, id)
			}
			if err == nil {
				err = s.eraser.eraseAttachments(ctx, tx, `entity_type = 'activity' AND entity_id = $1`, id)
			}
			if err == nil {
				// An outbound message ages out on the schedule of the activity
				// it belongs to: the send log holds the same recipients,
				// subject and body, and a policy that emptied one while the
				// other kept serving them would age out nothing.
				err = redactDeliveries(ctx, tx, []ids.UUID{id}, erasedActivitySubject)
			}
		case "deal/archive":
			_, err = tx.Exec(ctx, `UPDATE deal SET archived_at = now() WHERE id = $1`, id)
		case "ai_call_payload/erase":
			// The payload row is deleted outright, not scrubbed in place —
			// unlike activity/erase there is no metadata half of this record
			// left to keep: ai_call_payload IS the special-category-adjacent
			// content, and ai_call (the metadata row it FK-cascades from)
			// survives untouched. The retention audit entry below carries no
			// payload bytes, only policy metadata.
			_, err = tx.Exec(ctx, `DELETE FROM ai_call_payload WHERE id = $1`, id)
		case "lead/anonymize":
			_, err = tx.Exec(ctx, `
				UPDATE lead SET full_name = 'Anonymized Lead', email = NULL, title = NULL,
				  company_name = NULL, candidate_org_key = NULL, raw = NULL,
				  archived_at = coalesce(archived_at, now())
				WHERE id = $1`, id)
			if err == nil {
				_, err = tx.Exec(ctx,
					`DELETE FROM embedding WHERE entity_type = 'lead' AND entity_id = $1`, id)
			}
		case "person/anonymize":
			// The subject's addresses, read BEFORE person_email is deleted
			// below. The graph structures name them by raw address as well as
			// by person id — that is what the address arm of a participant row
			// IS — so a sweep that only matched person_id would leave the
			// address behind, still readable and still re-matchable. Same trap
			// the eraser hit with the subject's NAME, one column over.
			subjectEmails, emailErr := collectStrings(ctx, tx,
				`SELECT lower(email) FROM person_email WHERE person_id = $1`, id)
			if emailErr != nil {
				return emailErr
			}
			// The NAME too, and before the anonymization below overwrites it —
			// the ghost sweep matches on it, and by then it is the tombstone.
			var subjectName string
			if nameErr := tx.QueryRow(ctx,
				`SELECT coalesce(full_name, '') FROM person WHERE id = $1`, id).Scan(&subjectName); nameErr != nil {
				return nameErr
			}
			// Same in-place anonymization the eraser uses, minus the
			// suppression list — the subject may lawfully return.
			_, err = tx.Exec(ctx, `
				UPDATE person SET first_name = NULL, last_name = NULL, full_name = $2,
				  title = NULL, raw = NULL,
				  address_line1 = NULL, address_line2 = NULL, address_city = NULL,
				  address_region = NULL, address_postal_code = NULL, address_country = NULL,
				  archived_at = coalesce(archived_at, now())
				WHERE id = $1`, id, erasedName)
			if err == nil {
				_, err = tx.Exec(ctx, `DELETE FROM person_social WHERE person_id = $1`, id)
			}
			if err == nil {
				_, err = tx.Exec(ctx, `DELETE FROM person_email WHERE person_id = $1`, id)
			}
			if err == nil {
				_, err = tx.Exec(ctx, `DELETE FROM person_phone WHERE person_id = $1`, id)
			}
			if err == nil {
				// The channel identity is a resolution key on the subject as
				// much as their address: left behind, it would keep binding
				// inbound messages to the row this sweep just anonymized.
				_, err = tx.Exec(ctx,
					`DELETE FROM person_channel_identity WHERE person_id = $1`, id)
			}
			if err == nil {
				_, err = tx.Exec(ctx,
					`DELETE FROM embedding WHERE entity_type = 'person' AND entity_id = $1`, id)
			}
			// The relationship-graph structures (ADR-0078) hold the subject as
			// surely as the columns above, and the time-based sweep reaches
			// them for the same reason the request-driven eraser does: an
			// anonymized person who is still named on a participant row, still
			// counted in an interaction edge, or still listed in an imported
			// address book is not anonymized. This sweep is the path nobody
			// asks for, which is exactly why it must not be the thinner one.
			if err == nil {
				// Delete then null, in that order and for the reason the
				// eraser documents: a participant row must name somebody, so a
				// row whose only identity is the subject cannot be blanked,
				// while one that also names a colleague is not the subject's
				// to remove.
				_, err = tx.Exec(ctx, `
					DELETE FROM activity_participant
					 WHERE user_id IS NULL
					   AND (person_id = $1 OR (address IS NOT NULL AND address = ANY($2)))`,
					id, subjectEmails)
			}
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
		default:
			return fmt.Errorf("retention: no executor for %s/%s", pol.ObjectType, pol.Action)
		}
		if err != nil {
			return err
		}
		// Retention audits under the verb of the action it ran —
		// archive, anonymize and erase are all in the closed audit
		// vocabulary (0053) — so a governance read can tell a retention
		// anonymize from a user edit, and the field-history projection
		// can treat anonymize/erase as its scrub boundary instead of
		// parsing payload shapes. The policy metadata rides the evidence
		// column, and before/after stay nil: this row records that a
		// policy acted, not a field diff, so a projectable verb like
		// archive must carry no payload the field-history diff could
		// mistake for record fields.
		auditID, err := storekit.AuditWithEvidence(ctx, tx, pol.Action, pol.ObjectType, id, nil, nil, map[string]any{
			evidenceKeyRetentionAction: pol.Action, "policy": pol.ID, "retain_days": pol.RetainDays,
		})
		if err != nil {
			return err
		}
		policyID := pol.ID
		return storekit.EmitEventForEntity(ctx, tx, auditID, pol.ObjectType, id, retentionAppliedPayload(pol.Action, &policyID, nil))
	})
}
