// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// The staged-line persistence seam (E03.21a): a NEW, model-free store
// entry point the compose offer-drafting orchestrator (T8) calls AFTER it
// has already talked to the model — this file never imports ai/model and
// never calls one. Every field the model resolved (description, quantity,
// price, tax, its verbatim evidence, whether the price is grounded) lands
// as one proposal_state='staged' batch; recomputeOfferTotals's existing
// staged/accepted split (offer_totals.go) keeps every one of these lines
// invisible to the offer's totals until a human accepts it through
// AcceptOfferLineItem (offer_lines.go) — this seam itself never moves a
// number the buyer can see.
//
// Provenance: offer_line_item carries no captured_by/source column (only
// the offer row does), so there is nothing here to stamp from a request
// field. Instead this mirrors people's coldstart/enrich applies and
// deals' own overnight reconciler (reconcile.go): the CALLER binds the
// acting principal to agent:offer-drafting (a system-type actor, so
// auth.Require's RBAC check is a no-op the same way approvals' effects
// are) before invoking this seam, and storekit.Audit/Emit pick that actor
// up automatically — the audit_log row this call writes carries the real
// agent identity without offer_line_item needing a column of its own.

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
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// StagedOfferLineEvidence is the verbatim conversation/document evidence
// backing one AI-drafted line (evidence-or-omit, features/07 §8b): a
// snippet plus the id of the source it came from. Both are required —
// this seam only ever persists AI-drafted lines, never a fabricated one.
type StagedOfferLineEvidence struct {
	Snippet  string
	SourceID string
}

// StagedOfferLineInput is one AI-drafted line as the drafting
// orchestrator hands it to the seam: the model (or the rate-card lookup
// it fell back to) already resolved every field below — this store adds
// no defaults and reads no product snapshot, unlike AddOfferLineItem.
type StagedOfferLineInput struct {
	Description    string
	Quantity       string // exact decimal, numeric(14,3) — see OfferLineInput
	UnitPriceMinor int64
	TaxRate        string // exact decimal, numeric(5,2)
	Evidence       StagedOfferLineEvidence
	// PriceGrounded is false only when the orchestrator could not ground a
	// price in conversation evidence or the rate card and left the line
	// at the honest zero sentinel instead of guessing (never fabricated,
	// P11) — AddStagedOfferLines enforces the pairing: false requires
	// UnitPriceMinor == 0.
	PriceGrounded bool
}

// UngroundedPriceNotZeroError maps to 422: a line claiming its price is
// NOT grounded must carry the zero sentinel — a nonzero price paired with
// that claim would mean either the flag or the price is wrong, and this
// seam never persists that ambiguity silently.
type UngroundedPriceNotZeroError struct{ UnitPriceMinor int64 }

func (e *UngroundedPriceNotZeroError) Error() string {
	return fmt.Sprintf("an ungrounded line must price at the zero sentinel, got %d minor units", e.UnitPriceMinor)
}

