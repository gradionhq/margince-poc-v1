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
// aiRetentionStages adds the engine-owned AI stores, which batch the same way,
// and restrictionExpiryStages the erasure of held records whose window closed.
var MaxPassDuration = time.Duration(len(retentionSelectors)+aiRetentionStages+restrictionExpiryStages) *
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

// EvaluateInstallation is the installation's retention pass, with no
// enumeration of its own. Its error belongs to whoever asked for the pass: one
// that failed and reported success leaves subject data stored past its policy
// with nothing recording that it happened.
//
// The name is deliberately specific rather than a bare Evaluate: the scope gate
// in retentionscope_test.go matches its sink BY NAME, so a common name would
// make thirty-six unrelated declarations look like sanctioned destinations for
// a context that cannot be denied.
func (s *RetentionService) EvaluateInstallation(ctx context.Context) error {
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
		if err := s.evaluatePolicy(ctx, pol, ref); err != nil {
			return err
		}
	}

	// The two engine-owned sweeps, and they part company under the posture.
	// The embed sweep still runs — ai_call's columns are routing, spend and
	// served identity, so ageing them out is hygiene rather than storage
	// limitation — but narrowed to rows whose deletion cannot reach content
	// through the ai_call_payload cascade (embedCallWithoutPayload). The voice
	// corpus stops outright: it holds draft plaintext about real correspondence.
	// Before the posture is even read: a held record whose window closed is
	// an Art. 17 request the engine suspended, and completing it is not a
	// storage-limitation policy the operator may decline (retentionrestricted.go).
	if err := s.evaluateRestrictionExpiry(ctx); err != nil {
		return err
	}
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

// evaluatePolicy runs ONE policy's stage: resolve what it governs, decide whether
// it may act at all, then apply its action to a bounded batch of over-age
// records.
//
// Two conditions skip the whole stage rather than failing the pass, and they are
// the same shape: a scope with no selector, and an action with no executor for
// that object type. Both are LOUD (logged every pass) and both are unreachable
// through the authoring surface, which refuses them — so reaching either means a
// row predates the refusal or a release retired something. Skipping costs one
// policy; failing would abort the pass and take every LATER policy with it,
// nightly, until somebody found the row.
func (s *RetentionService) evaluatePolicy(ctx context.Context, pol retentionPolicy, ref time.Time) error {
	scope := pol.ObjectType + "/"
	if pol.Category != nil {
		scope += *pol.Category
	}
	selector, known := retentionSelectors[scope]
	if !known {
		s.log.Warn("retention: policy scope has no selector — skipped, not half-applied",
			"scope", scope, "policy", pol.ID)
		return nil
	}
	if !SupportsRetentionAction(pol.ObjectType, pol.Action) {
		s.log.Warn("retention: policy action has no executor for its object type — skipped, not half-applied",
			"scope", scope, "action", pol.Action, "policy", pol.ID,
			"supported", ActionsForScope(pol.ObjectType))
		return nil
	}
	retainOnly, err := s.retainOnly(ctx)
	if err != nil {
		return err
	}
	if retainOnly && isDestructive(pol.Action) {
		// Suppressed before the due-list query, so the records are never
		// selected. Not audited: audit_log records mutations and a suppression is
		// the absence of one, so the POSTURE CHANGE is the audited event and the
		// surface reports the live state as suppressed_by_posture.
		s.log.Info("retention: retain-only posture suppresses a destructive policy",
			"scope", scope, "policy", pol.ID, "action", pol.Action)
		return nil
	}
	due, err := s.dueRecords(ctx, pol, selector, ref)
	if err != nil {
		return fmt.Errorf("retention %s: select: %w", scope, err)
	}
	for _, id := range due {
		// The posture is re-read per RECORD for a destructive policy, not per
		// stage. A stage is up to retentionBatch records, each in its own
		// transaction, so a stage-level read alone let an admin who turned the
		// posture on mid-stage watch as many as 200 irreversible actions complete
		// after the promise was made. The cost of checking is one indexed read
		// against a tiny table; the cost of not checking is measured in destroyed
		// records, so the asymmetry decides it.
		if isDestructive(pol.Action) {
			held, err := s.retainOnly(ctx)
			if err != nil {
				return err
			}
			if held {
				s.log.Info("retention: retain-only posture turned on mid-pass — stopping this policy",
					"scope", scope, "policy", pol.ID, "action", pol.Action)
				return nil
			}
		}
		if err := s.apply(ctx, pol, id); err != nil {
			return fmt.Errorf("retention %s on %s: %w", scope, id, err)
		}
	}
	return nil
}

// dueRecords reads one batch of records this policy is over-age for.
//
// The activity selectors take two extra args because a destructive action on
// commercial correspondence is shielded by the statutory floor; ref anchors that
// comparison at the pass's own instant so two activity policies in one run cannot
// order mixed-unit periods differently.
func (s *RetentionService) dueRecords(ctx context.Context, pol retentionPolicy, selector string, ref time.Time) ([]ids.UUID, error) {
	args := []any{pol.RetainDays, retentionBatch}
	if pol.ObjectType == "activity" {
		floor := jurisdiction.RetentionClass{}
		if pol.Action != actionArchive {
			floor = statutoryCorrespondenceFloor(ref)
		}
		args = append(args, floor.Keep.String(), floor.Anchor == jurisdiction.AnchorCalendarYearEnd)
	}
	// The ids stay untyped: the selector's entity varies by policy scope (lead,
	// activity, person, deal), so the kind is only known one dispatch deeper.
	var due []ids.UUID
	err := s.db.Tx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, selector, args...)
		if err != nil {
			return err
		}
		due, err = pgx.CollectRows(rows, pgx.RowTo[ids.UUID])
		return err
	})
	return due, err
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
