// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package activities

// Handlers is the activities module's transport surface: the contract
// operations over the activity timeline. Wire concerns only — decode,
// validate, map store errors to the sentinel registry; the store owns
// the transactional write shape.

import (
	"context"
	"net/http"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

type Handlers struct {
	store *Store
	// emailDrafter is the compose-owned optional model drafting seam. Nil
	// preserves the deterministic draft, so an AI outage or unconfigured
	// deployment never blocks a user from preparing a reply.
	emailDrafter EmailDrafter
	// consent gates the send path; nil fails closed (WithConsent wires it).
	consent ConsentGate
	// delivery records an accepted send for transmission; nil fails closed
	// (WithDelivery wires it), because a 202 for a message nothing will
	// carry is a promise this surface cannot keep.
	delivery DeliveryStager
	// channelDelivery is the same seam for a messaging channel; nil fails
	// closed for the same reason (WithChannelDelivery wires it).
	channelDelivery ChannelDeliveryStager
	// timer wakes a scheduled send when it comes due; nil refuses to defer a
	// send (WithScheduleTimer wires it), because accepting "send it Monday"
	// with nothing to wake it is a promise this surface cannot keep.
	timer ScheduleTimer
	// The public-booking capture seams; nil fails closed
	// (WithPublicBooking wires them).
	publicPeople  PersonEnsurer
	publicConsent ConsentCapturer
	// uploadLimit is the deployment's ceiling for this module's upload route
	// (OPS-CFG-12), injected by WithUploadLimit. Zero refuses every upload,
	// which is the honest reading of "nobody has said" for a bound.
	uploadLimit int64
}

// EmailDrafter prepares a reply for an existing activity without sending it.
// Compose implements the optional model-backed path; activities retains the
// deterministic floor and the outbound send authority.
type EmailDrafter interface {
	DraftEmail(ctx context.Context, anchor ids.UUID, intent string) (subject, body string, err error)
}

// NewHandlers builds the module's HTTP surface over a workspace-bound handle.
func NewHandlers(db *database.DB) Handlers {
	return Handlers{store: NewStore(db)}
}

// WithTranscriptEnqueue lets a transcript POSTED to this transport start its
// own reading, the same way one written over the tool surface does.
//
// It is a separate builder call rather than a NewHandlers argument because the
// enqueue depends on the job runner, which the composition root only has after
// the handlers are built. Left unset, a transcript still lands — it is simply
// not read, which is what a deployment with no reading lane can offer.
func (h Handlers) WithTranscriptEnqueue(enqueue TranscriptReadEnqueue) Handlers {
	h.store = h.store.WithTranscriptEnqueue(enqueue)
	return h
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
	// An activity carries at most one project link (PROJ-AC-15), enforced by
	// a PARTIAL unique index on activity_id alone — which relink's ON CONFLICT
	// target cannot see, so a second project raises 23505 instead of being
	// absorbed. It is a business rule like any CHECK here, and relinkActivity
	// declares 422 for a refusal, so it answers as one: the caller re-issues
	// with replace_existing_of_type rather than reading a server fault.
	if constraint, ok := storekit.UniqueViolation(err); ok && constraint == "uq_activity_link_project" {
		httperr.Write(w, r, httperr.Validation("entity_id", "project_link_exists",
			"an activity links to at most one project — re-issue with replace_existing_of_type to move it"))
		return
	}
	// No CHECK arm below this one, deliberately. httperr's constraint net
	// answers every CHECK breach that reaches it, and it answers a held
	// activity as 423 locked carrying retain_until — which a module-local
	// "422 naming the rule" pre-empted, telling the caller to fix a value when
	// nothing they can send will work until the retention window closes.
	httperr.Write(w, r, err)
}
