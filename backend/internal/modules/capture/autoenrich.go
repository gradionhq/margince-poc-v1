// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package capture

// The captured-organization auto-enrich sweep's store (CAP-PARAM-7,
// ADR-0072/A118): the per-org attempt cursor (capture_auto_enrich_state), the
// per-workspace daily spend cap (capture_auto_enrich_budget), and the due-org
// candidate read. Compose owns the sweep worker and the deep-read enqueue; this
// store owns the scheduling state and the atomic cap reservation so the two are
// one transaction each. The candidate read joins organization / site_read
// (people-owned) — reads are governed by RLS, not ownership — so all the sweep's
// eligibility logic lives in one place.

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// autoEnrichMaxAttempts bounds how many times the sweep re-enqueues a deep-read
// for one organization before giving up (ADR-0072: retries=2). A read that
// applied or evidenced nothing is terminal (next_attempt_at NULL) and never
// counts against this; only a failed read consumes an attempt.
const autoEnrichMaxAttempts = 2

// DueOrg is one organization the sweep should enrich: its id and the primary
// domain that seeds the crawl.
type DueOrg struct {
	OrganizationID ids.OrganizationID
	Domain         string
}

// AutoEnrichStore owns the sweep's scheduling state and daily-cap reservation.
type AutoEnrichStore struct {
	pool *pgxpool.Pool
}

// NewAutoEnrichStore builds the store over the pool.
func NewAutoEnrichStore(pool *pgxpool.Pool) *AutoEnrichStore { return &AutoEnrichStore{pool: pool} }

// ListDueOrgs returns up to limit captured organizations that need a dossier,
// newest first (ADR-0072): a domain-named org (name_source='domain' — the
// auto-created/domain-derived kind, never a human-named or already-enriched
// company) with a live primary domain, no non-failed site read yet, and either
// no cursor row or a due one under the attempt bound. RLS scopes it to the
// current workspace.
func (s *AutoEnrichStore) ListDueOrgs(ctx context.Context, limit int) ([]DueOrg, error) {
	var out []DueOrg
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT o.id, d.domain
			FROM organization o
			JOIN organization_domain d
			  ON d.organization_id = o.id AND d.is_primary AND d.archived_at IS NULL
			LEFT JOIN capture_auto_enrich_state s ON s.organization_id = o.id
			WHERE o.archived_at IS NULL
			  AND o.name_source = 'domain'
			  AND NOT EXISTS (
				SELECT 1 FROM site_read sr
				WHERE sr.organization_id = o.id AND sr.status <> 'failed')
			  AND (
				s.organization_id IS NULL
				OR (s.next_attempt_at IS NOT NULL AND s.next_attempt_at <= now()
				    AND s.attempts < $1))
			ORDER BY o.created_at DESC
			LIMIT $2`, autoEnrichMaxAttempts, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var o DueOrg
			if err := rows.Scan(&o.OrganizationID, &o.Domain); err != nil {
				return err
			}
			out = append(out, o)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("capture: listing orgs due for auto-enrich: %w", err)
	}
	return out, nil
}

// ExpireExhausted retires the cursors of orgs that have used every attempt
// without a dossier landing: it sets last_outcome='exhausted' and clears
// next_attempt_at, so the row drops out of the partial due-index (it is no
// longer re-scanned every pass) — the real termination the 'exhausted' state
// names. Called once per sweep pass, before ListDueOrgs. A resolved org already
// has a NULL next_attempt_at, so the NOT-NULL guard leaves it untouched.
func (s *AutoEnrichStore) ExpireExhausted(ctx context.Context) error {
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			UPDATE capture_auto_enrich_state SET
			  last_outcome = 'exhausted', next_attempt_at = NULL, updated_at = now()
			WHERE attempts >= $1 AND next_attempt_at IS NOT NULL`, autoEnrichMaxAttempts)
		return err
	})
	if err != nil {
		return fmt.Errorf("capture: expiring exhausted auto-enrich cursors: %w", err)
	}
	return nil
}

