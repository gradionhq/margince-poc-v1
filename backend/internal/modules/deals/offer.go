// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// The offer aggregate's write paths (B-E03.17): a versioned Angebot
// bound to one deal, with typed line items whose price/description are
// SNAPSHOTS and whose money totals are derived by the offer_totals
// engine inside every mutating transaction. An offer is mutable only
// while status=draft (B-E03.19); the lifecycle transitions live in
// offer_lifecycle.go.
//
// Row scope: an offer carries no owner_id — it belongs to its deal, so
// every offer read/write derives visibility from the DEAL's row scope
// (a row-scope miss answers 404, existence-hiding).

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

// OfferNotDraftError maps to 422: only a draft offer is editable — a
// sent/accepted/rejected/superseded offer is a fixed record (B-E03.19).
type OfferNotDraftError struct{ Status string }

func (e *OfferNotDraftError) Error() string {
	return "the offer is " + e.Status + "; only a draft offer can be edited"
}

// OfferEmptyError maps to 422: an offer with no line items has nothing
// to send.
type OfferEmptyError struct{}

func (e *OfferEmptyError) Error() string { return "the offer has no line items to send" }

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
	ProductID      *ids.UUID
	Description    *string
	Unit           *string
	Quantity       string
	UnitPriceMinor *int64
	DiscountPct    *string
	TaxRate        *string
}

type CreateOfferInput struct {
	Currency   string
	BuyerOrgID *ids.UUID
	ValidUntil *string // ISO date
	IntroText  *string
	TermsText  *string
	LineItems  []OfferLineInputRow
	Source     string
}

func (s *Store) CreateOffer(ctx context.Context, dealID ids.UUID, in CreateOfferInput) (crmcontracts.Offer, error) {
	if err := auth.Require(ctx, "offer", principal.ActionCreate); err != nil {
		return crmcontracts.Offer{}, err
	}
	by, err := storekit.CapturedBy(ctx)
	if err != nil {
		return crmcontracts.Offer{}, err
	}

	var out crmcontracts.Offer
	err = s.tx(ctx, func(tx pgx.Tx) error {
		var err error
		out, err = createOfferTx(ctx, tx, dealID, in, by)
		return err
	})
	return out, err
}

// createOfferTx resolves the buyer org, mints the offer number, inserts
// the offer and its lines, derives the totals, and runs the write shape —
// all inside the caller's transaction.
func createOfferTx(ctx context.Context, tx pgx.Tx, dealID ids.UUID, in CreateOfferInput, by string) (crmcontracts.Offer, error) {
	wsID := storekit.MustWorkspace(ctx)
	// The deal anchors the offer's visibility: it must exist, be live
	// and sit inside the caller's row scope (miss = 404).
	if err := auth.EnsureLinkTarget(ctx, tx, "deal", dealID); err != nil {
		return crmcontracts.Offer{}, err
	}
	buyerOrg, err := resolveBuyerOrg(ctx, tx, dealID, in.BuyerOrgID)
	if err != nil {
		return crmcontracts.Offer{}, err
	}
	number, err := nextOfferNumber(ctx, tx, wsID)
	if err != nil {
		return crmcontracts.Offer{}, err
	}

	id := ids.NewV7()
	if _, err := tx.Exec(ctx,
		`INSERT INTO offer (id, workspace_id, deal_id, offer_number, revision, status, currency,
		                    buyer_org_id, valid_until, intro_text, terms_text, source, captured_by)
		 VALUES ($1, $2, $3, $4, 1, 'draft', $5, $6, $7, $8, $9, $10, $11)`,
		id, wsID, dealID, number, in.Currency, buyerOrg, in.ValidUntil, in.IntroText, in.TermsText, in.Source, by); err != nil {
		return crmcontracts.Offer{}, fmt.Errorf("insert offer: %w", err)
	}
	if err := insertOfferLines(ctx, tx, wsID, id, in.Currency, in.LineItems); err != nil {
		return crmcontracts.Offer{}, err
	}
	if err := recomputeOfferTotals(ctx, tx, id); err != nil {
		return crmcontracts.Offer{}, err
	}

	auditID, err := storekit.Audit(ctx, tx, "create", "offer", id,
		nil, map[string]any{"offer_number": number, "deal_id": dealID, "currency": in.Currency})
	if err != nil {
		return crmcontracts.Offer{}, fmt.Errorf("audit offer create: %w", err)
	}
	if err := storekit.Emit(ctx, tx, auditID, "offer.created", "offer", id, map[string]any{
		"offer_id": id, "deal_id": dealID, "revision": 1,
		"currency": in.Currency, "source": in.Source, "captured_by": by,
	}); err != nil {
		return crmcontracts.Offer{}, fmt.Errorf("emit offer.created: %w", err)
	}
	out, err := readOfferWithLines(ctx, tx, id, storekit.LiveOnly)
	if err != nil {
		return crmcontracts.Offer{}, fmt.Errorf("read created offer: %w", err)
	}
	return out, nil
}

