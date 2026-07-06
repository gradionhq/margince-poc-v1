// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package webhooks

import (
	"errors"
	"net/http"

	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
)

func wireSubscription(s Subscription) crmcontracts.WebhookSubscription {
	return crmcontracts.WebhookSubscription{
		Id:          openapi_types.UUID(s.ID),
		WorkspaceId: openapi_types.UUID(s.WorkspaceID),
		OwnerId:     openapi_types.UUID(s.OwnerID),
		TargetUrl:   s.TargetURL,
		EventTypes:  s.EventTypes,
		State:       crmcontracts.WebhookSubscriptionState(s.State),
		Version:     s.Version,
		CreatedAt:   &s.CreatedAt,
		UpdatedAt:   &s.UpdatedAt,
		ArchivedAt:  s.ArchivedAt,
	}
}

func wireCreated(s Subscription, secret string) crmcontracts.WebhookSubscriptionCreated {
	return crmcontracts.WebhookSubscriptionCreated{
		Subscription:  wireSubscription(s),
		SigningSecret: secret,
	}
}

func wireDelivery(d Delivery) crmcontracts.WebhookDelivery {
	return crmcontracts.WebhookDelivery{
		Id:             openapi_types.UUID(d.ID),
		SubscriptionId: openapi_types.UUID(d.SubscriptionID),
		EventId:        openapi_types.UUID(d.EventID),
		EventType:      d.EventType,
		Status:         crmcontracts.WebhookDeliveryStatus(d.Status),
		Attempts:       d.Attempts,
		LastStatusCode: d.LastStatusCode,
		LastError:      d.LastError,
		NextRetryAt:    d.NextRetryAt,
		DeliveredAt:    d.DeliveredAt,
		DeadLetteredAt: d.DeadLetteredAt,
		CreatedAt:      &d.CreatedAt,
		UpdatedAt:      &d.UpdatedAt,
	}
}

// writeErr maps the module's typed faults onto the wire: a bad request is
// a 422; the not-configured case is a 503 (the feature needs a deployment
// signing key); everything else flows through the sentinel mapper.
func writeErr(w http.ResponseWriter, r *http.Request, err error) {
	var bad *BadInputError
	if errors.As(err, &bad) {
		httperr.Write(w, r, httperr.Validation(bad.Field, "invalid", bad.Reason))
		return
	}
	if errors.Is(err, ErrNotConfigured) {
		httperr.Write(w, r, &httperr.DetailedError{
			Status: http.StatusServiceUnavailable,
			Code:   "webhooks_not_configured",
			Detail: "outbound webhooks require a deployment signing key that is not configured",
		})
		return
	}
	httperr.Write(w, r, err)
}
