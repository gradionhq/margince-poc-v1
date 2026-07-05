// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package activities

// The anonymous booking surface (feedback/14 — B-EP09.14 book.html):
// an unguessable slug resolves to (workspace, host) with no session and
// no workspace header, the availability read answers free/busy slots
// only, and the booking POST captures the booker as a person
// (idempotent on email), records the MANDATORY consent passthrough, and
// books the slot — three governed writes in sequence, each audited on
// its own path. A 409 after person+consent stands: the subject DID
// submit the form and the consent (capture semantics, recorded in the
// batch decision file).

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// BookingPage is the slug's resolution: which workspace, whose calendar.
type BookingPage struct {
	WorkspaceID ids.UUID
	HostUserID  ids.UUID
}

// ResolveBookingPage answers the slug→tenant lookup the public
// middleware binds the workspace from. booking_page is deliberately
// outside RLS (it IS the resolver — 0036); an unknown or revoked slug
// reads as absent.
func (s *Store) ResolveBookingPage(ctx context.Context, slug string) (BookingPage, error) {
	var page BookingPage
	err := database.WithInfraTx(ctx, s.pool, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx,
			`SELECT workspace_id, host_user_id FROM booking_page WHERE slug = $1 AND revoked_at IS NULL`,
			slug).Scan(&page.WorkspaceID, &page.HostUserID)
		if errors.Is(err, pgx.ErrNoRows) {
			return apperrors.ErrNotFound
		}
		return err
	})
	if err != nil {
		return BookingPage{}, err
	}
	return page, nil
}

// SeedBookingPageTx mints a host's public page inside the caller's
// transaction (the workspace bootstrap seeds one for the admin). The
// slug is a public identifier, not a credential — unguessable so the
// URL cannot be enumerated, stored plaintext because it IS the URL.
func SeedBookingPageTx(ctx context.Context, tx pgx.Tx, hostUserID ids.UUID) (string, error) {
	var buf [24]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", fmt.Errorf("activities: booking slug entropy: %w", err)
	}
	slug := base64.RawURLEncoding.EncodeToString(buf[:])
	if _, err := tx.Exec(ctx, `
		INSERT INTO booking_page (workspace_id, host_user_id, slug)
		VALUES (NULLIF(current_setting('app.workspace_id', true), '')::uuid, $1, $2)`,
		hostUserID, slug); err != nil {
		return "", fmt.Errorf("activities: seed booking page: %w", err)
	}
	return slug, nil
}

// PersonEnsurer is the people seam of the public capture path (compose
// injects it — activities never imports a sibling).
type PersonEnsurer interface {
	EnsurePersonByEmail(ctx context.Context, fullName, email, source string) (ids.UUID, error)
}

// BookingConsent is the CaptureConsent passthrough: the purpose and the
// exact wording/version the anonymous booker was shown.
type BookingConsent struct {
	PurposeID        ids.UUID
	PolicyVersion    string
	Wording          *string
	DoubleOptInToken *string
}

// ConsentCapturer records the booker's consent grant (the consent
// module behind a seam). ValidatePurpose runs BEFORE any write so a
// bogus purpose refuses the whole capture — no person row without a
// recordable consent.
type ConsentCapturer interface {
	ValidatePurpose(ctx context.Context, purposeID ids.UUID) error
	CaptureBookingConsent(ctx context.Context, personID ids.UUID, consent BookingConsent) error
}

// WithPublicBooking wires the public capture seams.
func (h Handlers) WithPublicBooking(people PersonEnsurer, consent ConsentCapturer) Handlers {
	h.publicPeople = people
	h.publicConsent = consent
	return h
}

