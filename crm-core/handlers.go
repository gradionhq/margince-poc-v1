package crmcore

// Handlers is crm-core's transport surface: the contract operations over
// people, organizations, pipelines, deals, leads and activities. Wire
// concerns only — decode, validate, map store errors to the sentinel
// registry; the store owns the transactional write shape.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/fable-poc/crm-core/internal/store"
	"github.com/gradionhq/fable-poc/internal/httperr"
)

type Handlers struct {
	store *store.Store
}

func NewHandlers(pool *pgxpool.Pool) Handlers {
	return Handlers{store: store.New(pool)}
}

// SeedWorkspaceDefaults provisions crm-core's per-workspace seed data
// (the default pipeline). Called by the edge composition on bootstrap.
func (h Handlers) SeedWorkspaceDefaults(ctx context.Context) error {
	return h.store.SeedDefaults(ctx)
}

// SeedWorkspaceDefaultsTx is the atomic-bootstrap variant (C5): it seeds
// the defaults inside the transaction crm-auth already opened to mint the
// workspace, so a seed failure rolls the whole tenant back rather than
// leaving a workspace with no default pipeline. Composed at the edge; the
// pgx.Tx keeps the module boundary (crm-auth never imports crm-core).
func (h Handlers) SeedWorkspaceDefaultsTx(ctx context.Context, tx pgx.Tx) error {
	return h.store.SeedDefaultsTx(ctx, tx)
}

// --- shared wire helpers ---

func decode(w http.ResponseWriter, r *http.Request, into any) bool {
	if err := json.NewDecoder(r.Body).Decode(into); err != nil {
		httperr.Write(w, r, httperr.Validation("body", "malformed_json", err.Error()))
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// ifMatchVersion reads the optional If-Match row version (data-model
// §1.3a: a bare integer, not a quoted ETag). Malformed input is a client
// error, not last-write-wins.
func ifMatchVersion(w http.ResponseWriter, r *http.Request) (*int64, bool) {
	raw := r.Header.Get("If-Match")
	if raw == "" {
		return nil, true
	}
	v, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || v < 1 {
		httperr.Write(w, r, httperr.Validation("If-Match", "malformed_if_match", "If-Match carries the last-seen integer version"))
		return nil, false
	}
	return &v, true
}

// writeStoreErr maps crm-core's typed store errors onto the wire codes
// the contract names, then falls through to the sentinel registry.
func writeStoreErr(w http.ResponseWriter, r *http.Request, err error) {
	var missing *RequiredFieldError
	if errors.As(err, &missing) {
		httperr.Write(w, r, httperr.Validation(missing.Field, "required", missing.Error()))
		return
	}
	var dupEmail *store.DuplicateEmailError
	if errors.As(err, &dupEmail) {
		httperr.Write(w, r, httperr.Duplicate("duplicate_email", dupEmail.ExistingID.String()))
		return
	}
	var dupDomain *store.DuplicateDomainError
	if errors.As(err, &dupDomain) {
		httperr.Write(w, r, httperr.Duplicate("duplicate_domain", dupDomain.ExistingID.String()))
		return
	}
	var dupLead *store.DuplicateLeadError
	if errors.As(err, &dupLead) {
		httperr.Write(w, r, httperr.Duplicate("duplicate_email", dupLead.ExistingID.String()))
		return
	}
	var amountPair *store.AmountCurrencyPairError
	if errors.As(err, &amountPair) {
		httperr.Write(w, r, httperr.Validation("currency", "amount_currency_pair", amountPair.Error()))
		return
	}
	var stageMismatch *store.StagePipelineMismatchError
	if errors.As(err, &stageMismatch) {
		httperr.Write(w, r, httperr.Validation("to_stage_id", "stage_not_in_pipeline", stageMismatch.Error()))
		return
	}
	var lostReason *store.LostReasonRequiredError
	if errors.As(err, &lostReason) {
		httperr.Write(w, r, httperr.Validation("lost_reason", "lost_reason_required", lostReason.Error()))
		return
	}
	var missingFx *store.MissingFxRateError
	if errors.As(err, &missingFx) {
		httperr.Write(w, r, httperr.Validation("fx_rate_to_base", "fx_rate_missing", missingFx.Error()))
		return
	}
	var badLink *store.InvalidLinkTypeError
	if errors.As(err, &badLink) {
		httperr.Write(w, r, httperr.Validation("links", "invalid_entity_type", badLink.Error()))
		return
	}
	var promoted *store.AlreadyPromotedError
	if errors.As(err, &promoted) {
		e := &httperr.DetailedError{
			Status: http.StatusConflict, Code: "already_promoted", Detail: promoted.Error(),
		}
		// The outcome pointer sits on the lead row the caller just proved
		// they can read, so echoing it discloses nothing new.
		if !promoted.PersonID.IsZero() {
			e.Details = map[string]any{"promoted_person_id": promoted.PersonID.String()}
		}
		httperr.Write(w, r, e)
		return
	}
	var needsIdentity *store.PromoteNeedsIdentityError
	if errors.As(err, &needsIdentity) {
		httperr.Write(w, r, httperr.Validation("lead", "identity_required", needsIdentity.Error()))
		return
	}
	var mergeSelf *store.MergeSelfError
	if errors.As(err, &mergeSelf) {
		httperr.Write(w, r, httperr.Validation("target_id", "merge_self", mergeSelf.Error()))
		return
	}
	var alreadyMerged *store.AlreadyMergedError
	if errors.As(err, &alreadyMerged) {
		e := &httperr.DetailedError{
			Status: http.StatusConflict, Code: "already_merged", Detail: alreadyMerged.Error(),
		}
		// The redirect pointer lives on the source row the caller just proved
		// they can address, so echoing it discloses nothing new (the
		// AlreadyPromoted precedent).
		if !alreadyMerged.IntoID.IsZero() {
			e.Details = map[string]any{"merged_into_id": alreadyMerged.IntoID.String()}
		}
		httperr.Write(w, r, e)
		return
	}
	var mergedTarget *store.MergedTargetError
	if errors.As(err, &mergedTarget) {
		httperr.Write(w, r, httperr.Validation("target_id", "merged_target", mergedTarget.Error()))
		return
	}
	var terminalStage *store.TerminalStageOnCreateError
	if errors.As(err, &terminalStage) {
		httperr.Write(w, r, httperr.Validation("stage_id", "terminal_stage_on_create", terminalStage.Error()))
		return
	}
	// Defense-in-depth net: a CHECK constraint is a business rule, so a
	// breach that slipped past the per-path validations still answers a
	// typed 422 naming the rule — never an opaque 500.
	if constraint, ok := store.CheckViolation(err); ok {
		httperr.Write(w, r, httperr.Validation(constraint, "constraint_violated",
			"the request violates the "+constraint+" business rule"))
		return
	}
	httperr.Write(w, r, err)
}
