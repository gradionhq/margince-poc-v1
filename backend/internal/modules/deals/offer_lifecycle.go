// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// The offer lifecycle (B-E03.19): draft → sent → accepted/rejected, with
// regenerate minting the next revision. Send freezes the FX rate and the
// buyer/issuer snapshots — a sent offer is a fixed record; accept syncs
// the deal's headline amount from the accepted gross (the offer becomes
// the deal's value source, restoring forecast honesty). The 🟡 gate on
// send is transport policy (the contract's x-mcp-tool tier + the agent
// gate), not re-implemented here: a human's direct call IS the approval.

package deals

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// OfferNotSentError maps to 422: accept/reject/regenerate act on a SENT
// offer — a draft has not left the workspace and a terminal offer
// already has its answer.
type OfferNotSentError struct{ Status string }

func (e *OfferNotSentError) Error() string {
	return "the offer is " + e.Status + "; this transition applies to a sent offer"
}

// SendOffer runs draft → sent: freezes fx_rate_to_base as of today (422
// when the daily rate is missing — never rate=1, RT-PR-C2), captures the
// buyer/issuer snapshots and emits offer.sent. An empty offer has
// nothing to send and is refused.
func (s *Store) SendOffer(ctx context.Context, id ids.OfferID, ifVersion *int64) (crmcontracts.Offer, error) {
	if err := auth.Require(ctx, "offer", principal.ActionUpdate); err != nil {
		return crmcontracts.Offer{}, err
	}
	var out crmcontracts.Offer
	err := s.tx(ctx, func(tx pgx.Tx) error {
		current, _, err := visibleOfferLocked(ctx, tx, id, storekit.LiveOnly)
		if err != nil {
			return err
		}
		if err := ensureDraft(current); err != nil {
			return err
		}
		var lineCount int
		if err := tx.QueryRow(ctx,
			`SELECT count(*) FROM offer_line_item WHERE offer_id = $1`, id).Scan(&lineCount); err != nil {
			return fmt.Errorf("count offer lines: %w", err)
		}
		if lineCount == 0 {
			return &OfferEmptyError{}
		}

		rate, rateDate, err := freezeFx(ctx, tx, current.Currency, time.Now().UTC())
		if err != nil {
			return fmt.Errorf("freeze fx at send: %w", err)
		}
		buyer, issuer, err := sendSnapshots(ctx, tx, current)
		if err != nil {
			return err
		}

		p := storekit.NewPatch()
		p.Set("status", current.Status, "sent")
		p.Set("fx_rate_to_base", current.FxRateToBase, rate)
		p.Set("fx_rate_date", current.FxRateDate, rateDate)
		p.Set("buyer_snapshot", nil, storekit.JSONArg(buyer))
		p.Set("issuer_snapshot", nil, storekit.JSONArg(issuer))
		if err := p.ApplyGuarded(ctx, tx, "offer", id.UUID, ifVersion); err != nil {
			return fmt.Errorf("apply send transition: %w", err)
		}

		auditID, err := storekit.Audit(ctx, tx, "update", "offer", id.UUID, p.Before(), p.After())
		if err != nil {
			return fmt.Errorf("audit offer send: %w", err)
		}
		if err := storekit.Emit(ctx, tx, auditID, "offer.sent", "offer", id.UUID, map[string]any{
			"offer_id": id, "deal_id": current.DealId, "revision": current.Revision,
			"gross_minor": current.GrossMinor, "fx_rate_to_base": rate, "valid_until": current.ValidUntil,
		}); err != nil {
			return fmt.Errorf("emit offer.sent: %w", err)
		}
		if out, err = readOfferWithLines(ctx, tx, id, storekit.LiveOnly); err != nil {
			return fmt.Errorf("read sent offer: %w", err)
		}
		return nil
	})
	return out, err
}