// GetPublicAvailability implements (GET /public/booking/{host_slug}/availability).
// The middleware already resolved the slug and bound workspace + the
// system principal; the handler re-resolves for the host id (the
// lookup is the same global read) and answers slots only.
func (h Handlers) GetPublicAvailability(w http.ResponseWriter, r *http.Request, hostSlug string, params crmcontracts.GetPublicAvailabilityParams) {
	page, err := h.store.ResolveBookingPage(r.Context(), hostSlug)
	if err != nil {
		httperr.Write(w, r, err)
		return
	}
	duration := defaultSlotDuration
	if params.DurationMinutes != nil {
		duration = time.Duration(*params.DurationMinutes) * time.Minute
	}
	slots, err := h.store.Availability(r.Context(), page.HostUserID, params.From, params.To, duration)
	if err != nil {
		writeStoreErr(w, r, err)
		return
	}
	if slots == nil {
		slots = []slot{}
	}
	httperr.WriteJSON(w, http.StatusOK, map[string]any{"slots": slots})
}

// BookPublicMeeting implements (POST /public/booking/{host_slug}):
// consent-shape check → person (idempotent on email) → consent grant →
// booking. The 201 discloses nothing beyond the slot itself.
func (h Handlers) BookPublicMeeting(w http.ResponseWriter, r *http.Request, hostSlug string, _ crmcontracts.BookPublicMeetingParams) {
	if h.publicPeople == nil || h.publicConsent == nil {
		// Fail closed: a process role composed without the capture seams
		// must refuse, not book without consent.
		httperr.Write(w, r, apperrors.ErrPermissionDenied)
		return
	}
	page, err := h.store.ResolveBookingPage(r.Context(), hostSlug)
	if err != nil {
		httperr.Write(w, r, err)
		return
	}

	var req crmcontracts.BookPublicMeetingJSONRequestBody
	if !httperr.Decode(w, r, &req) {
		return
	}
	if req.Booker.Name == "" || req.Booker.Email == "" {
		httperr.Write(w, r, httperr.Validation("booker", "required", "booker.name and booker.email are required"))
		return
	}
	if !req.End.After(req.Start) {
		httperr.Write(w, r, httperr.Validation("end", "invalid", "end must follow start"))
		return
	}
	// Consent is mandatory and validated BEFORE any write: a public
	// capture surface may not create a person it cannot attach a
	// recordable consent to.
	if req.Consent.PolicyVersion == "" {
		httperr.Write(w, r, httperr.Validation("consent.policy_version", "required", "the consent wording version shown to the booker is required"))
		return
	}
	purposeID := ids.UUID(req.Consent.PurposeId)
	if err := h.publicConsent.ValidatePurpose(r.Context(), purposeID); err != nil {
		writeStoreErr(w, r, err)
		return
	}

	personID, err := h.publicPeople.EnsurePersonByEmail(r.Context(), req.Booker.Name, string(req.Booker.Email), "public_booking")
	if err != nil {
		writeStoreErr(w, r, err)
		return
	}
	if err := h.publicConsent.CaptureBookingConsent(r.Context(), personID, BookingConsent{
		PurposeID:        purposeID,
		PolicyVersion:    req.Consent.PolicyVersion,
		Wording:          req.Consent.Wording,
		DoubleOptInToken: req.Consent.DoubleOptInToken,
	}); err != nil {
		writeStoreErr(w, r, err)
		return
	}

	subject := "Meeting"
	if req.Subject != nil && *req.Subject != "" {
		subject = *req.Subject
	}
	_, err = h.store.BookMeeting(r.Context(), BookMeetingInput{
		Host:    page.HostUserID,
		Start:   req.Start,
		End:     req.End,
		Subject: subject,
		Links:   []ActivityLinkInput{{EntityType: "person", EntityID: personID}},
	})
	if err != nil {
		var slotTaken *SlotTakenError
		if errors.As(err, &slotTaken) {
			httperr.Write(w, r, httperr.Duplicate("slot_taken", ""))
			return
		}
		writeStoreErr(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusCreated, map[string]any{
		"start": req.Start, "end": req.End,
	})
}
