// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// The offer PDF render seam: PrepareRender gathers every input
// offer_pdf.go's RenderOfferPDF needs — the offer (its Net/Tax/GrossMinor
// are already the totals engine's persisted output), its lines, the buyer
// legal block, the issuer name, and the selected template's locale +
// layout — WITHOUT ever touching blob storage. The render handler
// (handlers_offertemplates.go) calls RenderOfferPDF over these
// ingredients, writes the bytes to the object store, and then calls
// SetPdfAssetRef to persist the resulting key. Splitting the read/render/
// write this way keeps OfferStore blob-free (unlike activities'
// attachment store, which owns its blob field, this store never imports
// platform/blobstore).

package deals

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// RenderIngredients bundles every resolved input RenderOfferPDF needs.
type RenderIngredients struct {
	Offer      crmcontracts.Offer
	LineItems  []crmcontracts.OfferLineItem
	BuyerBlock map[string]any
	IssuerName string
	Locale     string
	// Layout is the selected offer_template's layout bag (or an empty map
	// when the offer carries no template) — offer_pdf.go's RenderOfferPDF
	// honors the bounded set of text keys it defines and ignores the rest.
	Layout map[string]any
}

// PrepareRender resolves the render inputs for one offer: the caller must
// hold offer read and be able to see the offer's deal (visibleOffer's
// usual row-scope gate; a miss on either answers 404, existence-hiding).
// It ALSO requires offer-update up front, even though this call itself
// only reads: RenderOffer's overall operation ends in a write
// (SetPdfAssetRef persists pdf_asset_ref), so requiring both gates here —
// before the transaction even opens — means a caller who lacks the update
// grant never reaches the render or the blob store at all; PrepareRender
// is the sole admission gate for the whole render operation.
// It runs a single read-only transaction and never opens the blob store —
// the caller (the render handler) writes the PDF bytes afterward and
// commits the resulting ref through the separate SetPdfAssetRef call.
func (s *Store) PrepareRender(ctx context.Context, id ids.OfferID) (RenderIngredients, error) {
	if err := auth.Require(ctx, "offer", principal.ActionRead); err != nil {
		return RenderIngredients{}, err
	}
	if err := auth.Require(ctx, "offer", principal.ActionUpdate); err != nil {
		return RenderIngredients{}, err
	}
	var out RenderIngredients
	err := s.tx(ctx, func(tx pgx.Tx) error {
		offer, err := visibleOffer(ctx, tx, id, storekit.LiveOnly)
		if err != nil {
			return err
		}
		lines, err := readOfferLines(ctx, tx, id)
		if err != nil {
			return err
		}
		buyerBlock, err := resolveRenderBuyerBlock(ctx, tx, offer)
		if err != nil {
			return err
		}
		issuerName, err := resolveRenderIssuerName(ctx, tx, offer)
		if err != nil {
			return err
		}
		locale, layout, err := resolveRenderTemplate(ctx, tx, offer.TemplateId)
		if err != nil {
			return err
		}
		out = RenderIngredients{
			Offer: offer, LineItems: lines, BuyerBlock: buyerBlock,
			IssuerName: issuerName, Locale: locale, Layout: layout,
		}
		return nil
	})
	return out, err
}

// resolveRenderBuyerBlock answers the buyer legal block the renderer
// shows: the frozen buyer_snapshot once sent (SendOffer already froze it
// as the legal record), the LIVE organization read fresh while still
// draft (so an offer edited but never sent never shows a stale block), or
// nil when the offer carries no buyer org at all. This is deliberately a
// fresh, independent query rather than a refactor of offer_lifecycle.go's
// sendSnapshots — the plan's boundary keeps Send/Accept/Reject/Regenerate
// and their snapshot logic untouched.
func resolveRenderBuyerBlock(ctx context.Context, tx pgx.Tx, offer crmcontracts.Offer) (block map[string]any, err error) {
	if offer.BuyerSnapshot != nil {
		return *offer.BuyerSnapshot, nil
	}
	if offer.BuyerOrgId == nil {
		return block, err
	}
	var displayName string
	var legalName *string
	scanErr := tx.QueryRow(ctx,
		`SELECT display_name, legal_name FROM organization WHERE id = $1`,
		ids.UUID(*offer.BuyerOrgId)).Scan(&displayName, &legalName)
	if errors.Is(scanErr, pgx.ErrNoRows) {
		return block, err
	}
	if scanErr != nil {
		return nil, fmt.Errorf("render: read buyer organization: %w", scanErr)
	}
	block = map[string]any{
		"organization_id": offer.BuyerOrgId.String(),
		"display_name":    displayName,
	}
	if legalName != nil {
		block["legal_name"] = *legalName
	}
	return block, nil
}

