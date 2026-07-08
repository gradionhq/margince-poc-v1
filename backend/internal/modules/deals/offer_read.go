// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// The offer read paths: single-row get (with nested lines + derived
// line figures), the per-deal keyset list, and the one column list +
// scanner every offer read shares. Line net/tax/total are never stored —
// they are re-derived by the totals engine at read time, so the wire
// always shows exactly what the engine computes.

package deals

import (
	"context"
	"errors"
	"fmt"
	"strconv"
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

func (s *Store) GetOffer(ctx context.Context, id ids.OfferID, archived storekit.ArchivedFilter) (crmcontracts.Offer, error) {
	if err := auth.Require(ctx, "offer", principal.ActionRead); err != nil {
		return crmcontracts.Offer{}, err
	}
	var out crmcontracts.Offer
	err := s.tx(ctx, func(tx pgx.Tx) error {
		if _, err := visibleOffer(ctx, tx, id, archived); err != nil {
			return err
		}
		var err error
		out, err = readOfferWithLines(ctx, tx, id, archived)
		return err
	})
	return out, err
}

type ListDealOffersInput struct {
	Cursor *string
	Limit  *int
	Status *string
}

func (s *Store) ListDealOffers(ctx context.Context, dealID ids.DealID, in ListDealOffersInput) ([]crmcontracts.Offer, storekit.Page, error) {
	if err := auth.Require(ctx, "offer", principal.ActionRead); err != nil {
		return nil, storekit.Page{}, err
	}
	limit := storekit.ClampLimit(in.Limit)

	var offers []crmcontracts.Offer
	var page storekit.Page
	err := s.tx(ctx, func(tx pgx.Tx) error {
		// The deal is the offer's visibility anchor: out of the caller's
		// row scope, the whole listing answers 404, never an empty page
		// that discloses the deal exists.
		if err := auth.EnsureLinkTarget(ctx, tx, "deal", dealID.UUID); err != nil {
			return err
		}

		where := []string{"deal_id = $1", "archived_at IS NULL"}
		args := []any{dealID}
		arg := func(v any) int { args = append(args, v); return len(args) }
		extra, err := dealOffersFilters(in, arg)
		if err != nil {
			return err
		}
		where = append(where, extra...)

		rows, err := tx.Query(ctx,
			`SELECT `+offerColumns+` FROM offer WHERE `+strings.Join(where, " AND ")+
				storekit.SQLf(` ORDER BY created_at DESC, id DESC LIMIT %d`, limit+1),
			args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		if offers, err = scanOffers(rows); err != nil {
			return err
		}
		if len(offers) > limit {
			offers = offers[:limit]
			last := offers[len(offers)-1]
			page = storekit.Page{HasMore: true, NextCursor: storekit.EncodeCursor(last.CreatedAt, ids.UUID(last.Id))}
		}
		return attachOfferLines(ctx, tx, offers)
	})
	if offers == nil {
		offers = []crmcontracts.Offer{}
	}
	return offers, page, err
}

// dealOffersFilters renders the status/cursor list filters, binding each
// value through arg.
func dealOffersFilters(in ListDealOffersInput, arg func(any) int) ([]string, error) {
	var where []string
	if in.Status != nil && *in.Status != "" {
		where = append(where, storekit.SQLf("status = $%d", arg(*in.Status)))
	}
	if in.Cursor != nil && *in.Cursor != "" {
		c, err := storekit.DecodeCursor(*in.Cursor)
		if err != nil {
			return nil, err
		}
		where = append(where, storekit.SQLf("(created_at, id) < ($%d, $%d)", arg(c.CreatedAt), arg(c.ID)))
	}
	return where, nil
}

// scanOffers drains an offer result set into rows (line items attached
// separately).
func scanOffers(rows pgx.Rows) ([]crmcontracts.Offer, error) {
	var offers []crmcontracts.Offer
	for rows.Next() {
		o, err := scanOffer(rows)
		if err != nil {
			return nil, err
		}
		offers = append(offers, o)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return offers, nil
}

// attachOfferLines loads and nests each offer's derived line items.
func attachOfferLines(ctx context.Context, tx pgx.Tx, offers []crmcontracts.Offer) error {
	for i := range offers {
		lines, err := readOfferLines(ctx, tx, ids.From[ids.OfferKind](ids.UUID(offers[i].Id)))
		if err != nil {
			return err
		}
		offers[i].LineItems = &lines
	}
	return nil
}

const offerColumns = `id, workspace_id, deal_id, offer_number, revision, status, currency,
	buyer_org_id, buyer_snapshot, issuer_snapshot, valid_until, intro_text, terms_text,
	net_minor, tax_minor, gross_minor, fx_rate_to_base::text, fx_rate_date, pdf_asset_ref,
	accepted_at, source, captured_by, version, created_at, updated_at, archived_at`

func readOffer(ctx context.Context, tx pgx.Tx, id ids.OfferID, archived storekit.ArchivedFilter) (crmcontracts.Offer, error) {
	q := `SELECT ` + offerColumns + ` FROM offer WHERE id = $1`
	if archived == storekit.LiveOnly {
		q += ` AND archived_at IS NULL`
	}
	o, err := scanOffer(tx.QueryRow(ctx, q, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return crmcontracts.Offer{}, apperrors.ErrNotFound
	}
	return o, err
}

// readOfferWithLines is readOffer plus the nested line items — the shape
// every offer response returns.
func readOfferWithLines(ctx context.Context, tx pgx.Tx, id ids.OfferID, archived storekit.ArchivedFilter) (crmcontracts.Offer, error) {
	offer, err := readOffer(ctx, tx, id, archived)
	if err != nil {
		return crmcontracts.Offer{}, err
	}
	lines, err := readOfferLines(ctx, tx, id)
	if err != nil {
		return crmcontracts.Offer{}, err
	}
	offer.LineItems = &lines
	return offer, nil
}

func scanOffer(row pgx.Row) (crmcontracts.Offer, error) {
	var o crmcontracts.Offer
	var id, wsID, dealID ids.UUID
	var buyerOrgID *ids.UUID
	var offerNumber string
	var revision int
	var status string
	var buyerSnapshot, issuerSnapshot *map[string]interface{}
	var validUntil, fxRateDate *time.Time
	var netMinor, taxMinor, grossMinor int64
	var capturedBy string
	var version int64

	err := row.Scan(&id, &wsID, &dealID, &offerNumber, &revision, &status, &o.Currency,
		&buyerOrgID, &buyerSnapshot, &issuerSnapshot, &validUntil, &o.IntroText, &o.TermsText,
		&netMinor, &taxMinor, &grossMinor, &o.FxRateToBase, &fxRateDate, &o.PdfAssetRef,
		&o.AcceptedAt, &o.Source, &capturedBy, &version, &o.CreatedAt, &o.UpdatedAt, &o.ArchivedAt)
	if err != nil {
		return o, err
	}

	o.Id = openapi_types.UUID(id)
	o.WorkspaceId = openapi_types.UUID(wsID)
	o.DealId = openapi_types.UUID(dealID)
	o.BuyerOrgId = uuidPtr(buyerOrgID)
	o.OfferNumber = &offerNumber
	o.Revision = &revision
	o.Status = crmcontracts.OfferStatus(status)
	o.BuyerSnapshot = buyerSnapshot
	o.IssuerSnapshot = issuerSnapshot
	if validUntil != nil {
		o.ValidUntil = &openapi_types.Date{Time: *validUntil}
	}
	if fxRateDate != nil {
		o.FxRateDate = &openapi_types.Date{Time: *fxRateDate}
	}
	o.NetMinor = &netMinor
	o.TaxMinor = &taxMinor
	o.GrossMinor = &grossMinor
	o.CapturedBy = &capturedBy
	o.Version = &version
	return o, nil
}

func readOfferLines(ctx context.Context, tx pgx.Tx, offerID ids.OfferID) ([]crmcontracts.OfferLineItem, error) {
	rows, err := tx.Query(ctx,
		`SELECT id, position, product_id, description, unit, quantity::text, unit_price_minor,
		        discount_pct::text, tax_rate::text, evidence, version, created_at, updated_at
		 FROM offer_line_item WHERE offer_id = $1 ORDER BY position, id`, offerID)
	if err != nil {
		return nil, fmt.Errorf("read offer lines: %w", err)
	}
	defer rows.Close()

	lines := []crmcontracts.OfferLineItem{}
	for rows.Next() {
		var l crmcontracts.OfferLineItem
		var id ids.UUID
		var productID *ids.UUID
		var quantity, discount, taxRate string
		var evidence *map[string]interface{}
		var version int64
		if err := rows.Scan(&id, &l.Position, &productID, &l.Description, &l.Unit, &quantity,
			&l.UnitPriceMinor, &discount, &taxRate, &evidence, &version, &l.CreatedAt, &l.UpdatedAt); err != nil {
			return nil, err
		}
		fig, err := LineTotals(OfferLineInput{
			Quantity: quantity, UnitPriceMinor: l.UnitPriceMinor, DiscountPct: discount, TaxRate: taxRate,
		})
		if err != nil {
			return nil, fmt.Errorf("derive line totals: %w", err)
		}
		l.Id = openapi_types.UUID(id)
		l.ProductId = uuidPtr(productID)
		l.Evidence = evidence
		l.Version = &version
		l.LineNetMinor = &fig.NetMinor
		l.LineTaxMinor = &fig.TaxMinor
		l.LineTotalMinor = &fig.TotalMinor
		if l.Quantity, err = parseWireDecimal("quantity", quantity); err != nil {
			return nil, err
		}
		if l.DiscountPct, err = parseWireDecimal("discount_pct", discount); err != nil {
			return nil, err
		}
		if l.TaxRate, err = parseWireDecimal("tax_rate", taxRate); err != nil {
			return nil, err
		}
		lines = append(lines, l)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return lines, nil
}

// parseWireDecimal renders a DB numeric (exact ::text) as the contract's
// float64. The scale is ≤3 decimal places over ≤14 digits, well inside
// float64's exact round-trip range — the engine itself never consumes
// the float, only the wire does.
func parseWireDecimal(field, value string) (float64, error) {
	v, err := strconv.ParseFloat(value, 64)
	if err != nil {
		return 0, fmt.Errorf("%s is not numeric: %w", field, err)
	}
	return v, nil
}
