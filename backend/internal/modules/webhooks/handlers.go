// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package webhooks

import (
	"net/http"

	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// Handlers is the module's transport slice; compose embeds it so the
// generated webhook stubs are shadowed by real code. The store owns the
// CRUD write shape; the deliverer (which may be nil when unconfigured)
// owns the on-demand replay of a parked delivery.
type Handlers struct {
	store     *Store
	deliverer *Deliverer
}

// NewHandlers wires the transport over a store and its deliverer. Both
// share the same *Store so replay reuses the CRUD gate + write shape.
func NewHandlers(store *Store, deliverer *Deliverer) Handlers {
	return Handlers{store: store, deliverer: deliverer}
}

// ListWebhookSubscriptions lists the workspace's subscriptions (RBAC-gated,
// existence-hiding); the signing secret is never in this view.
func (h Handlers) ListWebhookSubscriptions(w http.ResponseWriter, r *http.Request, params crmcontracts.ListWebhookSubscriptionsParams) {
	archived := storekit.LiveOnly
	if params.IncludeArchived != nil && *params.IncludeArchived {
		archived = storekit.IncludeArchived
	}
	subs, err := h.store.ListSubscriptions(r.Context(), archived)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	data := make([]crmcontracts.WebhookSubscription, 0, len(subs))
	for _, s := range subs {
		data = append(data, wireSubscription(s))
	}
	httperr.WriteJSON(w, http.StatusOK, crmcontracts.WebhookSubscriptionListResponse{
		Data: data, Page: crmcontracts.PageInfo{HasMore: false},
	})
}

// CreateWebhookSubscription registers a subscription and returns the
// one-time signing secret; 503 when no deployment key is configured.
func (h Handlers) CreateWebhookSubscription(w http.ResponseWriter, r *http.Request, _ crmcontracts.CreateWebhookSubscriptionParams) {
	var req crmcontracts.CreateWebhookSubscriptionRequest
	if !httperr.Decode(w, r, &req) {
		return
	}
	sub, secret, err := h.store.CreateSubscription(r.Context(), CreateSubscriptionInput{
		TargetURL:  req.TargetUrl,
		EventTypes: req.EventTypes,
	})
	if err != nil {
		writeErr(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusCreated, wireCreated(sub, secret))
}

// GetWebhookSubscription returns one subscription by id, or 404 when it is
// archived, absent, or outside the caller's scope (existence-hiding).
func (h Handlers) GetWebhookSubscription(w http.ResponseWriter, r *http.Request, id crmcontracts.Id) {
	sub, err := h.store.GetSubscription(r.Context(), ids.UUID(id))
	if err != nil {
		writeErr(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, wireSubscription(sub))
}

// UpdateWebhookSubscription pauses/resumes or re-targets a subscription,
// guarded by the If-Match version.
func (h Handlers) UpdateWebhookSubscription(w http.ResponseWriter, r *http.Request, id crmcontracts.Id, _ crmcontracts.UpdateWebhookSubscriptionParams) {
	ifVersion, ok := httperr.IfMatchVersion(w, r)
	if !ok {
		return
	}
	var req crmcontracts.UpdateWebhookSubscriptionRequest
	if !httperr.Decode(w, r, &req) {
		return
	}
	in := UpdateSubscriptionInput{EventTypes: req.EventTypes, IfVersion: ifVersion}
	if req.State != nil {
		state := string(*req.State)
		in.State = &state
	}
	sub, err := h.store.UpdateSubscription(r.Context(), ids.UUID(id), in)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, wireSubscription(sub))
}

// ArchiveWebhookSubscription archives a subscription, stopping all delivery.
func (h Handlers) ArchiveWebhookSubscription(w http.ResponseWriter, r *http.Request, id crmcontracts.Id) {
	sub, err := h.store.ArchiveSubscription(r.Context(), ids.UUID(id))
	if err != nil {
		writeErr(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, wireSubscription(sub))
}

// RotateWebhookSecret mints a new signing secret and returns it once; 503
// when no deployment key is configured.
func (h Handlers) RotateWebhookSecret(w http.ResponseWriter, r *http.Request, id crmcontracts.Id) {
	sub, secret, err := h.store.RotateSecret(r.Context(), ids.UUID(id))
	if err != nil {
		writeErr(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, wireCreated(sub, secret))
}

// ListWebhookDeliveries returns a subscription's delivery attempts
// newest-first — the dead-letter inspection surface.
func (h Handlers) ListWebhookDeliveries(w http.ResponseWriter, r *http.Request, id crmcontracts.Id, params crmcontracts.ListWebhookDeliveriesParams) {
	limit := 0
	if params.Limit != nil {
		limit = *params.Limit
	}
	deliveries, hasMore, err := h.store.ListDeliveries(r.Context(), ids.UUID(id), limit)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	data := make([]crmcontracts.WebhookDelivery, 0, len(deliveries))
	for _, d := range deliveries {
		data = append(data, wireDelivery(d))
	}
	httperr.WriteJSON(w, http.StatusOK, crmcontracts.WebhookDeliveryListResponse{
		Data: data, Page: crmcontracts.PageInfo{HasMore: hasMore},
	})
}

// ReplayWebhookDelivery re-attempts a parked delivery on demand; 503 when
// no deployment key is configured to sign it.
func (h Handlers) ReplayWebhookDelivery(w http.ResponseWriter, r *http.Request, id crmcontracts.Id, deliveryID openapi_types.UUID) {
	// Replay itself refuses (ErrNotConfigured → 503) when no signing key is
	// configured, before touching state — the handler need not pre-check.
	delivery, err := h.deliverer.Replay(r.Context(), ids.UUID(id), ids.UUID(deliveryID))
	if err != nil {
		writeErr(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, wireDelivery(delivery))
}
