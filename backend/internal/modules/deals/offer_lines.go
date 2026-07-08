// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// The offer's typed line items (B-E03.17): each line snapshots its
// price/description/tax from the product ONCE at insert — a later product
// edit never touches the line — and every line mutation re-derives the
// stored offer totals inside its transaction. Lines are editable only
// while the offer is a draft (ensureDraft, in offer.go); the offer
// aggregate and its lifecycle live in offer.go / offer_lifecycle.go.

package deals

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// ProductCurrencyMismatchError maps to 422: a line snapshots its price
// from a product priced in another currency — converting silently would
// fabricate a number (P11).
type ProductCurrencyMismatchError struct{ Product, Offer string }

func (e *ProductCurrencyMismatchError) Error() string {
	return "product is priced in " + e.Product + " but the offer is in " + e.Offer +
		"; give the line an explicit unit_price_minor in the offer currency"
}

// OfferLineInputRow is one line as the store consumes it: decimals as
// exact strings (formatted once at the transport edge), price in minor
// units, with product-snapshot defaults resolved here.
type OfferLineInputRow struct {
	Position       *int
	ProductID      *ids.ProductID
	Description    *string
	Unit           *string
	Quantity       string
	UnitPriceMinor *int64
	DiscountPct    *string
	TaxRate        *string
}

// insertOfferLines inserts each input line in order, numbering the 422
// error by the caller's 1-based line position.
func insertOfferLines(ctx context.Context, tx pgx.Tx, wsID ids.UUID, offerID ids.OfferID, currency string, lines []OfferLineInputRow) error {
	for i, line := range lines {
		if err := insertOfferLine(ctx, tx, wsID, offerID, currency, line); err != nil {
			return fmt.Errorf("line %d: %w", i+1, err)
		}
	}
	return nil
}

// insertOfferLine resolves the product snapshot (description, unit,
// price, tax default) and validates the resulting line before it lands.
// The snapshot is copied ONCE, here — a later product edit never touches
// the line (B-E03.17).
// lineSnapshotDefaults carries a line's description/unit/price/tax as the
// caller supplied them; a nil field falls back to the product snapshot
// (resolveProductSnapshot) or a stored default (normalizeLineDefaults).
type lineSnapshotDefaults struct {
	Description *string
	Unit        *string
	Price       *int64
	TaxRate     *string
}

// resolvedOfferLine is a fully-resolved line ready to insert: defaults
// applied and money already validated.
type resolvedOfferLine struct {
	Description string
	Unit        string
	Price       int64
	Discount    string
	Tax         string
}

func insertOfferLine(ctx context.Context, tx pgx.Tx, wsID ids.UUID, offerID ids.OfferID, offerCurrency string, in OfferLineInputRow) error {
	defaults, err := resolveProductSnapshot(ctx, tx, in.ProductID, offerCurrency, lineSnapshotDefaults{
		Description: in.Description, Unit: in.Unit, Price: in.UnitPriceMinor, TaxRate: in.TaxRate,
	})
	if err != nil {
		return err
	}
	if defaults.Description == nil || *defaults.Description == "" {
		return &RequiredFieldError{Field: "description"}
	}
	if defaults.Price == nil {
		return &RequiredFieldError{Field: "unit_price_minor"}
	}
	unitVal, discount, tax := normalizeLineDefaults(defaults.Unit, in.DiscountPct, defaults.TaxRate)
	// Validate the money math before the row lands: a malformed decimal
	// or a nonsense quantity answers 422 here, not a CHECK 500 later.
	if _, err := LineTotals(OfferLineInput{
		Quantity: in.Quantity, UnitPriceMinor: *defaults.Price, DiscountPct: discount, TaxRate: tax,
	}); err != nil {
		return err
	}
	return insertOfferLineRow(ctx, tx, wsID, offerID, in, resolvedOfferLine{
		Description: *defaults.Description, Unit: unitVal, Price: *defaults.Price, Discount: discount, Tax: tax,
	})
}

