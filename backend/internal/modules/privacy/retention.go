// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package privacy

// The retention engine (data-model §3.4, ADR-0011): a nightly pass
// evaluates each workspace's enabled policies and applies the policy's
// single action to over-age records, one audited transaction per
// record. legal_hold rows are NEVER auto-acted, and an activity is
// held transitively when any linked person/organization/deal is held —
// a hold on the subject must cover the evidence about them.

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/platform/blobstore"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/jurisdiction"
)

// retentionBatch bounds how many rows one policy acts on per pass — a
// first run against years of backlog drains over successive nights
// instead of one giant transaction.
const retentionBatch = 200

// embedCallRetention bounds how long an embedding-kind ai_call trace row
// survives (spec §4), in days. Unlike the retention_policy rows above,
// this is a fixed operational cap, not an admin-editable per-workspace
// setting: ai_call carries no subject content (it is telemetry — routing,
// spend, and identity facts, never a customer's data), so its age-out is
// engine-owned hygiene, the same footing as commercialCorrespondenceFloor
// below rather than a §3.4 storage-limitation policy. Only the embedding
// kind is aged because its volume is different in kind, not in risk:
// every indexed record emits embed rows on every re-index, and past the
// spend ledger's monthly close they answer no question a completion row
// doesn't. Completion rows ARE the certification substrate (attempt
// ladders, served identity, config lineage) and stay until a spec
// retention rule says otherwise.
const embedCallRetention = 90

// RetentionService drives the evaluator; the worker ticks it nightly.
type RetentionService struct {
	pool   *pgxpool.Pool
	eraser *Eraser
	log    *slog.Logger
}

// NewRetentionService wires the nightly evaluator. blob lets its erase
// action purge attachment objects (Art. 17 reaches the bytes); pass nil in
// a deployment with no object store, where no attachment object can exist.
func NewRetentionService(pool *pgxpool.Pool, blob blobstore.Store, log *slog.Logger) *RetentionService {
	return &RetentionService{pool: pool, eraser: NewEraser(pool).WithBlobstore(blob), log: log}
}

// commercialCorrespondenceFloor is the WHERE fragment that shields commercial
// correspondence younger than the jurisdiction floor ($3) from a destructive
// action — spelled once, applied by every activity selector. Correspondence
// under GoBD §147 AO is a Handelsbrief: EXTERNAL business communication (email,
// call, meeting, whatsapp, telegram). An internal note and a task are not
// correspondence and carry no statutory floor, so their bodies fall to the
// workspace policy like any other record. That boundary is not just prose:
// TestStatutoryFloorShieldsCorrespondenceFromDestruction pins it (a 400-day
// email survives, a same-age note is erased), so flipping the classification
// fails the build. Archive passes the zero period ("P0D") because archiving
// RETAINS. $3 is an ISO 8601 date interval (jurisdiction.Period.String):
// Postgres subtracts it with calendar arithmetic, so a six-YEAR statutory
// floor is never shortened to 2190 days across leap years.
const commercialCorrespondenceFloor = `AND NOT (a.kind NOT IN ('task','note') AND a.occurred_at > now() - $3::interval)`

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
		  ` + commercialCorrespondenceFloor + `
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
		  ` + commercialCorrespondenceFloor + `
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