// ReserveBudget atomically reserves one auto-enrich slot for the current
// workspace's UTC day, returning false when the daily cap is already spent. The
// reservation is the same transaction as the counter read, so two concurrent
// sweeps (replicas) can never both slip past the cap.
func (s *AutoEnrichStore) ReserveBudget(ctx context.Context, dailyCap int) (bool, error) {
	var reserved bool
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		var enqueued int
		// INSERT the day's first slot, or increment only while under the cap;
		// the WHERE on DO UPDATE makes an over-cap increment a no-op that
		// RETURNS nothing, so the reservation is atomic.
		err := tx.QueryRow(ctx, `
			INSERT INTO capture_auto_enrich_budget (workspace_id, budget_date, enqueued)
			VALUES (NULLIF(current_setting('app.workspace_id', true), '')::uuid, (now() AT TIME ZONE 'UTC')::date, 1)
			ON CONFLICT (workspace_id, budget_date)
			DO UPDATE SET enqueued = capture_auto_enrich_budget.enqueued + 1
			WHERE capture_auto_enrich_budget.enqueued < $1
			RETURNING enqueued`, dailyCap).Scan(&enqueued)
		if err == nil {
			reserved = true
			return nil
		}
		if errors.Is(err, pgx.ErrNoRows) {
			// The DO UPDATE WHERE failed the cap guard: nothing reserved. (A
			// real error would fall through and abort the workspace pass.)
			reserved = false
			return nil
		}
		return err
	})
	if err != nil {
		return false, fmt.Errorf("capture: reserving auto-enrich budget: %w", err)
	}
	return reserved, nil
}

// MarkQueued records that the sweep enqueued a deep-read for orgID: it counts
// the attempt and arms next_attempt_at at the failure backoff, so a job that
// never completes is re-driven after the backoff, up to the attempt bound. A
// terminal outcome (MarkResolved) clears next_attempt_at.
func (s *AutoEnrichStore) MarkQueued(ctx context.Context, orgID ids.OrganizationID, backoff time.Duration) error {
	nextAttempt := time.Now().UTC().Add(backoff)
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			INSERT INTO capture_auto_enrich_state
			  (organization_id, workspace_id, attempts, last_attempt_at, next_attempt_at, last_outcome)
			VALUES ($1, NULLIF(current_setting('app.workspace_id', true), '')::uuid, 1, now(), $2, 'queued')
			ON CONFLICT (organization_id) DO UPDATE SET
			  attempts = capture_auto_enrich_state.attempts + 1,
			  last_attempt_at = now(),
			  next_attempt_at = $2,
			  last_outcome = 'queued',
			  updated_at = now()`, orgID, nextAttempt)
		return err
	})
	if err != nil {
		return fmt.Errorf("capture: marking auto-enrich queued: %w", err)
	}
	return nil
}

// MarkResolved records the terminal outcome of a deep-read the sweep triggered.
// 'applied' and 'empty' are terminal — next_attempt_at is cleared so the org is
// never re-enqueued; 'failed' leaves the queued backoff standing so the next
// due sweep retries it (until the attempt bound). A cursor row is expected
// (MarkQueued wrote it); a missing row is a no-op, never an error.
func (s *AutoEnrichStore) MarkResolved(ctx context.Context, orgID ids.OrganizationID, outcome string) error {
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			UPDATE capture_auto_enrich_state SET
			  last_outcome = $2,
			  next_attempt_at = CASE WHEN $2 IN ('applied', 'empty') THEN NULL ELSE next_attempt_at END,
			  updated_at = now()
			WHERE organization_id = $1`, orgID, outcome)
		return err
	})
	if err != nil {
		return fmt.Errorf("capture: marking auto-enrich resolved: %w", err)
	}
	return nil
}