// resolveBuyerOrg picks the offer's buyer org: an explicit org is
// row-scope probed (a client-supplied FK, H1); absent one, the offer
// inherits the deal's organization.
func resolveBuyerOrg(ctx context.Context, tx pgx.Tx, dealID ids.UUID, buyerOrgID *ids.UUID) (*ids.UUID, error) {
	if buyerOrgID != nil {
		if err := auth.EnsureLinkTarget(ctx, tx, "organization", *buyerOrgID); err != nil {
			return nil, err
		}
		return buyerOrgID, nil
	}
	var dealOrg *ids.UUID
	if err := tx.QueryRow(ctx,
		`SELECT organization_id FROM deal WHERE id = $1`, dealID).Scan(&dealOrg); err != nil {
		return nil, fmt.Errorf("read deal organization: %w", err)
	}
	return dealOrg, nil
}

// insertOfferLines inserts each input line in order, numbering the 422
// error by the caller's 1-based line position.
func insertOfferLines(ctx context.Context, tx pgx.Tx, wsID, offerID ids.UUID, currency string, lines []OfferLineInputRow) error {
	for i, line := range lines {
		if err := insertOfferLine(ctx, tx, wsID, offerID, currency, line); err != nil {
			return fmt.Errorf("line %d: %w", i+1, err)
		}
	}
	return nil
}

