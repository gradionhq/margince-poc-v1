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

// WithDelivery returns handlers whose send path records an accepted message
// for transmission. Compose calls this; the zero Handlers value refuses to
// send rather than log an activity claiming a message went out.
func (h Handlers) WithDelivery(stager DeliveryStager) Handlers {
	h.delivery = stager
	return h
}

// WithMailbox returns handlers whose send path pre-flights the sender's
// mailbox grant, so a user with no send-capable mailbox is refused with an
// actionable message instead of accepting mail that can only park.
func (h Handlers) WithMailbox(authority MailboxAuthority) Handlers {
	h.store = h.store.WithMailbox(authority)
	return h
}

// WithEmailDrafter returns handlers whose draft endpoint uses the injected
// compose path. Drafting only proposes text; the send endpoint remains a
// separate consent-gated operation.
func (h Handlers) WithEmailDrafter(drafter EmailDrafter) Handlers {
	h.emailDrafter = drafter
	return h
}

// DraftResult is one prepared draft with its provenance: whether a model
// produced it (Art. 50 disclosure) and which Voice DNA version styled it.
type DraftResult struct {
	Subject             string
	Body                string
	AIGenerated         bool
	AIDisclosure        *string
	VoiceProfileVersion *int
	// DraftRef identifies this served voice draft for learning feedback
	// (rejectVoiceDraft); nil when no voice profile styled it.
	DraftRef *string
}

// ProvenanceEmailDrafter is the richer drafting seam: same draft, plus the
// provenance the HTTP response stamps. A drafter that implements it is
// preferred over the plain EmailDrafter shape; the plain seam stays for
// consumers (agents, automation) whose surfaces carry text only.
type ProvenanceEmailDrafter interface {
	DraftEmailWithProvenance(ctx context.Context, anchor ids.UUID, intent string) (DraftResult, error)
}

func (h Handlers) DraftEmail(w http.ResponseWriter, r *http.Request, id crmcontracts.Id) {
	var req struct {
		Intent *string `json:"intent"`
	}
	if r.ContentLength > 0 && !httperr.Decode(w, r, &req) {
		return
	}
	intent := ""
	if req.Intent != nil {
		intent = *req.Intent
	}
	result, err := h.prepareEmailDraft(r.Context(), ids.UUID(id), intent)
	if err != nil {
		writeStoreErr(w, r, err)
		return
	}

	replyTo := openapi_types.UUID(ids.UUID(id))
	httperr.WriteJSON(w, http.StatusOK, crmcontracts.EmailDraft{
		Subject:             result.Subject,
		Body:                result.Body,
		InReplyToActivityId: &replyTo,
		AiGenerated:         &result.AIGenerated,
		AiDisclosure:        result.AIDisclosure,
		VoiceProfileVersion: result.VoiceProfileVersion,
		DraftRef:            result.DraftRef,
	})
}

func (h Handlers) prepareEmailDraft(ctx context.Context, anchor ids.UUID, intent string) (DraftResult, error) {
	if provenance, ok := h.emailDrafter.(ProvenanceEmailDrafter); ok {
		return provenance.DraftEmailWithProvenance(ctx, anchor, intent)
	}
	if h.emailDrafter != nil {
		subject, body, err := h.emailDrafter.DraftEmail(ctx, anchor, intent)
		return DraftResult{Subject: subject, Body: body}, err
	}
	activity, err := h.store.GetActivity(ctx, ids.From[ids.ActivityKind](anchor), storekit.LiveOnly)
	if err != nil {
		return DraftResult{}, err
	}
	topic := ""
	if activity.Subject != nil {
		topic = *activity.Subject
	}
	subject, body := DeterministicEmailDraft(topic, intent)
	return DraftResult{Subject: subject, Body: body}, nil
}

// DeterministicEmailDraft is the shared no-model floor for every drafting
// transport. Compose calls it when the model lane is absent or unavailable,
// so HTTP, MCP, and automation cannot drift into different fallback text.
func DeterministicEmailDraft(topic, intent string) (subject, body string) {
	subject = "Re: follow-up"
	if topic != "" {
		subject = "Re: " + topic
	}
	var b strings.Builder
	b.WriteString("Hi,\n\nfollowing up on ")
	if topic != "" {
		fmt.Fprintf(&b, "%q", topic)
	} else {
		b.WriteString("our last conversation")
	}
	b.WriteString(".")
	if strings.TrimSpace(intent) != "" {
		b.WriteString("\n\n" + strings.TrimSpace(intent))
	}
	b.WriteString("\n\nBest regards")
	return subject, b.String()
}

func (h Handlers) SendEmail(w http.ResponseWriter, r *http.Request, id crmcontracts.Id, _ crmcontracts.SendEmailParams) {
	var req crmcontracts.SendEmailRequest
	if !httperr.Decode(w, r, &req) {
		return
	}
	// Recipients is the merged addressee list the consent gate answers on;
	// cc travels separately so the delivery can render a To: and a Cc: line
	// that address each person once.
	var cc []string
	if req.Cc != nil {
		cc = make([]string, 0, len(*req.Cc))
		for _, addr := range *req.Cc {
			cc = append(cc, string(addr))
		}
	}
	recipients := make([]string, 0, len(req.To)+len(cc))
	for _, addr := range req.To {
		recipients = append(recipients, string(addr))
	}
	recipients = append(recipients, cc...)

	// The contract field is nullable, and its absence is the ordinary case:
	// mail composed without a served draft resolves no learning signal, which
	// the empty string says to the send path.
	draftRef := ""
	if req.DraftRef != nil {
		draftRef = *req.DraftRef
	}

	// Deliverability — the RFC 8058 header and the visible footer — is
	// derived by the store, on the message, where the MCP send tool reaches
	// it too. It belongs on the mail, not on this response to the API
	// caller, who is not the recipient and has nothing to unsubscribe from.
	sent, err := h.store.SendEmail(r.Context(), pathID[ids.ActivityKind](id), SendEmailInput{
		Recipients:     recipients,
		Cc:             cc,
		Subject:        req.Subject,
		Body:           req.Body,
		ConsentPurpose: req.ConsentPurpose,
		DraftRef:       draftRef,
	}, h.consent, h.delivery)
	if err != nil {
		writeStoreErr(w, r, err)
		return
	}
	// 202: accepted for delivery, the activity is the durable fact.
	httperr.WriteJSON(w, http.StatusAccepted, sent)
}