// Evaluate is one nightly pass over every live workspace. The unbounded
// workspace list is fine here: it is bounded by fleet size (tenants per
// install), not by tenant data volume.
func (s *RetentionService) Evaluate(ctx context.Context) error {
	// rls-exempt: fleet enumeration — the workspace table is not workspace-scoped; this reads every tenant before entering a per-workspace tx.
	rows, err := s.pool.Query(ctx, `SELECT id FROM workspace WHERE archived_at IS NULL ORDER BY created_at`)
	if err != nil {
		return err
	}
	workspaces, err := pgx.CollectRows(rows, pgx.RowTo[ids.WorkspaceID])
	if err != nil {
		return err
	}
	for _, wsID := range workspaces {
		wsCtx := principal.WithWorkspaceID(ctx, wsID.UUID)
		wsCtx = principal.WithActor(wsCtx, principal.Principal{Type: principal.PrincipalSystem, ID: "system"})
		wsCtx = principal.WithCorrelationID(wsCtx, ids.NewV7())
		if err := s.evaluateWorkspace(wsCtx); err != nil {
			// One tenant's failure must not starve the rest of the fleet.
			s.log.Error("retention: workspace pass failed", "workspace", wsID, "err", err)
		}
	}
	return nil
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

func (s *RetentionService) evaluateWorkspace(ctx context.Context) error {
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
			floor := jurisdiction.Period{}
			if pol.Action != "archive" {
				floor = statutoryCorrespondenceFloor(time.Now())
			}
			args = append(args, floor.String())
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

// evaluateVoiceSignalRetention erases the draft plaintext of over-age voice
// learning signals: the counters row survives (the learning statistics stay
// honest), the generated and final texts do not outlive their window.
func (s *RetentionService) evaluateVoiceSignalRetention(ctx context.Context) error {
	var due []ids.UUID
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT id FROM voice_learning_signal
			WHERE retention_until < now() AND content_erased_at IS NULL
			  AND (generated_original IS NOT NULL OR final_text IS NOT NULL)
			LIMIT $1`, retentionBatch)
		if err != nil {
			return err
		}
		due, err = pgx.CollectRows(rows, pgx.RowTo[ids.UUID])
		return err
	})
	if err != nil {
		return fmt.Errorf("retention voice_learning_signal: select: %w", err)
	}
	for _, id := range due {
		if err := s.eraseVoiceSignalContent(ctx, id); err != nil {
			return fmt.Errorf("retention voice_learning_signal on %s: %w", id, err)
		}
	}
	return nil
}

func (s *RetentionService) eraseVoiceSignalContent(ctx context.Context, id ids.UUID) error {
	return database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		// The content_erased_at predicate is the CAS: a rival sweep that
		// already erased this row matches zero rows, and nothing is audited
		// twice for one erasure.
		tag, err := tx.Exec(ctx, `
			UPDATE voice_learning_signal
			SET generated_original = NULL, final_text = NULL, content_erased_at = now(),
			    version = version + 1, updated_at = now()
			WHERE id = $1 AND content_erased_at IS NULL`, id)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return nil
		}
		auditID, err := storekit.AuditWithEvidence(ctx, tx, actionErase, "voice_learning_signal", id, nil, nil, map[string]any{
			evidenceKeyRetentionAction: actionErase,
		})
		if err != nil {
			return err
		}
		return storekit.Emit(ctx, tx, auditID, "retention.applied", "voice_learning_signal", id, map[string]any{
			evidenceKeyAction: actionErase,
		})
	})
}

// evaluateEmbedCallRetention erases over-age embedding-kind ai_call trace
// rows, batched and audited one record per transaction like every other
// retention action — but driven by the fixed embedCallRetention cap
// instead of a workspace's retention_policy rows, since these rows are
// engine telemetry, not a policy-configurable domain record.
func (s *RetentionService) evaluateEmbedCallRetention(ctx context.Context) error {
	var due []ids.UUID
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT id FROM ai_call
			WHERE kind = 'embedding' AND occurred_at < now() - make_interval(days => $1)
			LIMIT $2`, embedCallRetention, retentionBatch)
		if err != nil {
			return err
		}
		due, err = pgx.CollectRows(rows, pgx.RowTo[ids.UUID])
		return err
	})
	if err != nil {
		return fmt.Errorf("retention ai_call/embedding: select: %w", err)
	}
	for _, id := range due {
		if err := s.eraseEmbedCall(ctx, id); err != nil {
			return fmt.Errorf("retention ai_call/embedding on %s: %w", id, err)
		}
	}
	return nil
}

// eraseEmbedCall deletes one over-age embedding-kind ai_call row outright
// — unlike activity/erase there is no metadata half left to keep: the
// embedding trace row IS the content being aged out.
func (s *RetentionService) eraseEmbedCall(ctx context.Context, id ids.UUID) error {
	return database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `DELETE FROM ai_call WHERE id = $1`, id); err != nil {
			return err
		}
		auditID, err := storekit.AuditWithEvidence(ctx, tx, actionErase, "ai_call", id, nil, nil, map[string]any{
			evidenceKeyRetentionAction: actionErase, "retain_days": embedCallRetention,
		})
		if err != nil {
			return err
		}
		return storekit.Emit(ctx, tx, auditID, "retention.applied", "ai_call", id, map[string]any{
			evidenceKeyAction: actionErase,
		})
	})
}

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
		case "activity/erase":
			// Transcript free-text is the special-category risk; the
			// record of the meeting stays, its content goes — including any
			// attached recording/transcript file (objects first, so the
			// purge shares the person-erase durability guarantee).
			_, err = tx.Exec(ctx,
				`UPDATE activity SET body = NULL, subject = 'Erased', archived_at = coalesce(archived_at, now()) WHERE id = $1`, id)
			if err == nil {
				_, err = tx.Exec(ctx,
					`DELETE FROM embedding WHERE entity_type = 'activity' AND entity_id = $1`, id)
			}
			if err == nil {
				err = s.eraser.eraseAttachments(ctx, tx, `entity_type = 'activity' AND entity_id = $1`, id)
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
				_, err = tx.Exec(ctx,
					`DELETE FROM embedding WHERE entity_type = 'person' AND entity_id = $1`, id)
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
		return storekit.Emit(ctx, tx, auditID, "retention.applied", pol.ObjectType, id, map[string]any{
			evidenceKeyAction: pol.Action, "policy": pol.ID,
		})
	})
}

// statutoryCorrespondenceFloor is the strictest compiled-in pack's
// commercial-correspondence class — the calendar span below which a
// destructive retention action must not touch an email activity. The
// floors are calendar periods, never day counts: a Years*365 conversion
// would shorten a statutory floor across leap years and let destruction
// run early. Strictness is compared at ref (the pass's evaluation time),
// because mixed-unit periods (P6Y vs P73M) only order against an anchor.
// The zero period means no pack declares one.
func statutoryCorrespondenceFloor(ref time.Time) jurisdiction.Period {
	floor := jurisdiction.Period{}
	for _, pack := range jurisdiction.Applicable() {
		retention := pack.Retention()
		if retention == nil {
			continue
		}
		for _, class := range retention.Classes() {
			if class.Name == jurisdiction.CommercialCorrespondence && class.Keep.Cutoff(ref).Before(floor.Cutoff(ref)) {
				floor = class.Keep
			}
		}
	}
	return floor
}

// RunRetention ticks the evaluator on the worker's schedule.
func RunRetention(ctx context.Context, svc *RetentionService, interval time.Duration, log *slog.Logger) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		if err := svc.Evaluate(ctx); err != nil {
			log.Error("retention: pass failed", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