// sendSnapshots captures the buyer and issuer legal blocks at send time:
// the sent document stays truthful even when the org or workspace is
// later renamed.
func sendSnapshots(ctx context.Context, tx pgx.Tx, offer crmcontracts.Offer) (buyer, issuer map[string]any, err error) {
	if offer.BuyerOrgId != nil {
		var displayName string
		var legalName *string
		err := tx.QueryRow(ctx,
			`SELECT display_name, legal_name FROM organization WHERE id = $1`, offer.BuyerOrgId).
			Scan(&displayName, &legalName)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return nil, nil, fmt.Errorf("snapshot buyer organization: %w", err)
		}
		if err == nil {
			buyer = map[string]any{"organization_id": offer.BuyerOrgId, "display_name": displayName}
			if legalName != nil {
				buyer["legal_name"] = *legalName
			}
		}
	}
	var wsName, baseCurrency string
	if err := tx.QueryRow(ctx,
		`SELECT name, base_currency FROM workspace WHERE id = $1`, storekit.MustWorkspace(ctx)).
		Scan(&wsName, &baseCurrency); err != nil {
		return nil, nil, fmt.Errorf("snapshot issuer workspace: %w", err)
	}
	issuer = map[string]any{"workspace_name": wsName, "base_currency": baseCurrency}
	return buyer, issuer, nil
}

// AcceptOffer runs sent → accepted: sets accepted_at, SYNCS the deal's
// amount_minor/currency from the accepted offer's gross (P12: one audit
// row; the paired deal.updated event rides the same correlation), and
// emits offer.accepted. Accepting re-prices the deal, so the caller
// needs the deal update grant as well as the offer's.
func (s *Store) AcceptOffer(ctx context.Context, id ids.OfferID, ifVersion *int64) (crmcontracts.Offer, error) {
	if err := auth.Require(ctx, "offer", principal.ActionUpdate); err != nil {
		return crmcontracts.Offer{}, err
	}
	if err := auth.Require(ctx, "deal", principal.ActionUpdate); err != nil {
		return crmcontracts.Offer{}, err
	}
	var out crmcontracts.Offer
	err := s.tx(ctx, func(tx pgx.Tx) error {
		current, _, err := visibleOfferLocked(ctx, tx, id, storekit.LiveOnly)
		if err != nil {
			return err
		}
		if current.Status != crmcontracts.OfferStatusSent {
			return &OfferNotSentError{Status: string(current.Status)}
		}

		now := time.Now().UTC()
		p := storekit.NewPatch()
		p.Set("status", current.Status, "accepted")
		p.Set("accepted_at", nil, now)
		if err := p.ApplyGuarded(ctx, tx, "offer", id.UUID, ifVersion); err != nil {
			return fmt.Errorf("apply accept transition: %w", err)
		}

		dealID := ids.From[ids.DealKind](ids.UUID(current.DealId))
		if err := syncDealAmountFromOffer(ctx, tx, dealID, current); err != nil {
			return err
		}

		auditID, err := storekit.Audit(ctx, tx, "update", "offer", id.UUID, p.Before(), map[string]any{
			"status": "accepted", "accepted_at": now,
			"deal_amount_minor": current.GrossMinor, "deal_currency": current.Currency,
		})
		if err != nil {
			return fmt.Errorf("audit offer accept: %w", err)
		}
		if err := storekit.Emit(ctx, tx, auditID, "offer.accepted", "offer", id.UUID, map[string]any{
			"offer_id": id, "deal_id": current.DealId, "revision": current.Revision,
			"gross_minor": current.GrossMinor,
		}); err != nil {
			return fmt.Errorf("emit offer.accepted: %w", err)
		}
		if err := storekit.Emit(ctx, tx, auditID, "deal.updated", "deal", dealID.UUID, map[string]any{
			"amount_minor": current.GrossMinor, "currency": current.Currency,
		}); err != nil {
			return fmt.Errorf("emit paired deal.updated: %w", err)
		}
		if out, err = readOfferWithLines(ctx, tx, id, storekit.LiveOnly); err != nil {
			return fmt.Errorf("read accepted offer: %w", err)
		}
		return nil
	})
	return out, err
}

