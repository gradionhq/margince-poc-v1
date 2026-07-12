// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package quotas

// The transport surface: wire concerns only — decode, validate, map
// store errors to the sentinel registry; the store owns the
// transactional write shape and the attainment read owns the
// computation.

import (
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"
	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// Handlers is the quotas module's transport surface: the six contract
// operations over the quota aggregate and its attainment sub-resource.
type Handlers struct {
	store *Store
}

// NewHandlers wires the transport over the RLS-bound app pool.
func NewHandlers(pool *pgxpool.Pool) Handlers {
	return Handlers{store: NewStore(pool)}
}

// pageInfo renders the store's keyset page onto the contract's PageInfo
// envelope — this module's own copy of the one-per-module spelling
// (people/deals/activities/signals each carry their own).
func pageInfo(p storekit.Page) crmcontracts.PageInfo {
	info := crmcontracts.PageInfo{HasMore: p.HasMore}
	if p.NextCursor != "" {
		info.NextCursor = &p.NextCursor
	}
	return info
}

// uuidArg widens an optional wire UUID (body field or query parameter)
// to the store's plain ids.UUID; nil stays nil. Quota carries no
// ids.EntityKind (T3's decision — a non-polymorphic-link target needs
// none), so this is the plain-UUID counterpart of the sibling modules'
// idArg[K].
func uuidArg(u *openapi_types.UUID) *ids.UUID {
	if u == nil {
		return nil
	}
	v := ids.UUID(*u)
	return &v
}

// ListQuotas serves listQuotas: the workspace's quotas, keyset-paginated,
// optionally narrowed by owner_id/team_id.
func (h Handlers) ListQuotas(w http.ResponseWriter, r *http.Request, params crmcontracts.ListQuotasParams) {
	in := ListQuotasInput{
		Cursor:          params.Cursor,
		Limit:           params.Limit,
		OwnerID:         uuidArg(params.OwnerId),
		TeamID:          uuidArg(params.TeamId),
		IncludeArchived: params.IncludeArchived != nil && *params.IncludeArchived,
		Sort:            params.Sort,
	}
	list, page, err := h.store.ListQuotas(r.Context(), in)
	if err != nil {
		writeQuotaErr(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, crmcontracts.QuotaListResponse{Data: list, Page: pageInfo(page)})
}

// CreateQuota serves createQuota: human session only (x-agent-access:
// human-only) — the store's OwnerXorTeamError maps to the contract's
// 422 owner_xor_team_required shape.
func (h Handlers) CreateQuota(w http.ResponseWriter, r *http.Request, _ crmcontracts.CreateQuotaParams) {
	var req crmcontracts.CreateQuotaRequest
	if !httperr.Decode(w, r, &req) {
		return
	}
	in := CreateQuotaInput{
		OwnerID:     uuidArg(req.OwnerId),
		TeamID:      uuidArg(req.TeamId),
		PeriodStart: req.PeriodStart.Time,
		PeriodEnd:   req.PeriodEnd.Time,
		TargetMinor: req.TargetMinor,
		Currency:    req.Currency,
	}
	quota, err := h.store.CreateQuota(r.Context(), in)
	if err != nil {
		writeQuotaErr(w, r, err)
		return
	}
	w.Header().Set("Location", "/v1/quotas/"+quota.Id.String())
	httperr.WriteJSON(w, http.StatusCreated, quota)
}

// GetQuota serves getQuota.
func (h Handlers) GetQuota(w http.ResponseWriter, r *http.Request, id crmcontracts.Id) {
	// IncludeArchived — the house single-get convention (GetProduct/GetDeal):
	// an archived quota stays fetchable by id, it just drops from lists.
	quota, err := h.store.GetQuota(r.Context(), ids.UUID(id), storekit.IncludeArchived)
	if err != nil {
		writeQuotaErr(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, quota)
}

// UpdateQuota serves updateQuota: a merge-PATCH, human session only, that
// re-validates the owner-XOR-team contract on the MERGED state.
func (h Handlers) UpdateQuota(w http.ResponseWriter, r *http.Request, id crmcontracts.Id, _ crmcontracts.UpdateQuotaParams) {
	ifVersion, ok := httperr.IfMatchVersion(w, r)
	if !ok {
		return
	}
	var req crmcontracts.UpdateQuotaRequest
	if !httperr.Decode(w, r, &req) {
		return
	}
	in := UpdateQuotaInput{
		OwnerID:     uuidArg(req.OwnerId),
		TeamID:      uuidArg(req.TeamId),
		TargetMinor: req.TargetMinor,
		Currency:    req.Currency,
		IfVersion:   ifVersion,
	}
	if req.PeriodStart != nil {
		in.PeriodStart = &req.PeriodStart.Time
	}
	if req.PeriodEnd != nil {
		in.PeriodEnd = &req.PeriodEnd.Time
	}
	quota, err := h.store.UpdateQuota(r.Context(), ids.UUID(id), in)
	if err != nil {
		writeQuotaErr(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, quota)
}

// ArchiveQuota serves archiveQuota: human session only, 200 + the full
// (now-archived) entity — never 204.
func (h Handlers) ArchiveQuota(w http.ResponseWriter, r *http.Request, id crmcontracts.Id) {
	quota, err := h.store.ArchiveQuota(r.Context(), ids.UUID(id))
	if err != nil {
		writeQuotaErr(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, quota)
}

// GetQuotaAttainment serves getQuotaAttainment: the live, server-computed
// attainment read (never a cached or invented figure).
func (h Handlers) GetQuotaAttainment(w http.ResponseWriter, r *http.Request, id crmcontracts.Id) {
	att, err := h.store.QuotaAttainment(r.Context(), ids.UUID(id))
	if err != nil {
		writeQuotaErr(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, attainmentWire(att))
}

// attainmentWire renders the store's Attainment onto the contract's
// QuotaAttainment: AsOfDate narrows from the store's full UTC instant to
// the wire's date-only field (the contract names it a date, not a
// date-time — the instant itself is an internal computation detail).
func attainmentWire(a Attainment) crmcontracts.QuotaAttainment {
	dealsWire := make([]crmcontracts.QuotaAttainmentDeal, len(a.ContributingDeals))
	for i, d := range a.ContributingDeals {
		dealsWire[i] = crmcontracts.QuotaAttainmentDeal{
			DealId:         openapi_types.UUID(d.DealID),
			BaseValueMinor: d.BaseValueMinor,
		}
	}
	return crmcontracts.QuotaAttainment{
		QuotaId:           openapi_types.UUID(a.QuotaID),
		ClosedWonMinor:    a.ClosedWonMinor,
		TargetMinor:       a.TargetMinor,
		Currency:          a.Currency,
		AttainmentPct:     float32(a.AttainmentPct),
		GapMinor:          a.GapMinor,
		PacePct:           float32(a.PacePct),
		Band:              crmcontracts.QuotaAttainmentBand(a.Band),
		AsOfDate:          openapi_types.Date{Time: a.AsOfDate},
		ContributingDeals: dealsWire,
	}
}

// writeQuotaErr maps this module's typed refusals onto the wire shapes
// the contract names, then falls through to httperr.Write's sentinel
// registry — which already resolves apperrors.ErrNotFound (absent/
// foreign-tenant/unknown owner-or-team ref), apperrors.ErrVersionSkew (a
// stale If-Match), and apperrors.ErrPermissionDenied (an RBAC deny);
// quotas adds no branch for any of those.
func writeQuotaErr(w http.ResponseWriter, r *http.Request, err error) {
	var xor *OwnerXorTeamError
	if errors.As(err, &xor) {
		httperr.Write(w, r, httperr.Validation("owner_id", "owner_xor_team_required", xor.Error()))
		return
	}
	if errors.Is(err, ErrAttainmentTargetZero) {
		// Detail text matches the crm.yaml targetZero example verbatim — a
		// zero-target refusal, never a division-by-zero 500.
		httperr.Write(w, r, &httperr.DetailedError{
			Status: http.StatusUnprocessableEntity,
			Code:   "attainment_target_zero",
			Detail: "This quota's target_minor is zero; attainment is refused rather than computed against a zero denominator.",
		})
		return
	}
	if errors.Is(err, ErrAttainmentComputationFailed) {
		// The actionable wire detail names the failure mode, never the FX
		// pair or as-of day the store's wrapped error carries for the log —
		// matches the crm.yaml computationFailed example verbatim.
		httperr.Write(w, r, &httperr.DetailedError{
			Status: http.StatusUnprocessableEntity,
			Code:   "attainment_computation_failed",
			Detail: "The clean-core closed-won query failed; showing a stale or guessed figure is never acceptable — retry.",
		})
		return
	}
	// Defense-in-depth net: a CHECK constraint is a business rule (e.g. a
	// lowercase currency, never pre-validated by the store — see the T3
	// report), so a breach still answers a typed 422 naming the rule —
	// never an opaque 500 (the deals/handlers.go precedent).
	if constraint, ok := storekit.CheckViolation(err); ok {
		httperr.Write(w, r, httperr.Validation(constraint, "constraint_violated",
			"the request violates the "+constraint+" business rule"))
		return
	}
	httperr.Write(w, r, err)
}