// nextOfferNumber mints the workspace's next human-facing Angebot number
// under a transaction-scoped advisory lock — two concurrent creates
// serialize on the mint instead of racing into offer_number_rev_unique.
func nextOfferNumber(ctx context.Context, tx pgx.Tx, wsID ids.UUID) (string, error) {
	if _, err := tx.Exec(ctx,
		`SELECT pg_advisory_xact_lock(hashtextextended('offer_number:' || $1::text, 0))`, wsID); err != nil {
		return "", fmt.Errorf("acquire offer-number lock: %w", err)
	}
	var next int
	if err := tx.QueryRow(ctx,
		`SELECT COALESCE(MAX(substring(offer_number FROM '^A-([0-9]+)$')::int), 1000) + 1
		 FROM offer WHERE workspace_id = $1`, wsID).Scan(&next); err != nil {
		return "", fmt.Errorf("mint offer number: %w", err)
	}
	return fmt.Sprintf("A-%d", next), nil
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

func insertOfferLine(ctx context.Context, tx pgx.Tx, wsID, offerID ids.UUID, offerCurrency string, in OfferLineInputRow) error {
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
func resolveProductSnapshot(ctx context.Context, tx pgx.Tx, productID *ids.UUID, offerCurrency string, in lineSnapshotDefaults) (lineSnapshotDefaults, error) {
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
func insertOfferLineRow(ctx context.Context, tx pgx.Tx, wsID, offerID ids.UUID, in OfferLineInputRow, line resolvedOfferLine) error {
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

// recomputeOfferTotals re-derives net/tax/gross from the offer's live
// lines through the totals engine — the ONE writer of the stored totals,
// called inside every transaction that touches a line.
func recomputeOfferTotals(ctx context.Context, tx pgx.Tx, offerID ids.UUID) error {
	rows, err := tx.Query(ctx,
		`SELECT quantity::text, unit_price_minor, discount_pct::text, tax_rate::text
		 FROM offer_line_item WHERE offer_id = $1`, offerID)
	if err != nil {
		return fmt.Errorf("read lines for totals: %w", err)
	}
	defer rows.Close()
	var lines []OfferLineInput
	for rows.Next() {
		var l OfferLineInput
		if err := rows.Scan(&l.Quantity, &l.UnitPriceMinor, &l.DiscountPct, &l.TaxRate); err != nil {
			return err
		}
		lines = append(lines, l)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	rows.Close()

	totals, err := OfferTotals(lines)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx,
		`UPDATE offer SET net_minor = $2, tax_minor = $3, gross_minor = $4 WHERE id = $1`,
		offerID, totals.NetMinor, totals.TaxMinor, totals.GrossMinor); err != nil {
		return fmt.Errorf("store recomputed totals: %w", err)
	}
	return nil
}

// visibleOffer loads one offer and applies the deal-derived row scope:
// the caller sees the offer iff they can see its deal (miss = 404).
func visibleOffer(ctx context.Context, tx pgx.Tx, id ids.UUID, archived storekit.ArchivedFilter) (crmcontracts.Offer, error) {
	offer, err := readOffer(ctx, tx, id, archived)
	if err != nil {
		return crmcontracts.Offer{}, err
	}
	if err := auth.EnsureVisible(ctx, tx, "deal", ids.UUID(offer.DealId)); err != nil {
		return crmcontracts.Offer{}, err
	}
	return offer, nil
}

// visibleOfferLocked is the MUTATION spelling of visibleOffer: it takes
// the offer's row lock before the status/visibility reads, so two
// concurrent editors — or an edit racing a send — linearize and the
// stored totals can never miss a committed line. The returned witness
// lets the caller patch under the held lock (storekit.ApplyLocked).
func visibleOfferLocked(ctx context.Context, tx pgx.Tx, id ids.UUID, archived storekit.ArchivedFilter) (crmcontracts.Offer, storekit.RowLock, error) {
	lock, err := storekit.LockRow(ctx, tx, "offer", id, storekit.LiveOnly)
	if err != nil {
		return crmcontracts.Offer{}, storekit.RowLock{}, err
	}
	offer, err := visibleOffer(ctx, tx, id, archived)
	if err != nil {
		return crmcontracts.Offer{}, storekit.RowLock{}, err
	}
	return offer, lock, nil
}

// ensureDraft gates every offer/line edit: mutable only while draft.
func ensureDraft(offer crmcontracts.Offer) error {
	if offer.Status != crmcontracts.OfferStatusDraft {
		return &OfferNotDraftError{Status: string(offer.Status)}
	}
	return nil
}

type UpdateOfferInput struct {
	Currency   *string
	BuyerOrgID *ids.UUID
	ValidUntil *string // ISO date
	IntroText  *string
	TermsText  *string
	IfVersion  *int64
}

func (s *Store) UpdateOffer(ctx context.Context, id ids.UUID, in UpdateOfferInput) (crmcontracts.Offer, error) {
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

		p := storekit.NewPatch()
		if in.Currency != nil {
			p.Set("currency", current.Currency, *in.Currency)
		}
		if in.BuyerOrgID != nil {
			if err := auth.EnsureLinkTarget(ctx, tx, "organization", *in.BuyerOrgID); err != nil {
				return err
			}
			p.Set("buyer_org_id", current.BuyerOrgId, *in.BuyerOrgID)
		}
		if in.ValidUntil != nil {
			p.Set("valid_until", current.ValidUntil, *in.ValidUntil)
		}
		if in.IntroText != nil {
			p.Set("intro_text", current.IntroText, *in.IntroText)
		}
		if in.TermsText != nil {
			p.Set("terms_text", current.TermsText, *in.TermsText)
		}
		if p.Empty() {
			out, err = readOfferWithLines(ctx, tx, id, storekit.LiveOnly)
			return err
		}
		if err := p.ApplyGuarded(ctx, tx, "offer", id, in.IfVersion); err != nil {
			return fmt.Errorf("apply offer patch: %w", err)
		}
		if _, err := storekit.Audit(ctx, tx, "update", "offer", id, p.Before(), p.After()); err != nil {
			return fmt.Errorf("audit offer update: %w", err)
		}
		if out, err = readOfferWithLines(ctx, tx, id, storekit.LiveOnly); err != nil {
			return fmt.Errorf("read updated offer: %w", err)
		}
		return nil
	})
	return out, err
}

func (s *Store) ArchiveOffer(ctx context.Context, id ids.UUID) (crmcontracts.Offer, error) {
	if err := auth.Require(ctx, "offer", principal.ActionDelete); err != nil {
		return crmcontracts.Offer{}, err
	}
	var out crmcontracts.Offer
	err := s.tx(ctx, func(tx pgx.Tx) error {
		if _, _, err := visibleOfferLocked(ctx, tx, id, storekit.LiveOnly); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx,
			`UPDATE offer SET archived_at = now() WHERE id = $1 AND archived_at IS NULL`, id); err != nil {
			return fmt.Errorf("archive offer: %w", err)
		}
		if _, err := storekit.Audit(ctx, tx, "archive", "offer", id, nil, nil); err != nil {
			return fmt.Errorf("audit offer archive: %w", err)
		}
		var err error
		if out, err = readOfferWithLines(ctx, tx, id, storekit.IncludeArchived); err != nil {
			return fmt.Errorf("read archived offer: %w", err)
		}
		return nil
	})
	return out, err
}

func (s *Store) AddOfferLineItem(ctx context.Context, offerID ids.UUID, in OfferLineInputRow) (crmcontracts.Offer, error) {
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
		if _, err := storekit.Audit(ctx, tx, "update", "offer", offerID,
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

func (s *Store) UpdateOfferLineItem(ctx context.Context, offerID, lineID ids.UUID, in UpdateOfferLineInput) (crmcontracts.Offer, error) {
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
		if _, err := storekit.Audit(ctx, tx, "update", "offer", offerID, before, after); err != nil {
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
func readOfferLineForUpdate(ctx context.Context, tx pgx.Tx, offerID, lineID ids.UUID) (curPosition int, curDescription, curUnit string, line OfferLineInput, err error) {
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

func (s *Store) RemoveOfferLineItem(ctx context.Context, offerID, lineID ids.UUID) (crmcontracts.Offer, error) {
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
		if _, err := storekit.Audit(ctx, tx, "update", "offer", offerID,
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