// syncDealAmountFromOffer writes the accepted gross onto the deal. A
// still-open deal takes the amount as-is; a deal that already closed
// must re-freeze FX as of its close date or the amount change would trip
// deal_closed_fx / corrupt the frozen base-currency roll-up — the same
// invariant applyMoneyInvariants enforces on direct deal edits.
func syncDealAmountFromOffer(ctx context.Context, tx pgx.Tx, dealID ids.DealID, offer crmcontracts.Offer) error {
	// The row lock makes the status read and the amount write below one
	// race-free unit. IncludeArchived preserves the read below, which
	// follows the deal row regardless of archived state.
	if _, err := storekit.LockRow(ctx, tx, "deal", dealID.UUID, storekit.IncludeArchived); err != nil {
		return fmt.Errorf("lock deal for amount sync: %w", err)
	}
	var status string
	var closedAt *time.Time
	if err := tx.QueryRow(ctx,
		`SELECT status, closed_at FROM deal WHERE id = $1`, dealID).Scan(&status, &closedAt); err != nil {
		return fmt.Errorf("read deal for amount sync: %w", err)
	}
	if DealStatus(status) == DealOpen {
		if _, err := tx.Exec(ctx,
			`UPDATE deal SET amount_minor = $2, currency = $3 WHERE id = $1`,
			dealID, offer.GrossMinor, offer.Currency); err != nil {
			return fmt.Errorf("sync deal amount from offer: %w", err)
		}
		return nil
	}
	// deal_closed_at guarantees closedAt on a non-open row.
	rate, rateDate, err := freezeFx(ctx, tx, offer.Currency, *closedAt)
	if err != nil {
		return fmt.Errorf("re-freeze fx for closed deal on accept: %w", err)
	}
	if _, err := tx.Exec(ctx,
		`UPDATE deal SET amount_minor = $2, currency = $3, fx_rate_to_base = $4, fx_rate_date = $5 WHERE id = $1`,
		dealID, offer.GrossMinor, offer.Currency, rate, rateDate); err != nil {
		return fmt.Errorf("sync closed deal amount from offer: %w", err)
	}
	return nil
}

// RejectOffer runs sent → rejected. The optional reason rides the event
// and the audit trail; the row itself only records the state.
func (s *Store) RejectOffer(ctx context.Context, id ids.OfferID, reason *string, ifVersion *int64) (crmcontracts.Offer, error) {
	if err := auth.Require(ctx, "offer", principal.ActionUpdate); err != nil {
		return crmcontracts.Offer{}, err
	}
	var out crmcontracts.Offer
	err := s.tx(ctx, func(tx pgx.Tx) error {
		current, _, err := visibleOfferLocked(ctx, tx, id, storekit.LiveOnly)
		if err != nil {
			return err
		}
		if current.Status != crmcontracts.OfferStatusSent {
			return &OfferNotSentError{Status: string(current.Status)}
		}

		p := storekit.NewPatch()
		p.Set("status", current.Status, "rejected")
		if err := p.ApplyGuarded(ctx, tx, "offer", id.UUID, ifVersion); err != nil {
			return fmt.Errorf("apply reject transition: %w", err)
		}
		after := p.After()
		if reason != nil {
			after["reason"] = *reason
		}
		auditID, err := storekit.Audit(ctx, tx, "update", "offer", id.UUID, p.Before(), after)
		if err != nil {
			return fmt.Errorf("audit offer reject: %w", err)
		}
		payload := map[string]any{
			"offer_id": id, "deal_id": current.DealId, "revision": current.Revision,
		}
		if reason != nil {
			payload["reason"] = *reason
		}
		if err := storekit.Emit(ctx, tx, auditID, "offer.rejected", "offer", id.UUID, payload); err != nil {
			return fmt.Errorf("emit offer.rejected: %w", err)
		}
		if out, err = readOfferWithLines(ctx, tx, id, storekit.LiveOnly); err != nil {
			return fmt.Errorf("read rejected offer: %w", err)
		}
		return nil
	})
	return out, err
}