// resolveRenderIssuerName mirrors resolveRenderBuyerBlock's frozen/live
// split for the seller side: the frozen issuer_snapshot's workspace_name
// once sent, else the workspace's current live name.
func resolveRenderIssuerName(ctx context.Context, tx pgx.Tx, offer crmcontracts.Offer) (string, error) {
	if offer.IssuerSnapshot != nil {
		if name, ok := (*offer.IssuerSnapshot)["workspace_name"].(string); ok && name != "" {
			return name, nil
		}
	}
	var name string
	if err := tx.QueryRow(ctx,
		`SELECT name FROM workspace WHERE id = $1`, storekit.MustWorkspace(ctx)).Scan(&name); err != nil {
		return "", fmt.Errorf("render: read issuer workspace name: %w", err)
	}
	return name, nil
}

// resolveRenderTemplate resolves an offer's render locale AND layout
// together — one offer_template row backs both. Unset falls back to
// de-DE (offer_template's own launch default) and an empty layout, never
// a blank column. A template_id that no longer resolves (only possible if
// a template were hard-deleted out from under the FK's ON DELETE SET NULL
// mid-flight, never observed in practice) falls back the same way rather
// than failing an otherwise-good render.
func resolveRenderTemplate(ctx context.Context, tx pgx.Tx, templateID *openapi_types.UUID) (locale string, layout map[string]any, err error) {
	if templateID == nil {
		return defaultOfferTemplateLocale, map[string]any{}, nil
	}
	err = tx.QueryRow(ctx,
		`SELECT locale, layout FROM offer_template WHERE id = $1`, ids.UUID(*templateID)).Scan(&locale, &layout)
	if errors.Is(err, pgx.ErrNoRows) {
		return defaultOfferTemplateLocale, map[string]any{}, nil
	}
	if err != nil {
		return "", nil, fmt.Errorf("render: read template locale/layout: %w", err)
	}
	if layout == nil {
		layout = map[string]any{}
	}
	return locale, layout, nil
}

// SetPdfAssetRef persists the blob key the render handler already wrote
// and audits it as a standard offer update. PrepareRender's read and this
// write are deliberately separate transactions — the blob.Put in between
// cannot ride inside either one — so a concurrent draft edit (a line
// added/removed, a regenerate, another render) can land between the two.
// expectedVersion is the offer's row version PrepareRender saw; the UPDATE
// is fenced on it (the same optimistic-concurrency column every other
// client-driven offer write honors), so a version that moved under us
// answers apperrors.ErrVersionSkew instead of silently pointing
// pdf_asset_ref at a PDF that no longer matches the offer's current lines.
// The caller (the render handler) reclaims the blob it already wrote when
// this happens — this store stays blob-free, so it cannot do that itself.
//
// It also returns the offer's PREVIOUS pdf_asset_ref (nil if it had none, or
// if it is unchanged from ref): the render handler's key is per-attempt-
// unique, so a successful re-render leaves its old blob orphaned unless the
// caller reclaims it — this is the one place that still holds the
// pre-overwrite row, so it is the one place that can hand the old ref back
// rather than requiring a second read.
func (s *Store) SetPdfAssetRef(ctx context.Context, id ids.OfferID, ref string, expectedVersion int64) (out crmcontracts.Offer, oldRef *string, err error) {
	if err := auth.Require(ctx, "offer", principal.ActionUpdate); err != nil {
		return crmcontracts.Offer{}, nil, err
	}
	err = s.tx(ctx, func(tx pgx.Tx) error {
		existing, err := visibleOffer(ctx, tx, id, storekit.LiveOnly)
		if err != nil {
			return err
		}
		if existing.PdfAssetRef != nil && *existing.PdfAssetRef != ref {
			oldRef = existing.PdfAssetRef
		}
		p := storekit.NewPatch()
		p.Set("pdf_asset_ref", nil, ref)
		if err := p.ApplyWithVersion(ctx, tx, "offer", id.UUID, expectedVersion); err != nil {
			return err
		}
		if _, err := storekit.Audit(ctx, tx, "update", "offer", id.UUID,
			nil, map[string]any{"pdf_asset_ref": ref}); err != nil {
			return fmt.Errorf("audit offer render: %w", err)
		}
		var err2 error
		if out, err2 = readOfferWithLines(ctx, tx, id, storekit.LiveOnly); err2 != nil {
			return fmt.Errorf("read offer after render: %w", err2)
		}
		return nil
	})
	if err != nil {
		return crmcontracts.Offer{}, nil, err
	}
	return out, oldRef, nil
}
