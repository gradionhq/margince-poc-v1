// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package deals

// The deal archive, split out of deal.go when that file reached the
// file-length cap.
//
// The refusal and the write live together because they are one obligation read
// twice: what the store refuses at execution is what a confirm-first staging
// has to be able to ask BEFORE a human answers, and the two drifting apart is
// the defect the pair exists to prevent.

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// RefuseArchiveDeal answers every authority refusal ArchiveDeal would answer
// with, and writes nothing — the stage-time half of the archive, so a staged
// approval is never spent on a call the store was always going to refuse. No
// version probe: see RefuseArchiveActivity on why a pin is the write's
// business rather than a staging's.
func (s *Store) RefuseArchiveDeal(ctx context.Context, id ids.DealID) error {
	if err := auth.Require(ctx, "deal", principal.ActionDelete); err != nil {
		return err
	}
	return s.tx(ctx, func(tx pgx.Tx) error {
		return auth.EnsureWritable(ctx, tx, "deal", id.UUID)
	})
}

// ArchiveDeal retires one deal and the edges hanging off it, conditioned on
// ifVersion wherever the caller's authority named a version.
//
// The DEAL's own row rides the guarded patch; the relationship sweep below
// stays a plain statement because it is a cascade off that row rather than a
// second decision — the guard on the deal is what serializes both.
func (s *Store) ArchiveDeal(ctx context.Context, id ids.DealID, ifVersion *int64) (crmcontracts.Deal, error) {
	if err := auth.Require(ctx, "deal", principal.ActionDelete); err != nil {
		return crmcontracts.Deal{}, err
	}
	active, err := s.activeColumns(ctx)
	if err != nil {
		return crmcontracts.Deal{}, err
	}
	var out crmcontracts.Deal
	err = s.tx(ctx, func(tx pgx.Tx) error {
		if err := auth.EnsureWritable(ctx, tx, "deal", id.UUID); err != nil {
			return err
		}
		// A liveness probe, not a wire read — no custom columns needed.
		if _, err := readDeal(ctx, tx, id, storekit.LiveOnly, nil); err != nil {
			return err
		}
		now := time.Now().UTC()
		p := storekit.NewPatch()
		p.Set("archived_at", nil, now)
		if err := p.ApplyGuarded(ctx, tx, "deal", id.UUID, ifVersion); err != nil {
			return fmt.Errorf("archive deal: %w", err)
		}
		if _, err := tx.Exec(ctx,
			`UPDATE relationship SET archived_at = $2 WHERE deal_id = $1 AND archived_at IS NULL`,
			id, now); err != nil {
			return fmt.Errorf("archive the deal's relationships: %w", err)
		}
		if _, err := tx.Exec(ctx,
			`DELETE FROM list_member WHERE entity_type = 'deal' AND entity_id = $1`, id); err != nil {
			return fmt.Errorf("detach list memberships: %w", err)
		}
		if _, err := tx.Exec(ctx,
			`DELETE FROM taggable WHERE entity_type = 'deal' AND entity_id = $1`, id); err != nil {
			return fmt.Errorf("detach tags: %w", err)
		}

		auditID, err := storekit.Audit(ctx, tx, "archive", "deal", id.UUID, nil, nil)
		if err != nil {
			return fmt.Errorf("audit deal archive: %w", err)
		}
		if err := storekit.EmitEvent(ctx, tx, auditID, id.UUID, crmcontracts.PublicEventDealArchived{}); err != nil {
			return fmt.Errorf("emit deal.archived: %w", err)
		}
		if out, err = readDealForCaller(ctx, tx, id, storekit.IncludeArchived, active); err != nil {
			return fmt.Errorf("read archived deal: %w", err)
		}
		return nil
	})
	return out, err
}
