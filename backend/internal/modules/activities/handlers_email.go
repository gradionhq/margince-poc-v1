// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package activities

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// ConsentGate is the outbound suppression seam (B-EP07.12): the
// consent module implements it, the composition root injects it. A
// send path constructed WITHOUT one fails closed — absence of the gate
// must never read as consent.
type ConsentGate interface {
	RequireGrantedForEmails(ctx context.Context, recipients []string, purposeKey string) error
}

// WithConsent returns handlers whose send path consults the given
// authority. Compose calls this; the zero Handlers value keeps sends
// suppressed.
func (h Handlers) WithConsent(gate ConsentGate) Handlers {
	h.consent = gate
	return h
}

func (h Handlers) DraftEmail(w http.ResponseWriter, r *http.Request, id crmcontracts.Id) {
	var req struct {
		Intent *string `json:"intent"`
	}
	if r.ContentLength > 0 && !httperr.Decode(w, r, &req) {
		return
	}
	activity, err := h.store.GetActivity(r.Context(), pathID[ids.ActivityKind](id), storekit.LiveOnly)
	if err != nil {
		writeStoreErr(w, r, err)
		return
	}

	// A deterministic draft over the activity's own context. The
	// model-backed voice draft rides the router once the API role wires
	// a model path; drafting is 🟢 and never sends either way.
	subject := "Re: follow-up"
	if activity.Subject != nil && *activity.Subject != "" {
		subject = "Re: " + *activity.Subject
	}
	var body strings.Builder
	body.WriteString("Hi,\n\nfollowing up on ")
	if activity.Subject != nil && *activity.Subject != "" {
		fmt.Fprintf(&body, "%q", *activity.Subject)
	} else {
		body.WriteString("our last conversation")
	}
	body.WriteString(".")
	if req.Intent != nil && strings.TrimSpace(*req.Intent) != "" {
		body.WriteString("\n\n" + strings.TrimSpace(*req.Intent))
	}
	body.WriteString("\n\nBest regards")

	replyTo := openapi_types.UUID(ids.UUID(id))
	httperr.WriteJSON(w, http.StatusOK, crmcontracts.EmailDraft{
		Subject:             subject,
		Body:                body.String(),
		InReplyToActivityId: &replyTo,
	})
}

func (h Handlers) SendEmail(w http.ResponseWriter, r *http.Request, id crmcontracts.Id, _ crmcontracts.SendEmailParams) {
	var req crmcontracts.SendEmailRequest
	if !httperr.Decode(w, r, &req) {
		return
	}
	recipients := make([]string, 0, len(req.To))
	for _, addr := range req.To {
		recipients = append(recipients, string(addr))
	}
	if req.Cc != nil {
		for _, addr := range *req.Cc {
			recipients = append(recipients, string(addr))
		}
	}

	// RFC 8058 one-click unsubscribe (features/06 §1.2 AC-D3): a marketing
	// send carries the List-Unsubscribe header pair and a visible footer,
	// keyed on the primary recipient's preference token. A locked
	// (transactional) purpose, or an address no person carries, yields no
	// token — a transactional message has nothing to unsubscribe from, and
	// an address the gate will refuse below discloses nothing. Both header
	// and footer derive from unsubscribeURL so they cannot diverge.
	body := req.Body
	var listUnsubscribe, listUnsubscribePost string
	if h.unsubscribe != nil && len(recipients) > 0 {
		token, ok, err := h.unsubscribe.UnsubscribeToken(r.Context(), recipients[0], req.ConsentPurpose)
		if err != nil {
			writeStoreErr(w, r, err)
			return
		}
		if ok {
			if h.publicBaseURL == "" {
				// Fail loudly rather than derive the base from the request:
				// the link carries the recipient's unsubscribe token, and a
				// marketing send may not go out without a working, non-
				// forgeable List-Unsubscribe URL (features/06 §1.2).
				httperr.Write(w, r, fmt.Errorf("send: public base URL is not configured; a marketing send must carry a working List-Unsubscribe URL"))
				return
			}
			unsubURL := unsubscribeURL(h.publicBaseURL, token, req.ConsentPurpose)
			listUnsubscribe, listUnsubscribePost = listUnsubscribeHeaders(unsubURL)
			body = appendUnsubscribeFooter(body, h.publicBaseURL, token, unsubURL)
		}
	}

	sent, err := h.store.SendEmail(r.Context(), pathID[ids.ActivityKind](id), SendEmailInput{
		Recipients:     recipients,
		Subject:        req.Subject,
		Body:           body,
		ConsentPurpose: req.ConsentPurpose,
	}, h.consent)
	if err != nil {
		writeStoreErr(w, r, err)
		return
	}
	if listUnsubscribe != "" {
		w.Header().Set("List-Unsubscribe", listUnsubscribe)
		w.Header().Set("List-Unsubscribe-Post", listUnsubscribePost)
	}
	// 202: accepted for delivery, the activity is the durable fact.
	httperr.WriteJSON(w, http.StatusAccepted, sent)
}