// AddStagedOfferLines persists a batch of AI-drafted lines as
// proposal_state='staged' in one transaction: the draft gate (ensureDraft)
// governs it exactly like every other line write, each line lands with
// its evidence and price_grounded flag, and the offer's stored totals are
// re-derived afterward — recomputeOfferTotals already excludes staged
// lines, so a batch this call adds can never move net/tax/gross. It
// returns just the inserted lines (not the whole offer): the caller
// already has the offer from the mechanical regenerate that ran first.
func (s *Store) AddStagedOfferLines(ctx context.Context, offerID ids.OfferID, lines []StagedOfferLineInput) ([]crmcontracts.OfferLineItem, error) {
	if err := auth.Require(ctx, "offer", principal.ActionUpdate); err != nil {
		return nil, err
	}
	if len(lines) == 0 {
		return nil, errors.New("deals: no staged lines to add")
	}

	var out []crmcontracts.OfferLineItem
	err := s.tx(ctx, func(tx pgx.Tx) error {
		current, _, err := visibleOfferLocked(ctx, tx, offerID, storekit.LiveOnly)
		if err != nil {
			return err
		}
		if err := ensureDraft(current); err != nil {
			return err
		}

		wsID := storekit.MustWorkspace(ctx)
		start, err := nextOfferLinePosition(ctx, tx, offerID)
		if err != nil {
			return err
		}
		inserted := make([]crmcontracts.OfferLineItem, 0, len(lines))
		for i, in := range lines {
			line, err := insertStagedOfferLine(ctx, tx, wsID, offerID, start+i, in)
			if err != nil {
				return fmt.Errorf("staged line %d: %w", i+1, err)
			}
			inserted = append(inserted, line)
		}

		if err := recomputeOfferTotals(ctx, tx, offerID); err != nil {
			return err
		}
		if _, err := storekit.Audit(ctx, tx, "update", "offer", offerID.UUID,
			nil, map[string]any{"staged_lines_added": len(lines)}); err != nil {
			return fmt.Errorf("audit staged lines add: %w", err)
		}
		out = inserted
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// insertStagedOfferLine validates one AI-drafted line, inserts it, and
// builds the returned OfferLineItem straight from the values just
// written — a second read-back would only re-derive what this function
// already knows.
func insertStagedOfferLine(ctx context.Context, tx pgx.Tx, wsID ids.UUID, offerID ids.OfferID, position int, in StagedOfferLineInput) (crmcontracts.OfferLineItem, error) {
	if in.Description == "" {
		return crmcontracts.OfferLineItem{}, &RequiredFieldError{Field: "description"}
	}
	if in.Evidence.Snippet == "" {
		return crmcontracts.OfferLineItem{}, &RequiredFieldError{Field: "evidence.snippet"}
	}
	if in.Evidence.SourceID == "" {
		return crmcontracts.OfferLineItem{}, &RequiredFieldError{Field: "evidence.source_id"}
	}
	if !in.PriceGrounded && in.UnitPriceMinor != 0 {
		return crmcontracts.OfferLineItem{}, &UngroundedPriceNotZeroError{UnitPriceMinor: in.UnitPriceMinor}
	}

	const unit, discount = "unit", "0.00"
	fig, err := LineTotals(OfferLineInput{
		Quantity: in.Quantity, UnitPriceMinor: in.UnitPriceMinor, DiscountPct: discount, TaxRate: in.TaxRate,
	})
	if err != nil {
		return crmcontracts.OfferLineItem{}, err
	}

	evidence := map[string]any{"snippet": in.Evidence.Snippet, "source_id": in.Evidence.SourceID}
	id := ids.NewV7()
	var createdAt, updatedAt time.Time
	var version int64
	err = tx.QueryRow(ctx,
		`INSERT INTO offer_line_item (id, workspace_id, offer_id, position, description, unit,
		                              quantity, unit_price_minor, discount_pct, tax_rate,
		                              evidence, price_grounded, proposal_state)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
		 RETURNING created_at, updated_at, version`,
		id, wsID, offerID, position, in.Description, unit, in.Quantity, in.UnitPriceMinor, discount, in.TaxRate,
		storekit.JSONArg(evidence), in.PriceGrounded, ProposalStaged).
		Scan(&createdAt, &updatedAt, &version)
	if err != nil {
		if storekit.IsUniqueViolation(err) {
			return crmcontracts.OfferLineItem{}, fmt.Errorf("position %d is already taken on this offer: %w", position, apperrors.ErrConflict)
		}
		return crmcontracts.OfferLineItem{}, fmt.Errorf("insert staged offer line: %w", err)
	}

	quantity, err := parseWireDecimal("quantity", in.Quantity)
	if err != nil {
		return crmcontracts.OfferLineItem{}, err
	}
	taxRate, err := parseWireDecimal("tax_rate", in.TaxRate)
	if err != nil {
		return crmcontracts.OfferLineItem{}, err
	}
	discountVal, err := parseWireDecimal("discount_pct", discount)
	if err != nil {
		return crmcontracts.OfferLineItem{}, err
	}

	grounded := in.PriceGrounded
	return crmcontracts.OfferLineItem{
		Id:             openapi_types.UUID(id),
		Position:       position,
		Description:    in.Description,
		Unit:           unit,
		Quantity:       quantity,
		UnitPriceMinor: in.UnitPriceMinor,
		DiscountPct:    discountVal,
		TaxRate:        taxRate,
		Evidence:       &evidence,
		PriceGrounded:  &grounded,
		LineNetMinor:   &fig.NetMinor,
		LineTaxMinor:   &fig.TaxMinor,
		LineTotalMinor: &fig.TotalMinor,
		Version:        &version,
		CreatedAt:      createdAt,
		UpdatedAt:      updatedAt,
	}, nil
}
