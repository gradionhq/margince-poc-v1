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
	openapi_types "github.com/oapi-codegen/runtime/types"

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
		if err := storekit.EmitEvent(ctx, tx, auditID, id.UUID, offerSentPayload(current, rate)); err != nil {
			return fmt.Errorf("emit offer.sent: %w", err)
		}
		if out, err = readOfferWithLines(ctx, tx, id, storekit.LiveOnly); err != nil {
			return fmt.Errorf("read sent offer: %w", err)
		}
		return nil
	})
	return out, err
}

// offerSentPayload builds the offer.sent wire payload from the pre-send
// offer snapshot and the FX rate frozen for this send — the ONE place that
// maps SendOffer's local values onto the published schema, so a future
// field rename shows up here (and at its call site) rather than at a
// map literal that drifts silently from the schema.
func offerSentPayload(current crmcontracts.Offer, rate string) crmcontracts.PublicEventOfferSent {
	return crmcontracts.PublicEventOfferSent{
		OfferId:      current.Id,
		DealId:       current.DealId,
		Revision:     current.Revision,
		GrossMinor:   current.GrossMinor,
		FxRateToBase: rate,
		ValidUntil:   current.ValidUntil,
	}
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
		dealChanged, err := syncDealAmountFromOffer(ctx, tx, dealID, current)
		if err != nil {
			return err
		}

		auditID, err := storekit.Audit(ctx, tx, "update", "offer", id.UUID, p.Before(), map[string]any{
			"status": "accepted", "accepted_at": now,
			"deal_amount_minor": current.GrossMinor, "deal_currency": current.Currency,
		})
		if err != nil {
			return fmt.Errorf("audit offer accept: %w", err)
		}
		if err := storekit.EmitEvent(ctx, tx, auditID, id.UUID, crmcontracts.PublicEventOfferAccepted{
			OfferId: current.Id, DealId: current.DealId, Revision: current.Revision,
			GrossMinor: current.GrossMinor,
		}); err != nil {
			return fmt.Errorf("emit offer.accepted: %w", err)
		}
		// The paired deal.updated carries the FULL set of deal columns the sync
		// actually wrote — including the re-frozen fx_rate_to_base/fx_rate_date
		// on a closed deal — so a subscriber never retains stale
		// base-currency state.
		if err := storekit.EmitEvent(ctx, tx, auditID, dealID.UUID, crmcontracts.PublicEventDealUpdated{
			ChangedFields: dealChanged,
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
// It returns the deal columns the sync actually wrote, so the caller's paired
// deal.updated reports the complete delta — on a closed deal that includes the
// re-frozen fx_rate_to_base/fx_rate_date, not just amount_minor/currency.
func syncDealAmountFromOffer(ctx context.Context, tx pgx.Tx, dealID ids.DealID, offer crmcontracts.Offer) (map[string]any, error) {
	// The row lock makes the status read and the amount write below one
	// race-free unit. IncludeArchived preserves the read below, which
	// follows the deal row regardless of archived state.
	if _, err := storekit.LockRow(ctx, tx, "deal", dealID.UUID, storekit.IncludeArchived); err != nil {
		return nil, fmt.Errorf("lock deal for amount sync: %w", err)
	}
	var status string
	var closedAt *time.Time
	if err := tx.QueryRow(ctx,
		`SELECT status, closed_at FROM deal WHERE id = $1`, dealID).Scan(&status, &closedAt); err != nil {
		return nil, fmt.Errorf("read deal for amount sync: %w", err)
	}
	changed := map[string]any{"amount_minor": offer.GrossMinor, "currency": offer.Currency}
	if DealStatus(status) == DealOpen {
		if _, err := tx.Exec(ctx,
			`UPDATE deal SET amount_minor = $2, currency = $3 WHERE id = $1`,
			dealID, offer.GrossMinor, offer.Currency); err != nil {
			return nil, fmt.Errorf("sync deal amount from offer: %w", err)
		}
		return changed, nil
	}
	// deal_closed_at guarantees closedAt on a non-open row.
	rate, rateDate, err := freezeFx(ctx, tx, offer.Currency, *closedAt)
	if err != nil {
		return nil, fmt.Errorf("re-freeze fx for closed deal on accept: %w", err)
	}
	if _, err := tx.Exec(ctx,
		`UPDATE deal SET amount_minor = $2, currency = $3, fx_rate_to_base = $4, fx_rate_date = $5 WHERE id = $1`,
		dealID, offer.GrossMinor, offer.Currency, rate, rateDate); err != nil {
		return nil, fmt.Errorf("sync closed deal amount from offer: %w", err)
	}
	changed["fx_rate_to_base"] = rate
	changed["fx_rate_date"] = rateDate
	return changed, nil
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
		payload := crmcontracts.PublicEventOfferRejected{
			OfferId: current.Id, DealId: current.DealId, Revision: current.Revision,
		}
		if reason != nil {
			payload.Reason = reason
		}
		if err := storekit.EmitEvent(ctx, tx, auditID, id.UUID, payload); err != nil {
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

		nextRevision, err := nextOfferRevision(ctx, tx, wsID, *current.OfferNumber)
		if err != nil {
			return err
		}

		newID := ids.New[ids.OfferKind]()
		if err := copyOfferIntoRevision(ctx, tx, id, newID, nextRevision, by); err != nil {
			return err
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
		if err := storekit.EmitEvent(ctx, tx, auditID, id.UUID, crmcontracts.PublicEventOfferSuperseded{
			OfferId: current.Id, DealId: current.DealId,
			FromRevision: current.Revision, ToRevision: nextRevision,
		}); err != nil {
			return fmt.Errorf("emit offer.superseded: %w", err)
		}
		if err := storekit.EmitEvent(ctx, tx, auditID, newID.UUID, crmcontracts.PublicEventOfferCreated{
			OfferId: openapi_types.UUID(newID.UUID), DealId: current.DealId, Revision: nextRevision,
			Currency: current.Currency, Source: current.Source, CapturedBy: by,
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

// nextOfferRevision mints revision N+1 for one offer number. Serialize
// the mint per offer number: two concurrent regenerations must produce
// N+1 and N+2, not collide on the unique (workspace, number, revision)
// key.
func nextOfferRevision(ctx context.Context, tx pgx.Tx, wsID ids.UUID, offerNumber string) (int, error) {
	if _, err := tx.Exec(ctx,
		`SELECT pg_advisory_xact_lock(hashtextextended('offer_revision:' || $1::text || ':' || $2, 0))`,
		wsID, offerNumber); err != nil {
		return 0, fmt.Errorf("acquire revision lock: %w", err)
	}
	var nextRevision int
	if err := tx.QueryRow(ctx,
		`SELECT MAX(revision) + 1 FROM offer WHERE workspace_id = $1 AND offer_number = $2`,
		wsID, offerNumber).Scan(&nextRevision); err != nil {
		return 0, fmt.Errorf("mint next revision: %w", err)
	}
	return nextRevision, nil
}

// copyOfferIntoRevision clones the sent offer's header and line
// snapshots verbatim into the new draft revision — the copy IS the
// snapshot semantics: nothing is re-derived from today's products.
func copyOfferIntoRevision(ctx context.Context, tx pgx.Tx, fromID, newID ids.OfferID, nextRevision int, by string) error {
	if _, err := tx.Exec(ctx,
		`INSERT INTO offer (id, workspace_id, deal_id, offer_number, revision, status, currency,
		                    buyer_org_id, valid_until, intro_text, terms_text,
		                    net_minor, tax_minor, gross_minor, source, captured_by)
		 SELECT $1, workspace_id, deal_id, offer_number, $3, 'draft', currency,
		        buyer_org_id, valid_until, intro_text, terms_text,
		        net_minor, tax_minor, gross_minor, source, $4
		 FROM offer WHERE id = $2`,
		newID, fromID, nextRevision, by); err != nil {
		return fmt.Errorf("copy offer into new revision: %w", err)
	}
	// proposal_state travels with the line: a still-staged proposal must
	// not silently become accepted (and start counting toward totals)
	// just because the offer grew a revision.
	if _, err := tx.Exec(ctx,
		`INSERT INTO offer_line_item (id, workspace_id, offer_id, position, product_id, description,
		                              unit, quantity, unit_price_minor, discount_pct, tax_rate, evidence, proposal_state, price_grounded)
		 SELECT uuidv7(), workspace_id, $2, position, product_id, description,
		        unit, quantity, unit_price_minor, discount_pct, tax_rate, evidence, proposal_state, price_grounded
		 FROM offer_line_item WHERE offer_id = $1`,
		fromID, newID); err != nil {
		return fmt.Errorf("copy lines into new revision: %w", err)
	}
	return nil
}