// resolveProductSnapshot fills a line's description/unit/price/tax defaults
// from its product; a line with no product keeps the caller's values. The
// snapshot is copied ONCE, here — a later product edit never touches the
// line (B-E03.17). The rate-card price only carries over when the
// currencies agree; a silent conversion would fabricate a number.
func resolveProductSnapshot(ctx context.Context, tx pgx.Tx, productID *ids.ProductID, offerCurrency string, in lineSnapshotDefaults) (lineSnapshotDefaults, error) {
	if productID == nil {
		return in, nil
	}
	var pName, pUnit, pCurrency, pTax string
	var pPrice int64
	err := tx.QueryRow(ctx,
		`SELECT name, unit, currency, default_tax_rate::text, unit_price_minor
		 FROM product WHERE id = $1 AND archived_at IS NULL`, *productID).
		Scan(&pName, &pUnit, &pCurrency, &pTax, &pPrice)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return lineSnapshotDefaults{}, apperrors.ErrNotFound
		}
		return lineSnapshotDefaults{}, fmt.Errorf("read product for snapshot: %w", err)
	}
	if in.Description == nil {
		in.Description = &pName
	}
	if in.Unit == nil {
		in.Unit = &pUnit
	}
	if in.Price == nil {
		if pCurrency != offerCurrency {
			return lineSnapshotDefaults{}, &ProductCurrencyMismatchError{Product: pCurrency, Offer: offerCurrency}
		}
		in.Price = &pPrice
	}
	if in.TaxRate == nil {
		in.TaxRate = &pTax
	}
	return in, nil
}

// normalizeLineDefaults resolves unit/discount/tax to their stored defaults
// when neither the caller nor the product snapshot supplied a value.
func normalizeLineDefaults(unit, discountPct, taxRate *string) (unitVal, discount, tax string) {
	unitVal = "unit"
	if unit != nil && *unit != "" {
		unitVal = *unit
	}
	discount = "0.00"
	if discountPct != nil {
		discount = *discountPct
	}
	tax = "0.00"
	if taxRate != nil {
		tax = *taxRate
	}
	return unitVal, discount, tax
}

// insertOfferLineRow assigns the line's position (appending after the last
// when unset) and inserts it, mapping a position collision to 409.
func insertOfferLineRow(ctx context.Context, tx pgx.Tx, wsID ids.UUID, offerID ids.OfferID, in OfferLineInputRow, line resolvedOfferLine) error {
	position := in.Position
	if position == nil {
		var next int
		if err := tx.QueryRow(ctx,
			`SELECT COALESCE(MAX(position), 0) + 1 FROM offer_line_item WHERE offer_id = $1`, offerID).
			Scan(&next); err != nil {
			return fmt.Errorf("next line position: %w", err)
		}
		position = &next
	}
	// note: an offer_line_item is not a first-class entity (no LineItemKind),
	// so its row id stays an untyped ids.UUID.
	_, err := tx.Exec(ctx,
		`INSERT INTO offer_line_item (id, workspace_id, offer_id, position, product_id, description,
		                              unit, quantity, unit_price_minor, discount_pct, tax_rate)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`,
		ids.NewV7(), wsID, offerID, *position, in.ProductID, line.Description,
		line.Unit, in.Quantity, line.Price, line.Discount, line.Tax)
	if err != nil {
		if storekit.IsUniqueViolation(err) {
			return fmt.Errorf("position %d is already taken on this offer: %w", *position, apperrors.ErrConflict)
		}
		return fmt.Errorf("insert offer line: %w", err)
	}
	return nil
}

func (s *Store) AddOfferLineItem(ctx context.Context, offerID ids.OfferID, in OfferLineInputRow) (crmcontracts.Offer, error) {
	if err := auth.Require(ctx, "offer", principal.ActionUpdate); err != nil {
		return crmcontracts.Offer{}, err
	}
	var out crmcontracts.Offer
	err := s.tx(ctx, func(tx pgx.Tx) error {
		current, _, err := visibleOfferLocked(ctx, tx, offerID, storekit.LiveOnly)
		if err != nil {
			return err
		}
		if err := ensureDraft(current); err != nil {
			return err
		}
		if err := insertOfferLine(ctx, tx, storekit.MustWorkspace(ctx), offerID, current.Currency, in); err != nil {
			return err
		}
		if err := recomputeOfferTotals(ctx, tx, offerID); err != nil {
			return err
		}
		if _, err := storekit.Audit(ctx, tx, "update", "offer", offerID.UUID,
			nil, map[string]any{"line_added": true}); err != nil {
			return fmt.Errorf("audit line add: %w", err)
		}
		if out, err = readOfferWithLines(ctx, tx, offerID, storekit.LiveOnly); err != nil {
			return fmt.Errorf("read offer after line add: %w", err)
		}
		return nil
	})
	return out, err
}

type UpdateOfferLineInput struct {
	Position       *int
	Description    *string
	Unit           *string
	Quantity       *string
	UnitPriceMinor *int64
	DiscountPct    *string
	TaxRate        *string
}

