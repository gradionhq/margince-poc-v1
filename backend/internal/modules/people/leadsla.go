// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package people

// The lead first-response SLA (formulas §18): the deterministic clock, what
// counts as a first response, and the at-most-once breach scan. The target
// is the §18 default; the RC-5 per-workspace override is not wired here yet.

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// leadSLAClock is the clock the derived SLA fields are read against. A
// package variable rather than a Store field because the scanner that
// derives them has no store in hand; tests pin it.
var leadSLAClock = time.Now

// leadSLAFields derives the wire's sla_deadline_at and sla_state from the
// stored clock start, first response and closure (formulas §18.1) under the
// installation's policy. A closed or answered lead owes nothing and reads
// null; with the target switched off every lead does.
func leadSLAFields(policy leadSLAPolicy, routedAt *time.Time, createdAt time.Time, firstResponseAt, archivedAt *time.Time) (*time.Time, *crmcontracts.LeadSlaState) {
	if !policy.enabled || archivedAt != nil {
		return nil, nil
	}
	start := createdAt
	if routedAt != nil {
		start = *routedAt
	}
	deadline := start.Add(policy.target)
	if firstResponseAt != nil {
		return &deadline, nil
	}
	now := leadSLAClock().UTC()
	state := crmcontracts.LeadSlaStateWithinTarget
	switch {
	case now.After(deadline):
		state = crmcontracts.LeadSlaStateBreached
	case deadline.Sub(now) <= policy.atRisk():
		state = crmcontracts.LeadSlaStateAtRisk
	}
	return &deadline, &state
}

// slaStateClause renders one sla_state filter as SQL over the lead's own
// columns, with the same arithmetic leadSLAFields applies in Go: the list
// and the row must agree about which leads are overdue.
//
// The instant is the application clock, bound as a parameter, never the
// database's now(): the row's sla_state is derived against leadSLAClock, and
// a filter reading a different clock — the container's, seconds adrift, or
// the other side of a boundary crossed mid-request — would return an at_risk
// row whose own payload says breached.
//
// With the target switched off no lead is in any SLA state, so the filter
// matches nothing rather than pretending a default target.
func slaStateClause(policy leadSLAPolicy, state crmcontracts.ListLeadsParamsSlaState, arg func(any) int) string {
	if !policy.enabled {
		return "FALSE"
	}
	deadline := "COALESCE(routed_at, created_at) + $%d * interval '1 minute'"
	open := "archived_at IS NULL AND first_response_at IS NULL AND "
	minutes := policy.targetMinutes()
	now := leadSLAClock().UTC()
	switch crmcontracts.LeadSlaState(state) {
	case crmcontracts.LeadSlaStateBreached:
		return storekit.SQLf(open+deadline+" < $%d", arg(minutes), arg(now))
	case crmcontracts.LeadSlaStateAtRisk:
		return storekit.SQLf(open+deadline+" >= $%d AND "+deadline+" - $%d * interval '1 minute' <= $%d",
			arg(minutes), arg(now), arg(minutes), arg(int(policy.atRisk()/time.Minute)), arg(now))
	default:
		return storekit.SQLf(open+deadline+" - $%d * interval '1 minute' > $%d",
			arg(minutes), arg(int(policy.atRisk()/time.Minute)), arg(now))
	}
}

// firstResponseColumn is the lead's §18.1 first-response stamp.
const firstResponseColumn = "first_response_at"

// firstResponseSet is the SET fragment every disposition write carries: the
// first genuine response is recorded once and never moved.
const firstResponseSet = firstResponseColumn + ` = COALESCE(` + firstResponseColumn + `, now())`

// SLABreach is one lead whose first-response deadline passed unanswered on
// this scan — what the escalation acts on.
type SLABreach struct {
	LeadID   ids.LeadID
	OwnerID  *ids.UserID
	Deadline time.Time
	Name     string
}

// ScanLeadSLA marks every open lead whose first-response deadline has passed
// unanswered and not yet been escalated (formulas §18.2), once per breach:
// sla_breached_at is the at-most-once mark, and the row lock with SKIP
// LOCKED lets two scans share the work without escalating a lead twice.
// Each breach lands one audit row and lead.sla_breached; the escalation
// task hangs off that event.
//
// With the target switched off the scan is a no-op: no deadline exists to
// breach, and a breach row the installation never asked for would page an
// owner about a rule they did not set.
func (s *Store) ScanLeadSLA(ctx context.Context, now time.Time) ([]SLABreach, error) {
	if err := auth.Require(ctx, "lead", principal.ActionUpdate); err != nil {
		return nil, err
	}
	var breaches []SLABreach
	err := s.tx(ctx, func(tx pgx.Tx) error {
		policy, err := loadLeadSLAPolicy(ctx, tx)
		if err != nil {
			return err
		}
		if !policy.enabled {
			return nil
		}
		rows, err := tx.Query(ctx, `
			SELECT id, owner_id, COALESCE(routed_at, created_at) + $1 * interval '1 minute', COALESCE(full_name, email, '')
			FROM lead
			WHERE archived_at IS NULL AND first_response_at IS NULL AND sla_breached_at IS NULL
			  AND COALESCE(routed_at, created_at) + $1 * interval '1 minute' < $2
			ORDER BY created_at
			FOR UPDATE SKIP LOCKED`,
			policy.targetMinutes(), now)
		if err != nil {
			return fmt.Errorf("select breached leads: %w", err)
		}
		breaches, err = collectBreaches(rows)
		if err != nil {
			return err
		}
		for _, b := range breaches {
			if err := markBreach(ctx, tx, b, now); err != nil {
				return err
			}
		}
		return nil
	})
	return breaches, err
}

