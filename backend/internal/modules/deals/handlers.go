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

// NewHandlers wires the transport over the RLS-bound app pool and the
// installation's base-currency seam.
func NewHandlers(pool *pgxpool.Pool, baseCurrency BaseCurrencyFunc) Handlers {
	return Handlers{store: NewStore(pool, baseCurrency)}
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

// SeedWorkspacePipelineTx is the configured variant (A107/ADR-0061): the
// deployment file names the pipeline and its open stages; the terminal
// Won/Lost pair stays module-owned. Same atomic-bootstrap shape as
// SeedWorkspaceDefaultsTx.
func (h Handlers) SeedWorkspacePipelineTx(ctx context.Context, tx pgx.Tx, name string, open []StageSeed) error {
	return h.store.SeedPipelineTx(ctx, tx, name, open)
}

func pageInfo(p storekit.Page) crmcontracts.PageInfo {
	info := crmcontracts.PageInfo{HasMore: p.HasMore}
	if p.NextCursor != "" {
		info.NextCursor = &p.NextCursor
	}
	return info
}

// WriteOfferError maps a deals.Store error onto the wire the SAME way
// every offer handler in this package does — exported so compose's
// regenerateOffer shadow (arc 4b: it calls Store.RegenerateOffer directly
// rather than this package's own HTTP handler, so it can layer the
// AI-drafting orchestrator onto the freshly minted revision before the
// response is written) shares the ONE mapping instead of hand-rolling a
// second copy that could drift from this one as new typed errors join it.
func WriteOfferError(w http.ResponseWriter, r *http.Request, err error) {
	writeStoreErr(w, r, err)
}

// writeStoreErr maps this module's typed store errors onto the wire
// codes the contract names, then falls through to the sentinel registry.
func writeStoreErr(w http.ResponseWriter, r *http.Request, err error) {
	if writeOfferTemplateConflict(w, r, err) {
		return
	}
	if writeProjectErr(w, r, err) {
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
		// Not httperr.Duplicate: its fixed detail claims a LIVE record
		// holds the name, but offer_template_name_unique (0071) is NOT
		// partial — an ARCHIVED template reserves its name too, so the
		// blocking row here may never have been live at all.
		httperr.Write(w, r, &httperr.DetailedError{
			Status: http.StatusConflict,
			Code:   "offer_template_name_duplicate",
			Detail: "an offer template with this name already exists",
			Details: map[string]any{
				"existing_id": dupTemplateName.ExistingID.String(),
			},
		})
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
