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
	"fmt"

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

type CreateOfferInput struct {
	Currency   string
	BuyerOrgID *ids.OrganizationID
	ValidUntil *string // ISO date
	IntroText  *string
	TermsText  *string
	// TemplateID picks the offer_template render.go's PrepareRender
	// resolves into a locale; unset falls back to de-DE (the
	// offer_template package default), never a blank column.
	TemplateID *ids.OfferTemplateID
	LineItems  []OfferLineInputRow
	Source     string
}

func (s *Store) CreateOffer(ctx context.Context, dealID ids.DealID, in CreateOfferInput) (crmcontracts.Offer, error) {
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
func createOfferTx(ctx context.Context, tx pgx.Tx, dealID ids.DealID, in CreateOfferInput, by string) (crmcontracts.Offer, error) {
	wsID := storekit.MustWorkspace(ctx)
	// The deal anchors the offer's visibility: it must exist, be live
	// and sit inside the caller's row scope (miss = 404).
	if err := auth.EnsureLinkTarget(ctx, tx, "deal", dealID.UUID); err != nil {
		return crmcontracts.Offer{}, err
	}
	buyerOrg, err := resolveBuyerOrg(ctx, tx, dealID, in.BuyerOrgID)
	if err != nil {
		return crmcontracts.Offer{}, err
	}
	if err := resolveOfferTemplateRef(ctx, tx, in.TemplateID); err != nil {
		return crmcontracts.Offer{}, err
	}
	number, err := nextOfferNumber(ctx, tx, wsID)
	if err != nil {
		return crmcontracts.Offer{}, err
	}

	id := ids.New[ids.OfferKind]()
	if _, err := tx.Exec(ctx,
		`INSERT INTO offer (id, workspace_id, deal_id, offer_number, revision, status, currency,
		                    buyer_org_id, valid_until, intro_text, terms_text, template_id, source, captured_by)
		 VALUES ($1, $2, $3, $4, 1, 'draft', $5, $6, $7, $8, $9, $10, $11, $12)`,
		id, wsID, dealID, number, in.Currency, buyerOrg, in.ValidUntil, in.IntroText, in.TermsText,
		in.TemplateID, in.Source, by); err != nil {
		return crmcontracts.Offer{}, fmt.Errorf("insert offer: %w", err)
	}
	if err := insertOfferLines(ctx, tx, wsID, id, in.Currency, in.LineItems); err != nil {
		return crmcontracts.Offer{}, err
	}
	if err := recomputeOfferTotals(ctx, tx, id); err != nil {
		return crmcontracts.Offer{}, err
	}

	auditID, err := storekit.Audit(ctx, tx, "create", "offer", id.UUID,
		nil, map[string]any{"offer_number": number, "deal_id": dealID, "currency": in.Currency})
	if err != nil {
		return crmcontracts.Offer{}, fmt.Errorf("audit offer create: %w", err)
	}
	if err := storekit.Emit(ctx, tx, auditID, "offer.created", "offer", id.UUID, map[string]any{
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
func resolveBuyerOrg(ctx context.Context, tx pgx.Tx, dealID ids.DealID, buyerOrgID *ids.OrganizationID) (*ids.OrganizationID, error) {
	if buyerOrgID != nil {
		if err := auth.EnsureLinkTarget(ctx, tx, "organization", buyerOrgID.UUID); err != nil {
			return nil, err
		}
		return buyerOrgID, nil
	}
	var dealOrg *ids.OrganizationID
	if err := tx.QueryRow(ctx,
		`SELECT organization_id FROM deal WHERE id = $1`, dealID).Scan(&dealOrg); err != nil {
		return nil, fmt.Errorf("read deal organization: %w", err)
	}
	return dealOrg, nil
}

// resolveOfferTemplateRef validates a client-supplied template_id refers
// to a live template in this workspace. offer_template carries no
// owner_id — it is workspace-shared config, not row-scoped data (see
// offer_template.go's file doc) — so auth.EnsureLinkTarget (which
// requires the target table be row-scoped) does not apply here; a plain
// existence probe, already RLS-scoped to the caller's workspace by the
// surrounding transaction, gives the same existence-hiding 404 without it.
func resolveOfferTemplateRef(ctx context.Context, tx pgx.Tx, templateID *ids.OfferTemplateID) error {
	if templateID == nil {
		return nil
	}
	var exists bool
	if err := tx.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM offer_template WHERE id = $1 AND archived_at IS NULL)`,
		*templateID).Scan(&exists); err != nil {
		return fmt.Errorf("check offer_template reference: %w", err)
	}
	if !exists {
		return apperrors.ErrNotFound
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

// recomputeOfferTotals re-derives net/tax/gross from the offer's live
// lines through the totals engine — the ONE writer of the stored totals,
// called inside every transaction that touches a line. Only accepted
// lines count: a staged AI proposal must never move a total (E03.21a).
func recomputeOfferTotals(ctx context.Context, tx pgx.Tx, offerID ids.OfferID) error {
	rows, err := tx.Query(ctx,
		`SELECT quantity::text, unit_price_minor, discount_pct::text, tax_rate::text, proposal_state
		 FROM offer_line_item WHERE offer_id = $1`, offerID)
	if err != nil {
		return fmt.Errorf("read lines for totals: %w", err)
	}
	defer rows.Close()
	var lines []statefulOfferLine
	for rows.Next() {
		var l statefulOfferLine
		if err := rows.Scan(&l.Line.Quantity, &l.Line.UnitPriceMinor, &l.Line.DiscountPct, &l.Line.TaxRate, &l.State); err != nil {
			return err
		}
		lines = append(lines, l)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	rows.Close()

	totals, err := OfferTotals(acceptedLines(lines))
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
func visibleOffer(ctx context.Context, tx pgx.Tx, id ids.OfferID, archived storekit.ArchivedFilter) (crmcontracts.Offer, error) {
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
func visibleOfferLocked(ctx context.Context, tx pgx.Tx, id ids.OfferID, archived storekit.ArchivedFilter) (crmcontracts.Offer, storekit.RowLock, error) {
	lock, err := storekit.LockRow(ctx, tx, "offer", id.UUID, storekit.LiveOnly)
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
	BuyerOrgID *ids.OrganizationID
	ValidUntil *string // ISO date
	IntroText  *string
	TermsText  *string
	TemplateID *ids.OfferTemplateID
	IfVersion  *int64
}

func (s *Store) UpdateOffer(ctx context.Context, id ids.OfferID, in UpdateOfferInput) (crmcontracts.Offer, error) {
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
			if err := auth.EnsureLinkTarget(ctx, tx, "organization", in.BuyerOrgID.UUID); err != nil {
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
		if in.TemplateID != nil {
			if err := resolveOfferTemplateRef(ctx, tx, in.TemplateID); err != nil {
				return err
			}
			p.Set("template_id", current.TemplateId, *in.TemplateID)
		}
		if p.Empty() {
			out, err = readOfferWithLines(ctx, tx, id, storekit.LiveOnly)
			return err
		}
		if err := p.ApplyGuarded(ctx, tx, "offer", id.UUID, in.IfVersion); err != nil {
			return fmt.Errorf("apply offer patch: %w", err)
		}
		if _, err := storekit.Audit(ctx, tx, "update", "offer", id.UUID, p.Before(), p.After()); err != nil {
			return fmt.Errorf("audit offer update: %w", err)
		}
		if out, err = readOfferWithLines(ctx, tx, id, storekit.LiveOnly); err != nil {
			return fmt.Errorf("read updated offer: %w", err)
		}
		return nil
	})
	return out, err
}

func (s *Store) ArchiveOffer(ctx context.Context, id ids.OfferID) (crmcontracts.Offer, error) {
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
		if _, err := storekit.Audit(ctx, tx, "archive", "offer", id.UUID, nil, nil); err != nil {
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
