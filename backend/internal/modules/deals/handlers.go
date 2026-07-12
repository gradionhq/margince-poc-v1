// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package deals

// Handlers is the deals module's transport surface: the contract
// operations over deals, pipelines and stages, plus the per-workspace
// default-pipeline seed. Wire concerns only — decode, validate, map
// store errors to the sentinel registry; the store owns the
// transactional write shape.

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/blobstore"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
	"github.com/gradionhq/margince/backend/internal/shared/ports/fieldcatalog"
)

type Handlers struct {
	store *Store
	// blob backs the renderOffer endpoint's PDF write; nil means this
	// role answers RenderOffer 501 (WithBlobstore opts a role in). Unlike
	// activities' attachment store, the blob write lives here in
	// transport, not the store — OfferStore's PrepareRender/
	// SetPdfAssetRef seams (offer_render.go) stay blobstore-free.
	blob blobstore.Store
}

func NewHandlers(pool *pgxpool.Pool) Handlers {
	return Handlers{store: NewStore(pool)}
}

// WithFieldCatalog wires the workspace custom-field catalog into the
// transport's store (see Store.WithFieldCatalog); compose injects
// modules/customfields' Service here.
func (h Handlers) WithFieldCatalog(catalog fieldcatalog.Reader) Handlers {
	h.store = h.store.WithFieldCatalog(catalog)
	return h
}

// WithBlobstore returns handlers whose renderOffer endpoint is backed by
// the given object store; without it renderOffer answers 501 (the
// attachments precedent, activities.Handlers.WithBlobstore).
func (h Handlers) WithBlobstore(blob blobstore.Store) Handlers {
	h.blob = blob
	return h
}

// SeedWorkspaceDefaults provisions this module's per-workspace seed data
// (the default pipeline). Called by the composition root on bootstrap.
func (h Handlers) SeedWorkspaceDefaults(ctx context.Context) error {
	return h.store.SeedDefaults(ctx)
}

// SeedWorkspaceDefaultsTx is the atomic-bootstrap variant (C5): it seeds
// the defaults inside the transaction identity already opened to mint
// the workspace, so a seed failure rolls the whole tenant back rather
// than leaving a workspace with no default pipeline. Composed at the
// root; the pgx.Tx keeps the module boundary (identity never imports
// deals).
func (h Handlers) SeedWorkspaceDefaultsTx(ctx context.Context, tx pgx.Tx) error {
	return h.store.SeedDefaultsTx(ctx, tx)
}

func pageInfo(p storekit.Page) crmcontracts.PageInfo {
	info := crmcontracts.PageInfo{HasMore: p.HasMore}
	if p.NextCursor != "" {
		info.NextCursor = &p.NextCursor
	}
	return info
}

// writeStoreErr maps this module's typed store errors onto the wire
// codes the contract names, then falls through to the sentinel registry.
func writeStoreErr(w http.ResponseWriter, r *http.Request, err error) {
	var missing *RequiredFieldError
	if errors.As(err, &missing) {
		httperr.Write(w, r, httperr.Validation(missing.Field, "required", missing.Error()))
		return
	}
	var amountPair *AmountCurrencyPairError
	if errors.As(err, &amountPair) {
		httperr.Write(w, r, httperr.Validation("currency", "amount_currency_pair", amountPair.Error()))
		return
	}
	var stageMismatch *StagePipelineMismatchError
	if errors.As(err, &stageMismatch) {
		httperr.Write(w, r, httperr.Validation("to_stage_id", "stage_not_in_pipeline", stageMismatch.Error()))
		return
	}
	var lostReason *LostReasonRequiredError
	if errors.As(err, &lostReason) {
		httperr.Write(w, r, httperr.Validation("lost_reason", "lost_reason_required", lostReason.Error()))
		return
	}
	var missingFx *MissingFxRateError
	if errors.As(err, &missingFx) {
		// The spec-named code (formulas §6.1, RT-CT-23): a roll-up or freeze
		// that needs a rate it doesn't have hard-fails — never rate=1.
		httperr.Write(w, r, httperr.Validation("fx_rate_to_base", "fx_rate_unavailable", missingFx.Error()))
		return
	}
	var terminalStage *TerminalStageOnCreateError
	if errors.As(err, &terminalStage) {
		httperr.Write(w, r, httperr.Validation("stage_id", "terminal_stage_on_create", terminalStage.Error()))
		return
	}
	var pastClose *PastCloseDateError
	if errors.As(err, &pastClose) {
		httperr.Write(w, r, httperr.Validation("expected_close_date", "close_date_past", pastClose.Error()))
		return
	}
	var notDraft *OfferNotDraftError
	if errors.As(err, &notDraft) {
		httperr.Write(w, r, httperr.Validation("status", "offer_not_draft", notDraft.Error()))
		return
	}
	var notSent *OfferNotSentError
	if errors.As(err, &notSent) {
		httperr.Write(w, r, httperr.Validation("status", "offer_not_sent", notSent.Error()))
		return
	}
	var emptyOffer *OfferEmptyError
	if errors.As(err, &emptyOffer) {
		httperr.Write(w, r, httperr.Validation("line_items", "offer_empty", emptyOffer.Error()))
		return
	}
	var currencyMismatch *ProductCurrencyMismatchError
	if errors.As(err, &currencyMismatch) {
		httperr.Write(w, r, httperr.Validation("unit_price_minor", "product_currency_mismatch", currencyMismatch.Error()))
		return
	}
	var badDecimal *DecimalFieldError
	if errors.As(err, &badDecimal) {
		httperr.Write(w, r, httperr.Validation(badDecimal.Field, "invalid_decimal", badDecimal.Error()))
		return
	}
	if writeOfferTemplateConflict(w, r, err) {
		return
	}
	// Defense-in-depth net: a CHECK constraint is a business rule, so a
	// breach that slipped past the per-path validations still answers a
	// typed 422 naming the rule — never an opaque 500.
	if constraint, ok := storekit.CheckViolation(err); ok {
		httperr.Write(w, r, httperr.Validation(constraint, "constraint_violated",
			"the request violates the "+constraint+" business rule"))
		return
	}
	httperr.Write(w, r, err)
}

// writeOfferTemplateConflict maps the two offer_template pre-checked
// 409s onto the wire; false means neither matched (writeStoreErr falls
// through to the sentinel registry).
func writeOfferTemplateConflict(w http.ResponseWriter, r *http.Request, err error) bool {
	var dupTemplateName *DuplicateTemplateNameError
	if errors.As(err, &dupTemplateName) {
		httperr.Write(w, r, httperr.Duplicate("offer_template_name_duplicate", dupTemplateName.ExistingID.String()))
		return true
	}
	var defaultConflict *DefaultConflictError
	if errors.As(err, &defaultConflict) {
		httperr.Write(w, r, &httperr.DetailedError{
			Status: http.StatusConflict,
			Code:   "offer_template_default_conflict",
			Detail: fmt.Sprintf("a default template already exists for locale %q; archive or un-default it first", defaultConflict.Locale),
			Details: map[string]any{
				"existing_id": defaultConflict.ExistingID.String(),
				"locale":      defaultConflict.Locale,
			},
		})
		return true
	}
	return false
}
