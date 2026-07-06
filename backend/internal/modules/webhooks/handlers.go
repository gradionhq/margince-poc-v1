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

func (h Handlers) CreateWebhookSubscription(w http.ResponseWriter, r *http.Request) {
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

func (h Handlers) GetWebhookSubscription(w http.ResponseWriter, r *http.Request, id crmcontracts.Id) {
	sub, err := h.store.GetSubscription(r.Context(), ids.UUID(id))
	if err != nil {
		writeErr(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, wireSubscription(sub))
}

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

func (h Handlers) ArchiveWebhookSubscription(w http.ResponseWriter, r *http.Request, id crmcontracts.Id) {
	sub, err := h.store.ArchiveSubscription(r.Context(), ids.UUID(id))
	if err != nil {
		writeErr(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, wireSubscription(sub))
}

func (h Handlers) RotateWebhookSecret(w http.ResponseWriter, r *http.Request, id crmcontracts.Id) {
	sub, secret, err := h.store.RotateSecret(r.Context(), ids.UUID(id))
	if err != nil {
		writeErr(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, wireCreated(sub, secret))
}

func (h Handlers) ListWebhookDeliveries(w http.ResponseWriter, r *http.Request, id crmcontracts.Id, params crmcontracts.ListWebhookDeliveriesParams) {
	limit := 0
	if params.Limit != nil {
		limit = *params.Limit
	}
	deliveries, err := h.store.ListDeliveries(r.Context(), ids.UUID(id), limit)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	data := make([]crmcontracts.WebhookDelivery, 0, len(deliveries))
	for _, d := range deliveries {
		data = append(data, wireDelivery(d))
	}
	httperr.WriteJSON(w, http.StatusOK, crmcontracts.WebhookDeliveryListResponse{
		Data: data, Page: crmcontracts.PageInfo{HasMore: false},
	})
}

func (h Handlers) ReplayWebhookDelivery(w http.ResponseWriter, r *http.Request, id crmcontracts.Id, deliveryId openapi_types.UUID) {
	if h.deliverer == nil {
		writeErr(w, r, ErrNotConfigured)
		return
	}
	delivery, err := h.deliverer.Replay(r.Context(), ids.UUID(id), ids.UUID(deliveryId))
	if err != nil {
		writeErr(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, wireDelivery(delivery))
}
