// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package webhooks

import (
	"encoding/json"
	"fmt"

	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	kevents "github.com/gradionhq/margince/backend/internal/shared/kernel/events"
)

// toWireEnvelope maps the INTERNAL bus envelope (events.md §2 — the shape
// every module publishes to the outbox) onto the PUBLIC delivery envelope a
// webhook subscriber receives (public-events.yaml's PublicEventEnvelope,
// generated into crmcontracts). The two shapes are deliberately different:
// the internal envelope carries governance metadata a subscriber must never
// see — audit_log_id, causation_id, passport_id, on_behalf_of, workspace_id,
// the exact fields envelope_contract_test.go pins as forbidden on the wire —
// and a fuller actor than the public contract exposes. Only the documented
// public fields survive the trip; everything else is dropped, not merely
// hidden by omitempty.
//
// This is applied ONLY at enqueue time for a freshly observed bus event
// (HandleEvent). A delivery, once enqueued, stores its marshaled body and
// every retry/replay re-sends that stored body verbatim — so a delivery
// enqueued before this mapping existed keeps its pre-migration (internal-
// shape) body forever; toWireEnvelope is never applied retroactively.
func toWireEnvelope(env kevents.Envelope) (crmcontracts.PublicEventEnvelope, error) {
	var data map[string]interface{}
	if len(env.Payload) > 0 {
		if err := json.Unmarshal(env.Payload, &data); err != nil {
			// env.Payload is JSON this same process marshaled moments earlier
			// from a typed PublicEvent* struct (the module stores' emit
			// sites); a decode failure here means that contract broke
			// upstream, not a wire input to tolerate — surface it rather than
			// silently deliver a truncated envelope.
			return crmcontracts.PublicEventEnvelope{}, fmt.Errorf("webhooks: decoding payload for event %s as the public data field: %w", env.EventID, err)
		}
	}
	return crmcontracts.PublicEventEnvelope{
		EventId:    openapi_types.UUID(env.EventID),
		Type:       env.Type,
		Version:    env.Version,
		OccurredAt: env.OccurredAt,
		// Actor is reduced to its public Type — the internal ID (which
		// embeds a UUID), PassportID and OnBehalfOf never leave the process.
		Actor: crmcontracts.PublicEventActor{Type: env.Actor.Type},
		Entity: crmcontracts.PublicEventEntityRef{
			Type: env.Entity.Type,
			Id:   openapi_types.UUID(env.Entity.ID),
		},
		CorrelationId: openapi_types.UUID(env.Trace.CorrelationID),
		Data:          data,
	}, nil
}