// note: lineID is an offer_line_item row id; it has no first-class
// entity kind (LineItemKind), so it stays an untyped ids.UUID.
func (s *Store) UpdateOfferLineItem(ctx context.Context, offerID ids.OfferID, lineID ids.UUID, in UpdateOfferLineInput) (crmcontracts.Offer, error) {
	if err := auth.Require(ctx, "offer", principal.ActionUpdate); err != nil {
		return crmcontracts.Offer{}, err
	}
	var out crmcontracts.Offer
	err := s.tx(ctx, func(tx pgx.Tx) error {
		current, _, err := visibleOfferLocked(ctx, tx, offerID, storekit.LiveOnly)
		if err != nil {
			return err
		}
		if err := ensureDraft(current); err != nil {
			return err
		}

		curPosition, curDescription, curUnit, line, err := readOfferLineForUpdate(ctx, tx, offerID, lineID)
		if err != nil {
			return err
		}

		sets, args, before, after, line := buildOfferLinePatch(lineID, in, curPosition, curDescription, curUnit, line)
		// Validate the resulting line's math up front (422, not a CHECK 500).
		if _, err := LineTotals(line); err != nil {
			return err
		}
		if len(sets) == 0 {
			out, err = readOfferWithLines(ctx, tx, offerID, storekit.LiveOnly)
			return err
		}
		if _, err := tx.Exec(ctx,
			fmt.Sprintf(`UPDATE offer_line_item SET %s WHERE id = $1`, strings.Join(sets, ", ")),
			args...); err != nil {
			if storekit.IsUniqueViolation(err) {
				return fmt.Errorf("position is already taken on this offer: %w", apperrors.ErrConflict)
			}
			return fmt.Errorf("apply line patch: %w", err)
		}
		if err := recomputeOfferTotals(ctx, tx, offerID); err != nil {
			return err
		}
		if _, err := storekit.Audit(ctx, tx, "update", "offer", offerID.UUID, before, after); err != nil {
			return fmt.Errorf("audit line update: %w", err)
		}
		if out, err = readOfferWithLines(ctx, tx, offerID, storekit.LiveOnly); err != nil {
			return fmt.Errorf("read offer after line update: %w", err)
		}
		return nil
	})
	return out, err
}

// readOfferLineForUpdate loads the line's current snapshot for a patch;
// a missing line (or one on another offer) is 404, not a fault.
func readOfferLineForUpdate(ctx context.Context, tx pgx.Tx, offerID ids.OfferID, lineID ids.UUID) (curPosition int, curDescription, curUnit string, line OfferLineInput, err error) {
	err = tx.QueryRow(ctx,
		`SELECT position, description, unit, quantity::text, unit_price_minor, discount_pct::text, tax_rate::text
		 FROM offer_line_item WHERE id = $1 AND offer_id = $2`, lineID, offerID).
		Scan(&curPosition, &curDescription, &curUnit, &line.Quantity, &line.UnitPriceMinor, &line.DiscountPct, &line.TaxRate)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, "", "", OfferLineInput{}, apperrors.ErrNotFound
	}
	if err != nil {
		return 0, "", "", OfferLineInput{}, fmt.Errorf("read offer line: %w", err)
	}
	return curPosition, curDescription, curUnit, line, nil
}

// buildOfferLinePatch folds the caller's sparse line edit into a
// hand-built patch. storekit's Patch targets archivable rows (its WHERE
// carries archived_at IS NULL) and a line lives or dies with its offer
// instead — the draft gate is the mutability rule here. It returns the
// SET fragments and args ($1 is the line id), the before/after audit
// maps, and the resulting line with the edits applied for validation.
func buildOfferLinePatch(lineID ids.UUID, in UpdateOfferLineInput, curPosition int, curDescription, curUnit string, line OfferLineInput) (sets []string, args []any, before, after map[string]any, validated OfferLineInput) {
	before, after = map[string]any{}, map[string]any{}
	sets, args = []string{}, []any{lineID}
	set := func(column string, oldVal, newVal any) {
		args = append(args, newVal)
		sets = append(sets, fmt.Sprintf("%s = $%d", column, len(args)))
		before[column], after[column] = oldVal, newVal
	}
	if in.Position != nil {
		set("position", curPosition, *in.Position)
	}
	if in.Description != nil {
		set("description", curDescription, *in.Description)
	}
	if in.Unit != nil {
		set("unit", curUnit, *in.Unit)
	}
	if in.Quantity != nil {
		set("quantity", line.Quantity, *in.Quantity)
		line.Quantity = *in.Quantity
	}
	if in.UnitPriceMinor != nil {
		set("unit_price_minor", line.UnitPriceMinor, *in.UnitPriceMinor)
		line.UnitPriceMinor = *in.UnitPriceMinor
	}
	if in.DiscountPct != nil {
		set("discount_pct", line.DiscountPct, *in.DiscountPct)
		line.DiscountPct = *in.DiscountPct
	}
	if in.TaxRate != nil {
		set("tax_rate", line.TaxRate, *in.TaxRate)
		line.TaxRate = *in.TaxRate
	}
	return sets, args, before, after, line
}

