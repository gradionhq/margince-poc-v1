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

// RetentionService drives the evaluator; the worker ticks it nightly.
type RetentionService struct {
	pool   *pgxpool.Pool
	eraser *Eraser
	log    *slog.Logger
}

func NewRetentionService(pool *pgxpool.Pool, log *slog.Logger) *RetentionService {
	return &RetentionService{pool: pool, eraser: NewEraser(pool), log: log}
}

// selectors name the records a (object_type, category) policy governs.
// The closed map is deliberate: a policy row with a scope the engine
// does not understand is skipped LOUDLY (logged every pass), never
// half-applied. Every query filters the hold column — and for
// activities, the holds of every linked record plus the jurisdiction
// packs' statutory floor ($3): a destructive action must not touch any
// commercial correspondence — every non-task activity kind (email, call,
// meeting, whatsapp, telegram, note), not email alone — younger than the
// floor; archive passes floor 0 because archiving RETAINS.
var retentionSelectors = map[string]string{
	"lead/unconverted": `SELECT id FROM lead
		WHERE status IN ('new','working') AND archived_at IS NULL AND NOT legal_hold
		  AND full_name IS DISTINCT FROM 'Anonymized Lead'
		  AND created_at < now() - make_interval(days => $1) LIMIT $2`,
	"activity/": `SELECT a.id FROM activity a
		WHERE a.archived_at IS NULL
		  AND a.occurred_at < now() - make_interval(days => $1)
		  AND NOT (a.kind <> 'task' AND a.occurred_at > now() - make_interval(days => $3))
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
		  AND NOT (a.kind <> 'task' AND a.occurred_at > now() - make_interval(days => $3))
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
}

// Evaluate is one nightly pass over every live workspace. The unbounded
// workspace list is fine here: it is bounded by fleet size (tenants per
// install), not by tenant data volume.
func (s *RetentionService) Evaluate(ctx context.Context) error {
	rows, err := s.pool.Query(ctx, `SELECT id FROM workspace WHERE archived_at IS NULL ORDER BY created_at`)
	if err != nil {
		return err
	}
	workspaces, err := pgx.CollectRows(rows, pgx.RowTo[ids.UUID])
	if err != nil {
		return err
	}
	for _, wsID := range workspaces {
		wsCtx := principal.WithWorkspaceID(ctx, wsID)
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
			floor := 0
			if pol.Action != "archive" {
				floor = statutoryCorrespondenceFloorDays()
			}
			args = append(args, floor)
		}
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
	return nil
}

// apply runs ONE action on ONE record in one audited transaction.
func (s *RetentionService) apply(ctx context.Context, pol retentionPolicy, id ids.UUID) error {
	if pol.ObjectType == "person" && pol.Action == "erase" {
		return s.eraser.ErasePerson(ctx, id, "retention")
	}
	return database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		switch pol.ObjectType + "/" + pol.Action {
		case "activity/archive":
			_, err = tx.Exec(ctx, `UPDATE activity SET archived_at = now() WHERE id = $1`, id)
		case "activity/erase":
			// Transcript free-text is the special-category risk; the
			// record of the meeting stays, its content goes.
			_, err = tx.Exec(ctx,
				`UPDATE activity SET body = NULL, subject = 'Erased', archived_at = coalesce(archived_at, now()) WHERE id = $1`, id)
			if err == nil {
				_, err = tx.Exec(ctx,
					`DELETE FROM embedding WHERE entity_type = 'activity' AND entity_id = $1`, id)
			}
		case "deal/archive":
			_, err = tx.Exec(ctx, `UPDATE deal SET archived_at = now() WHERE id = $1`, id)
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
				  title = NULL, social = '{}'::jsonb, address = NULL, raw = NULL,
				  archived_at = coalesce(archived_at, now())
				WHERE id = $1`, id, erasedName)
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
		// The audit action vocabulary is closed (0012); retention names
		// itself in evidence: archive→archive, anonymize→update,
		// erase→erase.
		auditAction := map[string]string{"archive": "archive", "anonymize": "update", "erase": "erase"}[pol.Action]
		auditID, err := storekit.Audit(ctx, tx, auditAction, pol.ObjectType, id, nil, map[string]any{
			"retention_action": pol.Action, "policy": pol.ID, "retain_days": pol.RetainDays,
		})
		if err != nil {
			return err
		}
		return storekit.Emit(ctx, tx, auditID, "retention.applied", pol.ObjectType, id, map[string]any{
			"action": pol.Action, "policy": pol.ID,
		})
	})
}

// statutoryCorrespondenceFloorDays is the strictest compiled-in pack's
// commercial-correspondence class in days — the floor below which a
// destructive retention action must not touch an email activity. Zero
// when no pack declares one.
func statutoryCorrespondenceFloorDays() int {
	floor := 0
	for _, pack := range jurisdiction.Applicable() {
		retention := pack.Retention()
		if retention == nil {
			continue
		}
		for _, class := range retention.Classes() {
			if class.Name == "commercial_correspondence" && class.Years*365 > floor {
				floor = class.Years * 365
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
