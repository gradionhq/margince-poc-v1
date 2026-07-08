// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package approvals

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// redemptionTTL bounds the approve→redeem window: the human's yes is a
// judgment about the world NOW, not standing authority.
const redemptionTTL = 15 * time.Minute

// Redeem consumes one approved staging for exactly the call it was staged
// for: same tool, same diff_hash, same passport, and the target row still
// at the version the human saw. Single-use is enforced by the conditional
// UPDATE — two racing redemptions cannot both pass.
func (s *Service) Redeem(ctx context.Context, id ids.ApprovalID, tool, diffHash string) error {
	p, ok := principal.Actor(ctx)
	if !ok {
		return errors.New("crmapprovals: no actor bound to context")
	}
	return database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		a, err := get(ctx, tx, id)
		if err != nil {
			// An unknown approval id reads as an invalid token, not a 404:
			// the caller is asserting authority, not browsing.
			return fmt.Errorf("no such approval: %w", apperrors.ErrApprovalTokenInvalid)
		}
		switch {
		case a.Status != "approved":
			return fmt.Errorf("approval is %s: %w", a.effectiveStatus(s.now()), apperrors.ErrApprovalTokenInvalid)
		case a.ConsumedAt != nil:
			return fmt.Errorf("approval already redeemed: %w", apperrors.ErrApprovalTokenInvalid)
		case a.DecidedAt != nil && s.now().Sub(*a.DecidedAt) > redemptionTTL:
			return fmt.Errorf("approval expired %s after decision: %w", redemptionTTL, apperrors.ErrApprovalTokenInvalid)
		case a.Kind != tool:
			return fmt.Errorf("approval is for %s, not %s: %w", a.Kind, tool, apperrors.ErrApprovalTokenInvalid)
		case a.DiffHash != diffHash:
			return fmt.Errorf("call differs from the approved change: %w", apperrors.ErrApprovalTokenInvalid)
		case !p.PassportID.IsZero() && a.PassportID == nil:
			// AGENT token redemption (ADR-0055): a staging with no passport
			// binds to no agent, so no agent may consume it. A HUMAN inbox
			// decision is the other redeemer — it reached here through Decide
			// (human-only, decide-authority, pending→approved once), so an
			// unbound, human-staged approval is theirs to consume below.
			return fmt.Errorf("approval is not bound to a passport: %w", apperrors.ErrApprovalTokenInvalid)
		case !p.PassportID.IsZero() && *a.PassportID != ids.From[ids.PassportKind](p.PassportID):
			// An agent may only redeem the passport that staged the approval.
			return fmt.Errorf("approval was staged by a different passport: %w", apperrors.ErrApprovalTokenInvalid)
		}

		if a.TargetVersion != nil && a.TargetID != nil && a.TargetType != nil {
			current, err := targetVersion(ctx, tx, *a.TargetType, *a.TargetID)
			if err != nil {
				return err
			}
			if current != *a.TargetVersion {
				// The world moved since the human saw the diff — their yes
				// no longer covers it (ADR-0036); re-stage.
				return fmt.Errorf("target changed since approval (v%d → v%d): %w",
					*a.TargetVersion, current, apperrors.ErrVersionSkew)
			}
		}

		tag, err := tx.Exec(ctx,
			`UPDATE approval SET consumed_at = now() WHERE id = $1 AND consumed_at IS NULL`, id)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return fmt.Errorf("approval already redeemed: %w", apperrors.ErrApprovalTokenInvalid)
		}
		_, err = s.audit(ctx, tx, p, "update", id.UUID, map[string]any{"kind": a.Kind, "redeemed": true})
		return err
	})
}

// versionTables whitelists the tables a target_version re-check may read:
// every versioned entity type a staging can target under its own table
// name. A type outside this set (e.g. the partner extension, which
// audits on its organization row) cannot be version-pinned — stagers
// must leave TargetVersion nil for it rather than mint a pin redemption
// could never verify.
var versionTables = map[string]bool{
	"person": true, "organization": true, "deal": true, "lead": true, "activity": true,
	"offer": true, "product": true, "list": true, "tag": true, "relationship": true,
}

// TargetVersionCheckable reports whether a staged approval against this
// entity type can carry a target_version pin that Redeem is able to
// re-verify (ADR-0036 §2).
func TargetVersionCheckable(entityType string) bool {
	return versionTables[entityType]
}

func targetVersion(ctx context.Context, tx pgx.Tx, table string, id ids.UUID) (int64, error) {
	if !versionTables[table] {
		return 0, fmt.Errorf("crmapprovals: %q is not a versioned target", table)
	}
	var v int64
	err := tx.QueryRow(ctx, `SELECT version FROM `+table+` WHERE id = $1`, id).Scan(&v)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, apperrors.ErrNotFound
	}
	return v, err
}
