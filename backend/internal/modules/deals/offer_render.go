// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// The offer PDF render seam (OFFER-AC-12/12a, offers-depth arc 4a T4):
// PrepareRender gathers every input offer_pdf.go's RenderOfferPDF needs —
// the offer (its Net/Tax/GrossMinor are already the totals engine's
// persisted output), its lines, the buyer legal block, the issuer name,
// and the resolved locale — WITHOUT ever touching blob storage. The
// render handler (handlers_offertemplates.go) calls RenderOfferPDF over
// these ingredients, writes the bytes to the object store, and then calls
// SetPdfAssetRef to persist the resulting key. Splitting the read/render/
// write this way keeps OfferStore blob-free (mirrors poc-1's own split;
// unlike activities' attachment store, which owns its blob field, this
// store never imports platform/blobstore).

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
}

// PrepareRender resolves the render inputs for one offer: the caller must
// hold offer read and be able to see the offer's deal (visibleOffer's
// usual row-scope gate; a miss on either answers 404, existence-hiding).
// It ALSO requires offer-update up front, even though this call itself
// only reads: RenderOffer's overall operation ends in a write
// (SetPdfAssetRef persists pdf_asset_ref), so gating on read alone would
// let a read-only-role caller trigger a full PDF render and an object-store
// Put before ever hitting the update check that used to live only in
// SetPdfAssetRef — an orphan blob write ahead of the 403 it was always
// going to get. Requiring both here, before the transaction even opens,
// means a denied caller never reaches the render or the blob store at all.
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
		locale, err := resolveRenderLocale(ctx, tx, offer.TemplateId)
		if err != nil {
			return err
		}
		out = RenderIngredients{
			Offer: offer, LineItems: lines, BuyerBlock: buyerBlock,
			IssuerName: issuerName, Locale: locale,
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

// resolveRenderLocale resolves an offer's render locale: unset falls back
// to de-DE (offer_template's own launch default), never a blank column. A
// template_id that no longer resolves (only possible if a template were
// hard-deleted out from under the FK's ON DELETE SET NULL mid-flight,
// never observed in practice) falls back the same way rather than
// failing an otherwise-good render.
func resolveRenderLocale(ctx context.Context, tx pgx.Tx, templateID *openapi_types.UUID) (string, error) {
	if templateID == nil {
		return defaultOfferTemplateLocale, nil
	}
	var locale string
	err := tx.QueryRow(ctx,
		`SELECT locale FROM offer_template WHERE id = $1`, ids.UUID(*templateID)).Scan(&locale)
	if errors.Is(err, pgx.ErrNoRows) {
		return defaultOfferTemplateLocale, nil
	}
	if err != nil {
		return "", fmt.Errorf("render: read template locale: %w", err)
	}
	return locale, nil
}

// SetPdfAssetRef persists the blob key the render handler already wrote
// and audits it as a standard offer update. PrepareRender's read and this
// write are deliberately separate transactions — the blob.Put in between
// cannot ride inside either one — so this step re-locks and re-verifies
// visibility fresh rather than trusting the caller's earlier read. The
// offer-update requirement below is a belt-and-braces repeat of
// PrepareRender's own — the real admission gate for the whole render
// operation lives there, before the render or the blob write, precisely so
// a denied caller never reaches this far.
func (s *Store) SetPdfAssetRef(ctx context.Context, id ids.OfferID, ref string) (crmcontracts.Offer, error) {
	if err := auth.Require(ctx, "offer", principal.ActionUpdate); err != nil {
		return crmcontracts.Offer{}, err
	}
	var out crmcontracts.Offer
	err := s.tx(ctx, func(tx pgx.Tx) error {
		if _, _, err := visibleOfferLocked(ctx, tx, id, storekit.LiveOnly); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx,
			`UPDATE offer SET pdf_asset_ref = $2 WHERE id = $1`, id, ref); err != nil {
			return fmt.Errorf("set offer pdf_asset_ref: %w", err)
		}
		if _, err := storekit.Audit(ctx, tx, "update", "offer", id.UUID,
			nil, map[string]any{"pdf_asset_ref": ref}); err != nil {
			return fmt.Errorf("audit offer render: %w", err)
		}
		var err error
		if out, err = readOfferWithLines(ctx, tx, id, storekit.LiveOnly); err != nil {
			return fmt.Errorf("read offer after render: %w", err)
		}
		return nil
	})
	return out, err
}
