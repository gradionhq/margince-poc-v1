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
	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/blobstore"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/platform/settings"
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

// actionAnonymize is the in-place anonymization action. Named beside the
// erase/archive constants the sibling files declare so isDestructive reads as a
// closed set rather than a pair of string literals.
const actionAnonymize = "anonymize"

// erasedActivitySubject is the tombstone the retention erase action leaves in
// an over-age activity's subject line — and, through redactDeliveries, in the
// subject line of the delivery that transmitted it. One spelling, because the
// two rows describe one message and must never read differently about it.
const erasedActivitySubject = "Erased"

// retentionBatch bounds how many rows one policy acts on per pass — a
// first run against years of backlog drains over successive nights
// instead of one giant transaction.
const retentionBatch = 200

// maxRecordDuration is the allowance one record's action gets. It is a stated
// bound, not an enforced deadline — nothing in this engine cancels a record
// mid-transaction — so it exists for the scheduler that must cap the pass and
// has to know what a slow-but-healthy record costs. The heaviest action sets
// it: person/erase is a ~30-statement transaction that also deletes the
// subject's attachment objects from the object store over the network.
const maxRecordDuration = 10 * time.Second

// MaxPassDuration is the ceiling on ONE workspace's retention pass: every
// batched stage it can run, times the batch bound, times the per-record
// allowance, because a pass applies its records sequentially, one audited
// transaction each. A scheduler that must cap the pass reads it from here
// rather than re-deriving it from constants it cannot see, so raising any bound
// moves the cap with it.
//
// The stage count is one per selector, and two ENFORCED facts hold it there —
// neither of them a convention, because an admin can author a policy row:
//
//   - `retention_policy_unique` is UNIQUE NULLS NOT DISTINCT, so the database
//     refuses a second row for one scope, the NULL-category scope included (a
//     plain UNIQUE would not: Postgres counts NULLs as distinct).
//   - every write resolves its scope through ParseRetentionScope, so a policy
//     can only exist for a scope this table has a selector for.
//
// Together: at most one enabled policy per selector, therefore at most
// len(retentionSelectors) policy stages. A second row for one selector is
// precisely what would make this bound a fiction, which is why both facts are
// enforced rather than assumed.
//
// The §3.4 ladder is unaffected: its rungs are different scopes (`activity` at
// 1095, `activity/transcript` at 365), never repeated ones.
//
// aiRetentionStages adds the engine-owned AI stores, which batch the same way.
var MaxPassDuration = time.Duration(len(retentionSelectors)+aiRetentionStages) *
	(retentionBatch * maxRecordDuration)

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
	// db binds the installation\'s workspace itself (ADR-0091 §9 step 3).
	db     *database.DB
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
func NewRetentionService(db *database.DB, blob blobstore.Store, log *slog.Logger) *RetentionService {
	return &RetentionService{db: db, eraser: NewEraser(db).WithBlobstore(blob), log: log}
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
	err := s.db.Tx(ctx, func(tx pgx.Tx) error {
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
		retainOnly, err := s.retainOnly(ctx)
		if err != nil {
			return err
		}
		if retainOnly && isDestructive(pol.Action) {
			// Suppressed at the POLICY, before the due-list query: the records
			// are never selected, so a retain-only pass costs one log line per
			// suppressed policy rather than a batch of reads that act on
			// nothing.
			//
			// The posture is re-read PER STAGE rather than once per pass, and the
			// asymmetry is the reason: a pass is bounded at hours, so an admin who
			// turns the posture on while one is running would otherwise keep
			// watching records be destroyed until it ended. Partial suppression is
			// strictly better than none when the change is toward KEEPING, and a
			// stage is the smallest unit at which stopping costs nothing.
			//
			// Not audited. audit_log records mutations, and a suppression is the
			// absence of one — a row per skipped record would be up to a full
			// batch per policy per night, forever, saying nothing happened. The
			// POSTURE CHANGE is the audited event (settings write, under
			// RetainOnly's own verb), and the surface reports the live state as
			// suppressed_by_posture so nobody has to read the log to see it.
			s.log.Info("retention: retain-only posture suppresses a destructive policy",
				"scope", scope, "policy", pol.ID, "action", pol.Action)
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
		err = s.db.Tx(ctx, func(tx pgx.Tx) error {
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
	// The two engine-owned sweeps, and they part company under the posture.
	// The embed sweep still runs — ai_call's columns are routing, spend and
	// served identity, so ageing them out is hygiene rather than storage
	// limitation — but narrowed to rows whose deletion cannot reach content
	// through the ai_call_payload cascade (embedCallWithoutPayload). The voice
	// corpus stops outright: it holds draft plaintext about real correspondence.
	retainOnly, err := s.retainOnly(ctx)
	if err != nil {
		return err
	}
	if err := s.evaluateEmbedCallRetention(ctx, retainOnly); err != nil {
		return err
	}
	if retainOnly {
		s.log.Info("retention: retain-only posture suppresses the voice-signal content sweep")
		return nil
	}
	return s.evaluateVoiceSignalRetention(ctx)
}

// retainOnly reads the installation's retain-only posture (GCS-PARAM-6).
//
// Its own short transaction, called once per stage. The alternative — one read
// for the whole pass — would let a pass that began before an admin set the
// posture keep destroying records for as long as it ran, which for a bound
// measured in hours is the wrong answer in the one direction that matters.
func (s *RetentionService) retainOnly(ctx context.Context) (bool, error) {
	var held bool
	err := s.db.Tx(ctx, func(tx pgx.Tx) error {
		var err error
		held, err = settings.GetTx(ctx, tx, RetainOnly)
		return err
	})
	if err != nil {
		return false, fmt.Errorf("retention: reading the retain-only posture: %w", err)
	}
	return held, nil
}

// isDestructive reports whether an action destroys data. `archive` retains — it
// sets archived_at and the record stays readable — so it is the one action the
// retain-only posture leaves alone.
func isDestructive(action string) bool {
	return action == actionAnonymize || action == actionErase
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
	return s.db.Tx(ctx, func(tx pgx.Tx) error {
		var err error
		switch pol.ObjectType + "/" + pol.Action {
		case "activity/archive":
			_, err = tx.Exec(ctx, `UPDATE activity SET archived_at = now() WHERE id = $1`, id)
			if err == nil {
				err = s.invalidateGraph(ctx, tx, id)
			}
		case "activity/erase":
			err = s.eraseActivityContent(ctx, tx, id)
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
			err = anonymizePersonRecord(ctx, tx, id)
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

// eraseActivityContent is the activity/erase action. Transcript free-text is
// the special-category risk; the record of the meeting stays, its content goes
// — including any attached recording/transcript file (objects first, so the
// purge shares the person-erase durability guarantee).
func (s *RetentionService) eraseActivityContent(ctx context.Context, tx pgx.Tx, id ids.UUID) error {
	_, err := tx.Exec(ctx,
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
	return err
}

// anonymizePersonRecord is the person/anonymize action: the same in-place
// anonymization the eraser performs, minus the suppression list — the subject
// may lawfully return.
func anonymizePersonRecord(ctx context.Context, tx pgx.Tx, id ids.UUID) error {
	// The subject's addresses, read BEFORE person_email is deleted
	// below. The graph structures name them by raw address as well as
	// by person id — that is what the address arm of a participant row
	// IS — so a sweep that only matched person_id would leave the
	// address behind, still readable and still re-matchable. Same trap
	// the eraser hit with the subject's NAME, one column over.
	subjectEmails, err := collectStrings(ctx, tx,
		`SELECT lower(email) FROM person_email WHERE person_id = $1`, id)
	if err != nil {
		return err
	}
	// The NAME too, and before the anonymization below overwrites it —
	// the ghost sweep matches on it, and by then it is the tombstone.
	var subjectName string
	if err := tx.QueryRow(ctx,
		`SELECT coalesce(full_name, '') FROM person WHERE id = $1`, id).Scan(&subjectName); err != nil {
		return err
	}
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
		// The enrichment sidecar holds the subject's title and employer with
		// the verbatim sentence naming them. Anonymizing the person row above
		// cascades to nothing, so a sweep that skipped this would leave the
		// quote standing beside an "Erased Subject" record.
		_, err = tx.Exec(ctx, `DELETE FROM person_profile_field WHERE person_id = $1`, id)
	}
	if err == nil {
		// Purchased provider values, and the runs that bought them. Same
		// reasoning as the sidecar above and the same statements the erasure
		// path uses: anonymize-in-place cascades to nothing, so without these
		// the person page would show a bought email and employer beside an
		// "Erased Subject" name.
		_, err = tx.Exec(ctx, `DELETE FROM person_provider_claim WHERE person_id = $1`, id)
	}
	if err == nil {
		_, err = tx.Exec(ctx,
			`UPDATE provider_run SET`+storekit.ScrubProviderRunColumns+` WHERE person_id = $1`, id)
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
	if err == nil {
		err = scrubPersonGraphTraces(ctx, tx, id, subjectEmails, subjectName)
	}
	return err
}
