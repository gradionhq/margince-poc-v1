package activities

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
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
	activity, err := h.store.GetActivity(r.Context(), ids.UUID(id), false)
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
	if h.consent == nil {
		// Fail closed: a send surface without its suppression gate is a
		// wiring defect, not an implicit allow.
		httperr.Write(w, r, fmt.Errorf("send path has no consent authority wired: %w", apperrors.ErrConsentNotGranted))
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
	// Authorization FIRST, consent second: the anchor's visibility and
	// the write grant must refuse before the consent gate answers, or
	// the 409-vs-403 difference becomes a consent oracle for callers
	// with no rights at all.
	anchor, err := h.store.GetActivity(r.Context(), ids.UUID(id), false)
	if err != nil {
		writeStoreErr(w, r, err)
		return
	}
	if err := auth.Require(r.Context(), "activity", principal.ActionCreate); err != nil {
		writeStoreErr(w, r, err)
		return
	}
	if err := h.consent.RequireGrantedForEmails(r.Context(), recipients, req.ConsentPurpose); err != nil {
		writeStoreErr(w, r, err)
		return
	}
	var links []ActivityLinkInput
	if anchor.Links != nil {
		links = make([]ActivityLinkInput, 0, len(*anchor.Links))
		for _, l := range *anchor.Links {
			links = append(links, ActivityLinkInput{EntityType: string(l.EntityType), EntityID: ids.UUID(l.EntityId)})
		}
	}

	direction := "outbound"
	sent, _, err := h.store.LogActivity(r.Context(), LogActivityInput{
		Kind:      "email",
		Subject:   &req.Subject,
		Body:      &req.Body,
		Direction: &direction,
		Links:     links,
		Source:    "manual",
	})
	if err != nil {
		writeStoreErr(w, r, err)
		return
	}
	// The transport hand-off is the deployment's SMTP/provider seam;
	// V1 logs the governed, consent-checked send. 202: accepted for
	// delivery, the activity is the durable fact.
	httperr.WriteJSON(w, http.StatusAccepted, sent)
}
