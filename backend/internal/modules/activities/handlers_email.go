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
	"github.com/gradionhq/margince/backend/internal/shared/ports/connector"
)

// ConsentGate is the outbound suppression seam (B-EP07.12): the
// consent module implements it, the composition root injects it. A
// send path constructed WITHOUT one fails closed — absence of the gate
// must never read as consent.
//
// ONE gate serves both transports, with two spellings of the same question,
// because the alternative is two default-deny checks — and the one that stopped
// applying would look exactly like the one that passes. Mail asks in addresses
// because that is what a mail surface holds; a channel recipient has no address,
// so the channel reply asks in recipients (connector.Recipient), which is the
// union of the two vocabularies.
type ConsentGate interface {
	RequireGrantedForEmails(ctx context.Context, recipients []string, purposeKey string) error
	RequireGrantedForRecipients(ctx context.Context, recipients []connector.Recipient, purposeKey string) error
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

// WithSendAuthority returns handlers whose send paths pre-flight the credential
// they are about to transmit through, so a sender with no send-capable mailbox —
// or a workspace with no bot bound — is refused with an actionable message
// instead of accepting a message that can only park.
func (h Handlers) WithSendAuthority(authority SendAuthority) Handlers {
	h.store = h.store.WithSendAuthority(authority)
	return h
}

// WithRecipientDirectory returns handlers whose account-started sends resolve
// every typed address to a person the sender can read, so a rep is told which
// address is not on file instead of mailing someone the record cannot name.
func (h Handlers) WithRecipientDirectory(dir RecipientDirectory) Handlers {
	h.store = h.store.WithRecipientDirectory(dir)
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

// SendAccountEmail starts a NEW conversation from a record rather than
// answering one. It differs from SendEmail in exactly two places — the origin
// it builds and the links that origin carries — and shares the send itself,
// so the consent gate, deliverability and the staging transaction cannot
// drift between the two surfaces (ADR-0087 §1).
func (h Handlers) SendAccountEmail(w http.ResponseWriter, r *http.Request, _ crmcontracts.SendAccountEmailParams) {
	var req crmcontracts.SendAccountEmailRequest
	if !httperr.Decode(w, r, &req) {
		return
	}
	links := make([]ActivityLinkInput, 0, len(req.Links))
	for _, l := range req.Links {
		links = append(links, ActivityLinkInput{EntityType: string(l.EntityType), EntityID: ids.UUID(l.EntityId)})
	}
	if len(links) == 0 {
		// A message filed under nothing is one nobody finds again, which is
		// the gap this operation exists to close. The contract says minItems,
		// and nothing in this stack validates a request against the schema.
		writeStoreErr(w, r, &RequiredFieldError{Field: "links"})
		return
	}

	sent, err := h.store.SendEmail(r.Context(), FromAccount(links), sendInputFrom(
		req.To, req.Cc, req.Subject, req.Body, req.ConsentPurpose, req.DraftRef,
	), h.consent, h.delivery)
	if err != nil {
		writeStoreErr(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusAccepted, sent)
}

// sendInputFrom builds the send's input from the fields both mail surfaces
// carry. One spelling, because the merged consent list is a rule rather than
// a convenience: consent is owed to every addressee, so Recipients is To+Cc
// and Cc is a subset of it. A second hand-rolled copy would eventually merge
// one of them differently and mail somebody the gate never asked about.
func sendInputFrom(to []openapi_types.Email, cc *[]openapi_types.Email, subject, body, purpose string, draftRef *string) SendEmailInput {
	var ccAddresses []string
	if cc != nil {
		ccAddresses = make([]string, 0, len(*cc))
		for _, addr := range *cc {
			ccAddresses = append(ccAddresses, string(addr))
		}
	}
	recipients := make([]string, 0, len(to)+len(ccAddresses))
	for _, addr := range to {
		recipients = append(recipients, string(addr))
	}
	recipients = append(recipients, ccAddresses...)

	ref := ""
	if draftRef != nil {
		ref = *draftRef
	}
	return SendEmailInput{
		Recipients:     recipients,
		Cc:             ccAddresses,
		Subject:        subject,
		Body:           body,
		ConsentPurpose: purpose,
		DraftRef:       ref,
	}
}

// SendEmail answers an existing conversation: the activity in the path is the
// anchor whose threading chain the reply continues and whose record links it
// inherits. Its account-started twin above shares everything after the origin.
func (h Handlers) SendEmail(w http.ResponseWriter, r *http.Request, id crmcontracts.Id, _ crmcontracts.SendEmailParams) {
	var req crmcontracts.SendEmailRequest
	if !httperr.Decode(w, r, &req) {
		return
	}
	// Deliverability — the RFC 8058 header and the visible footer — is
	// derived by the store, on the message, where the MCP send tool reaches
	// it too. It belongs on the mail, not on this response to the API
	// caller, who is not the recipient and has nothing to unsubscribe from.
	sent, err := h.store.SendEmail(r.Context(), FromActivity(pathID[ids.ActivityKind](id)), sendInputFrom(
		req.To, req.Cc, req.Subject, req.Body, req.ConsentPurpose, req.DraftRef,
	), h.consent, h.delivery)
	if err != nil {
		writeStoreErr(w, r, err)
		return
	}
	// 202: accepted for delivery, the activity is the durable fact.
	httperr.WriteJSON(w, http.StatusAccepted, sent)
}
