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

// DraftResult carries generated copy and the profile provenance needed for
// later learning from the sent edit.
type DraftResult struct {
	Subject             string
	Body                string
	DraftRef            string
	VoiceProfileVersion *int
	AIGenerated         bool
}

// DraftFeedback records the final text for a previously generated draft.
type DraftFeedback interface {
	RecordSentDraft(context.Context, string, string) error
}

// DraftComposer is the compose-owned, voice-aware email drafting seam.
type DraftComposer interface {
	DraftEmail(context.Context, string, string) (DraftResult, error)
}

// WithDrafter returns handlers using the shared drafting orchestrator.
func (h Handlers) WithDrafter(drafter DraftComposer) Handlers {
	h.drafter = drafter
	return h
}

// WithDraftFeedback returns handlers that capture the sent version of drafts.
func (h Handlers) WithDraftFeedback(feedback DraftFeedback) Handlers {
	h.draftFeedback = feedback
	return h
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

	topic := ""
	if activity.Subject != nil {
		topic = *activity.Subject
	}
	intent := ""
	if req.Intent != nil {
		intent = *req.Intent
	}
	result := defaultDraft(topic, intent)
	if h.drafter != nil {
		result, err = h.drafter.DraftEmail(r.Context(), topic, intent)
	}
	if err != nil {
		writeStoreErr(w, r, err)
		return
	}

	replyTo := openapi_types.UUID(ids.UUID(id))
	httperr.WriteJSON(w, http.StatusOK, crmcontracts.EmailDraft{
		Subject:             result.Subject,
		Body:                result.Body,
		DraftRef:            result.DraftRef,
		InReplyToActivityId: &replyTo,
		VoiceProfileVersion: result.VoiceProfileVersion,
		AiGenerated:         result.AIGenerated,
	})
}

func defaultDraft(topic, intent string) DraftResult {
	subject := "Re: follow-up"
	if topic != "" {
		subject = "Re: " + topic
	}
	var body strings.Builder
	body.WriteString("Hi,\n\nFollowing up on ")
	if topic != "" {
		fmt.Fprintf(&body, "%q", topic)
	} else {
		body.WriteString("our last conversation")
	}
	body.WriteString(".")
	if strings.TrimSpace(intent) != "" {
		body.WriteString("\n\n" + strings.TrimSpace(intent))
	}
	body.WriteString("\n\nBest regards")
	return DraftResult{Subject: subject, Body: body.String(), DraftRef: ids.NewV7().String()}
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
	if req.DraftRef != nil && h.draftFeedback != nil {
		if err := h.draftFeedback.RecordSentDraft(r.Context(), *req.DraftRef, req.Body); err != nil {
			// Delivery is already committed. Do not misreport a successful send
			// as failed because optional learning could not be recorded.
			w.Header().Set("X-Margince-Voice-Learning", "failed")
		}
	}
	if listUnsubscribe != "" {
		w.Header().Set("List-Unsubscribe", listUnsubscribe)
		w.Header().Set("List-Unsubscribe-Post", listUnsubscribePost)
	}
	// 202: accepted for delivery, the activity is the durable fact.
	httperr.WriteJSON(w, http.StatusAccepted, sent)
}