// AcceptOfferLineItem flips a staged (AI-proposed) line to accepted, at
// which point it starts counting toward the offer totals (E03.21a). The
// contract does not expose proposal_state yet, so this is the store seam
// the future drafting surface confirms through; accepting an already
// accepted line is a no-op because the desired end-state already holds.
func (s *Store) AcceptOfferLineItem(ctx context.Context, offerID ids.OfferID, lineID ids.UUID) (crmcontracts.Offer, error) {
	if err := auth.Require(ctx, "offer", principal.ActionUpdate); err != nil {
		return crmcontracts.Offer{}, err
	}
	var out crmcontracts.Offer
	err := s.tx(ctx, func(tx pgx.Tx) error {
		current, _, err := visibleOfferLocked(ctx, tx, offerID, storekit.LiveOnly)
		if err != nil {
			return err
		}
		if err := ensureDraft(current); err != nil {
			return err
		}
		tag, err := tx.Exec(ctx,
			`UPDATE offer_line_item SET proposal_state = $3 WHERE id = $1 AND offer_id = $2 AND proposal_state = $4`,
			lineID, offerID, ProposalAccepted, ProposalStaged)
		if err != nil {
			return fmt.Errorf("accept offer line: %w", err)
		}
		if tag.RowsAffected() == 0 {
			// Missing line is 404; an existing accepted one is idempotent.
			var exists bool
			if err := tx.QueryRow(ctx,
				`SELECT EXISTS (SELECT 1 FROM offer_line_item WHERE id = $1 AND offer_id = $2)`,
				lineID, offerID).Scan(&exists); err != nil {
				return fmt.Errorf("probe offer line: %w", err)
			}
			if !exists {
				return apperrors.ErrNotFound
			}
			out, err = readOfferWithLines(ctx, tx, offerID, storekit.LiveOnly)
			return err
		}
		if err := recomputeOfferTotals(ctx, tx, offerID); err != nil {
			return err
		}
		if _, err := storekit.Audit(ctx, tx, "update", "offer", offerID.UUID,
			map[string]any{"line_id": lineID, "proposal_state": ProposalStaged},
			map[string]any{"line_id": lineID, "proposal_state": ProposalAccepted}); err != nil {
			return fmt.Errorf("audit line accept: %w", err)
		}
		if out, err = readOfferWithLines(ctx, tx, offerID, storekit.LiveOnly); err != nil {
			return fmt.Errorf("read offer after line accept: %w", err)
		}
		return nil
	})
	return out, err
}

func (s *Store) RemoveOfferLineItem(ctx context.Context, offerID ids.OfferID, lineID ids.UUID) (crmcontracts.Offer, error) {
	if err := auth.Require(ctx, "offer", principal.ActionUpdate); err != nil {
		return crmcontracts.Offer{}, err
	}
	var out crmcontracts.Offer
	err := s.tx(ctx, func(tx pgx.Tx) error {
		current, _, err := visibleOfferLocked(ctx, tx, offerID, storekit.LiveOnly)
		if err != nil {
			return err
		}
		if err := ensureDraft(current); err != nil {
			return err
		}
		tag, err := tx.Exec(ctx,
			`DELETE FROM offer_line_item WHERE id = $1 AND offer_id = $2`, lineID, offerID)
		if err != nil {
			return fmt.Errorf("delete offer line: %w", err)
		}
		if tag.RowsAffected() == 0 {
			return apperrors.ErrNotFound
		}
		if err := recomputeOfferTotals(ctx, tx, offerID); err != nil {
			return err
		}
		if _, err := storekit.Audit(ctx, tx, "update", "offer", offerID.UUID,
			map[string]any{"line_id": lineID}, map[string]any{"line_removed": true}); err != nil {
			return fmt.Errorf("audit line remove: %w", err)
		}
		if out, err = readOfferWithLines(ctx, tx, offerID, storekit.LiveOnly); err != nil {
			return fmt.Errorf("read offer after line remove: %w", err)
		}
		return nil
	})
	return out, err
}