func collectBreaches(rows pgx.Rows) ([]SLABreach, error) {
	defer rows.Close()
	var out []SLABreach
	for rows.Next() {
		var b SLABreach
		if err := rows.Scan(&b.LeadID, &b.OwnerID, &b.Deadline, &b.Name); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

func markBreach(ctx context.Context, tx pgx.Tx, b SLABreach, now time.Time) error {
	// The row is already locked by the scan's SELECT ... FOR UPDATE; the
	// predicate is the CAS that keeps the mark at-most-once regardless.
	tag, err := tx.Exec(ctx,
		`UPDATE lead SET sla_breached_at = $2 WHERE id = $1 AND sla_breached_at IS NULL`, b.LeadID, now)
	if err != nil {
		return fmt.Errorf("mark sla breach: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return fmt.Errorf("mark sla breach on %s: %w", b.LeadID, apperrors.ErrConflict)
	}
	auditID, err := storekit.Audit(ctx, tx, "update", "lead", b.LeadID.UUID,
		nil, map[string]any{"sla_breached_at": now, "deadline": b.Deadline})
	if err != nil {
		return fmt.Errorf("audit sla breach: %w", err)
	}
	payload := crmcontracts.PublicEventLeadSlaBreached{Deadline: b.Deadline}
	if b.OwnerID != nil {
		owner := openapi_types.UUID(b.OwnerID.UUID)
		payload.OwnerId = &owner
		// Until a team-lead concept exists to resolve the §18 escalation
		// target through, the owner IS the target: the breach lands on the
		// desk that owns the lead rather than nowhere.
		payload.EscalationTarget = &owner
	}
	if err := storekit.EmitEvent(ctx, tx, auditID, b.LeadID.UUID, payload); err != nil {
		return fmt.Errorf("emit lead.sla_breached: %w", err)
	}
	return nil
}

// RecordLeadFirstResponse stamps the lead's first genuine response from an
// outbound activity (formulas §18.1) — the one first-response trigger that
// does not ride another lead write. It answers whether this call was the
// one that set it, so the caller can tell a real change from a replay.
func (s *Store) RecordLeadFirstResponse(ctx context.Context, leadID ids.LeadID, at time.Time) (bool, error) {
	if err := auth.Require(ctx, "lead", principal.ActionUpdate); err != nil {
		return false, err
	}
	set := false
	err := s.tx(ctx, func(tx pgx.Tx) error {
		lock, err := storekit.LockRow(ctx, tx, "lead", leadID.UUID, storekit.LiveOnly)
		if err != nil {
			return err
		}
		var current *time.Time
		if err := tx.QueryRow(ctx, `SELECT first_response_at FROM lead WHERE id = $1`, leadID).Scan(&current); err != nil {
			return err
		}
		// The FIRST response is the earliest one, not the first one this
		// subscriber happened to process: the bus is at-least-once and
		// unordered, so a 09:00 reply may arrive after a 10:00 one. A later
		// or equal stamp on a lead already answered is a replay and a no-op.
		if current != nil && !at.Before(*current) {
			return nil
		}
		p := storekit.NewPatch()
		p.Set(firstResponseColumn, current, at)
		if err := p.ApplyLocked(ctx, tx, lock); err != nil {
			return err
		}
		auditID, err := storekit.Audit(ctx, tx, "update", "lead", leadID.UUID,
			map[string]any{firstResponseColumn: current}, map[string]any{firstResponseColumn: at})
		if err != nil {
			return err
		}
		set = true
		return storekit.EmitEvent(ctx, tx, auditID, leadID.UUID, crmcontracts.PublicEventLeadUpdated{
			ChangedFields: map[string]any{eventKeyDelta: map[string]any{firstResponseColumn: at}},
		})
	})
	return set, err
}

// isFirstResponseActivity decides whether an outbound activity captured by
// this actor is a genuine response (formulas §18.1) rather than a
// cold-outbound auto-touch: a human's outbound always is; an agent's counts
// only when the lead had already written in — a touch with nothing to
// respond to is the anti-pollution case §2 names.
func isFirstResponseActivity(direction, capturedBy string, hadInbound bool) bool {
	if direction != "outbound" {
		return false
	}
	if strings.HasPrefix(capturedBy, string(principal.PrincipalHuman)+":") {
		return true
	}
	return hadInbound
}
