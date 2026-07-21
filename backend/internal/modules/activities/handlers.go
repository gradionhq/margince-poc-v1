// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package activities

// Handlers is the activities module's transport surface: the contract
// operations over the activity timeline. Wire concerns only — decode,
// validate, map store errors to the sentinel registry; the store owns
// the transactional write shape.

import (
	"context"
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/ports/extraction"
)

type Handlers struct {
	store *Store
	// emailDrafter is the compose-owned optional model drafting seam. Nil
	// preserves the deterministic draft, so an AI outage or unconfigured
	// deployment never blocks a user from preparing a reply.
	emailDrafter EmailDrafter
	// consent gates the send path; nil fails closed (WithConsent wires it).
	consent ConsentGate
	// The public-booking capture seams; nil fails closed
	// (WithPublicBooking wires them).
	publicPeople  PersonEnsurer
	publicConsent ConsentCapturer
	// unsubscribe builds the RFC 8058 List-Unsubscribe URL for a marketing
	// send; nil means no unsubscribe header (WithUnsubscribe wires it).
	unsubscribe UnsubscribeLinker
	// publicBaseURL is the canonical scheme+host the tokenized unsubscribe
	// link resolves to — configured at boot, never taken from the request
	// (WithPublicBaseURL wires it).
	publicBaseURL string
	// extractor is the staged AI-extraction seam (RD-T10); nil falls back to
	// extraction.NoOpExtractor (WithExtractor wires a real one).
	extractor extraction.Extractor
}

// EmailDrafter prepares a reply for an existing activity without sending it.
// Compose implements the optional model-backed path; activities retains the
// deterministic floor and the outbound send authority.
type EmailDrafter interface {
	DraftEmail(ctx context.Context, anchor ids.UUID, intent string) (subject, body string, err error)
}

func NewHandlers(pool *pgxpool.Pool) Handlers {
	return Handlers{store: NewStore(pool)}
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
	var badLink *InvalidLinkTypeError
	if errors.As(err, &badLink) {
		httperr.Write(w, r, httperr.Validation("links", "invalid_entity_type", badLink.Error()))
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
