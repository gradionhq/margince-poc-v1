package deals

// Handlers is the deals module's transport surface: the contract
// operations over deals, pipelines and stages, plus the per-workspace
// default-pipeline seed. Wire concerns only — decode, validate, map
// store errors to the sentinel registry; the store owns the
// transactional write shape.

import (
	"context"
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
)

type Handlers struct {
	store *Store
}

func NewHandlers(pool *pgxpool.Pool) Handlers {
	return Handlers{store: NewStore(pool)}
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
		httperr.Write(w, r, httperr.Validation("fx_rate_to_base", "fx_rate_missing", missingFx.Error()))
		return
	}
	var terminalStage *TerminalStageOnCreateError
	if errors.As(err, &terminalStage) {
		httperr.Write(w, r, httperr.Validation("stage_id", "terminal_stage_on_create", terminalStage.Error()))
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
