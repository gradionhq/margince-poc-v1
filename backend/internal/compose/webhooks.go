// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

import (
	nethttp "net/http"

	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
)

// webhooksHandlers is the contract-first placeholder for the outbound
// webhook surface (E10/S-E10.6, B-E10.13). The `/webhook-subscriptions`
// contract ships one phase ahead of its module: until the `webhooks`
// module lands (B-E10.13a-c) and shadows these operations, every call
// answers a loud 501 — never a silent 404, and never a half-working
// surface. It is embedded in Server so ServerInterface stays fully
// covered; the module's real Handlers replace this whole set in phase 2.
type webhooksHandlers struct{}

func (webhooksHandlers) ListWebhookSubscriptions(w nethttp.ResponseWriter, r *nethttp.Request, _ crmcontracts.ListWebhookSubscriptionsParams) {
	httperr.NotImplemented(w, r, "ListWebhookSubscriptions")
}

func (webhooksHandlers) CreateWebhookSubscription(w nethttp.ResponseWriter, r *nethttp.Request) {
	httperr.NotImplemented(w, r, "CreateWebhookSubscription")
}

func (webhooksHandlers) GetWebhookSubscription(w nethttp.ResponseWriter, r *nethttp.Request, _ crmcontracts.Id) {
	httperr.NotImplemented(w, r, "GetWebhookSubscription")
}

func (webhooksHandlers) UpdateWebhookSubscription(w nethttp.ResponseWriter, r *nethttp.Request, _ crmcontracts.Id, _ crmcontracts.UpdateWebhookSubscriptionParams) {
	httperr.NotImplemented(w, r, "UpdateWebhookSubscription")
}

func (webhooksHandlers) ArchiveWebhookSubscription(w nethttp.ResponseWriter, r *nethttp.Request, _ crmcontracts.Id) {
	httperr.NotImplemented(w, r, "ArchiveWebhookSubscription")
}

func (webhooksHandlers) RotateWebhookSecret(w nethttp.ResponseWriter, r *nethttp.Request, _ crmcontracts.Id) {
	httperr.NotImplemented(w, r, "RotateWebhookSecret")
}

func (webhooksHandlers) ListWebhookDeliveries(w nethttp.ResponseWriter, r *nethttp.Request, _ crmcontracts.Id, _ crmcontracts.ListWebhookDeliveriesParams) {
	httperr.NotImplemented(w, r, "ListWebhookDeliveries")
}

func (webhooksHandlers) ReplayWebhookDelivery(w nethttp.ResponseWriter, r *nethttp.Request, _ crmcontracts.Id, _ openapi_types.UUID) {
	httperr.NotImplemented(w, r, "ReplayWebhookDelivery")
}
