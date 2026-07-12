// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package deals

// The deal partial-update entry points: the store-opened (UpdateDeal) and
// caller-opened (UpdateDealTx) variants plus their shared transactional
// body. The patch-building and money-invariant helpers this body calls
// live in deal.go. Split out to keep each file one concept under the
// 500-LOC cap.

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
	"github.com/gradionhq/margince/backend/internal/shared/ports/fieldcatalog"
)

// UpdateDealInput is one deal partial update: every field is optional, and
// CustomFields carries the request body's extra top-level keys.
type UpdateDealInput struct {
	Name                  *string
	AmountMinor           *int64
	Currency              *string
	OrganizationID        *ids.OrganizationID
	OwnerID               *ids.UserID
	PartnerOrganizationID *ids.OrganizationID
	ExpectedClose         *time.Time
	ForecastCategory      *string
	WaitUntil             *time.Time
	IfVersion             *int64
	// CustomFields carries the request body's extra top-level keys
	// (additionalProperties); only active cf_* catalog columns land,
	// drop-on-mismatch (storekit customcolumns).
	CustomFields map[string]any
}

// UpdateDeal applies a partial update inside the store's own transaction —
// the ordinary CRUD entry point (Handlers→Store). Use UpdateDealTx when the
// write must share a caller-opened transaction.
func (s *Store) UpdateDeal(ctx context.Context, id ids.DealID, in UpdateDealInput) (crmcontracts.Deal, error) {
	if err := auth.Require(ctx, "deal", principal.ActionUpdate); err != nil {
		return crmcontracts.Deal{}, err
	}
	active, err := s.activeColumns(ctx)
	if err != nil {
		return crmcontracts.Deal{}, err
	}
	var out crmcontracts.Deal
	err = s.tx(ctx, func(tx pgx.Tx) error {
		var err error
		out, err = updateDealInTx(ctx, tx, id, in, active)
		return err
	})
	return out, err
}

// UpdateDealTx is UpdateDeal's transaction-accepting variant (the C5
// shared-tx shape SeedWorkspaceDefaultsTx pioneered): a caller that must
// commit the deal write atomically with a sibling module's own write (the
// extraction accept-write's per-field notes, compose/extractionaccept.go)
// drives it inside the ONE transaction it already opened, so a note
// failure rolls the deal update back too, instead of UpdateDeal opening
// (and committing) a second transaction of its own.
//
// activeColumns runs here BEFORE touching tx, per its own documented rule
// (never nested inside an open transaction — a second pool acquire while
// the caller's tx holds one connection is a deadlock shape under load).
// This holds only because every UpdateDealTx caller today builds its
// Store with NewStore (no WithFieldCatalog), so activeColumns
// short-circuits to (nil, nil) with no pool access; a future caller that
// wires a catalog here must fetch active columns itself, before opening
// its tx, and thread them through — exactly as UpdateDeal does.
func (s *Store) UpdateDealTx(ctx context.Context, tx pgx.Tx, id ids.DealID, in UpdateDealInput) (crmcontracts.Deal, error) {
	if err := auth.Require(ctx, "deal", principal.ActionUpdate); err != nil {
		return crmcontracts.Deal{}, err
	}
	active, err := s.activeColumns(ctx)
	if err != nil {
		return crmcontracts.Deal{}, err
	}
	return updateDealInTx(ctx, tx, id, in, active)
}

// updateDealInTx is UpdateDeal's transactional body, shared by the
// store-opened (UpdateDeal) and caller-opened (UpdateDealTx) entry points.
func updateDealInTx(ctx context.Context, tx pgx.Tx, id ids.DealID, in UpdateDealInput, active []fieldcatalog.Column) (crmcontracts.Deal, error) {
	if err := auth.EnsureVisible(ctx, tx, "deal", id.UUID); err != nil {
		return crmcontracts.Deal{}, err
	}
	// current reads WITH active columns so the patch's audit before-image
	// carries the honest pre-update cf values.
	current, err := readDeal(ctx, tx, id, storekit.LiveOnly, active)
	if err != nil {
		return crmcontracts.Deal{}, fmt.Errorf("read deal before update: %w", err)
	}

	p, err := dealUpdatePatch(ctx, tx, current, in)
	if err != nil {
		return crmcontracts.Deal{}, err
	}
	storekit.SetCustomFieldPatch(p, active, in.CustomFields, current.AdditionalProperties)
	if p.Empty() {
		return current, nil
	}

	if err := applyMoneyInvariants(ctx, tx, current, in, p); err != nil {
		return crmcontracts.Deal{}, err
	}

	if err := p.ApplyGuarded(ctx, tx, "deal", id.UUID, in.IfVersion); err != nil {
		return crmcontracts.Deal{}, fmt.Errorf("apply deal patch: %w", err)
	}
	if err := recordDealUpdate(ctx, tx, id, current, in, p); err != nil {
		return crmcontracts.Deal{}, err
	}
	out, err := readDeal(ctx, tx, id, storekit.LiveOnly, active)
	if err != nil {
		return crmcontracts.Deal{}, fmt.Errorf("read updated deal: %w", err)
	}
	return out, nil
}