// RegenerateOffer mints revision N+1 of a SENT offer as a fresh draft —
// header and line snapshots copied verbatim — and marks the original
// superseded. A sent offer is never mutated in place (B-E03.19); the
// produced draft is a reversible internal write (🟢) that still cannot
// leave without the send gate.
func (s *Store) RegenerateOffer(ctx context.Context, id ids.OfferID) (crmcontracts.Offer, error) {
	if err := auth.Require(ctx, "offer", principal.ActionCreate); err != nil {
		return crmcontracts.Offer{}, err
	}
	if err := auth.Require(ctx, "offer", principal.ActionUpdate); err != nil {
		return crmcontracts.Offer{}, err
	}
	by, err := storekit.CapturedBy(ctx)
	if err != nil {
		return crmcontracts.Offer{}, err
	}

	var out crmcontracts.Offer
	err = s.tx(ctx, func(tx pgx.Tx) error {
		wsID := storekit.MustWorkspace(ctx)
		current, lock, err := visibleOfferLocked(ctx, tx, id, storekit.LiveOnly)
		if err != nil {
			return err
		}
		if current.Status != crmcontracts.OfferStatusSent {
			return &OfferNotSentError{Status: string(current.Status)}
		}

		// Serialize revision minting per offer number: two concurrent
		// regenerations must produce N+1 and N+2, not collide on the
		// unique (workspace, number, revision) key.
		if _, err := tx.Exec(ctx,
			`SELECT pg_advisory_xact_lock(hashtextextended('offer_revision:' || $1::text || ':' || $2, 0))`,
			wsID, *current.OfferNumber); err != nil {
			return fmt.Errorf("acquire revision lock: %w", err)
		}
		var nextRevision int
		if err := tx.QueryRow(ctx,
			`SELECT MAX(revision) + 1 FROM offer WHERE workspace_id = $1 AND offer_number = $2`,
			wsID, *current.OfferNumber).Scan(&nextRevision); err != nil {
			return fmt.Errorf("mint next revision: %w", err)
		}

		newID := ids.New[ids.OfferKind]()
		if _, err := tx.Exec(ctx,
			`INSERT INTO offer (id, workspace_id, deal_id, offer_number, revision, status, currency,
			                    buyer_org_id, valid_until, intro_text, terms_text,
			                    net_minor, tax_minor, gross_minor, source, captured_by)
			 SELECT $1, workspace_id, deal_id, offer_number, $3, 'draft', currency,
			        buyer_org_id, valid_until, intro_text, terms_text,
			        net_minor, tax_minor, gross_minor, source, $4
			 FROM offer WHERE id = $2`,
			newID, id, nextRevision, by); err != nil {
			return fmt.Errorf("copy offer into new revision: %w", err)
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO offer_line_item (id, workspace_id, offer_id, position, product_id, description,
			                              unit, quantity, unit_price_minor, discount_pct, tax_rate, evidence)
			 SELECT uuidv7(), workspace_id, $2, position, product_id, description,
			        unit, quantity, unit_price_minor, discount_pct, tax_rate, evidence
			 FROM offer_line_item WHERE offer_id = $1`,
			id, newID); err != nil {
			return fmt.Errorf("copy lines into new revision: %w", err)
		}

		supersede := storekit.NewPatch()
		supersede.Set("status", current.Status, "superseded")
		if err := supersede.ApplyLocked(ctx, tx, lock); err != nil {
			return fmt.Errorf("mark prior revision superseded: %w", err)
		}

		auditID, err := storekit.Audit(ctx, tx, "create", "offer", newID.UUID, nil, map[string]any{
			"offer_number": current.OfferNumber, "from_revision": current.Revision, "revision": nextRevision,
		})
		if err != nil {
			return fmt.Errorf("audit offer regenerate: %w", err)
		}
		if err := storekit.Emit(ctx, tx, auditID, "offer.superseded", "offer", id.UUID, map[string]any{
			"offer_id": id, "deal_id": current.DealId,
			"from_revision": current.Revision, "to_revision": nextRevision,
		}); err != nil {
			return fmt.Errorf("emit offer.superseded: %w", err)
		}
		if err := storekit.Emit(ctx, tx, auditID, "offer.created", "offer", newID.UUID, map[string]any{
			"offer_id": newID, "deal_id": current.DealId, "revision": nextRevision,
			"currency": current.Currency, "source": current.Source, "captured_by": by,
		}); err != nil {
			return fmt.Errorf("emit offer.created for new revision: %w", err)
		}
		if out, err = readOfferWithLines(ctx, tx, newID, storekit.LiveOnly); err != nil {
			return fmt.Errorf("read regenerated offer: %w", err)
		}
		return nil
	})
	return out, err
}
