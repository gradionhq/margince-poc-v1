// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// bookingConsentAdapter satisfies activities.ConsentCapturer over the
// consent module — the cross-module edge of the public booking capture
// path, injected here so activities never imports its sibling. The
// grant rides the normal consent write shape (proof row + audit +
// consent.changed), carrying the CaptureConsent passthrough verbatim.

import (
	"context"
	"errors"

	"github.com/gradionhq/margince/backend/internal/modules/activities"
	"github.com/gradionhq/margince/backend/internal/modules/consent"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

type bookingConsentAdapter struct {
	store *consent.Store
}

// bookingScopedPurposeKey is the ONE purpose an anonymous public booking
// form may assert consent for: the always-seeded `transactional` lane
// (the lawful channel for operational mail about the meeting). Admitting
// any tracked purpose let an unauthenticated caller who knows only a
// victim's email plant an effective first-party grant under an operator's
// non-DOI marketing purpose — forged consent authorizing outbound sends
// the address's owner never opted into. The edge is scoped to its own
// purpose so a stranger's submission can only ever touch the transactional
// lane, never escalate into a marketing grant.
const bookingScopedPurposeKey = "transactional"

// ValidatePurpose confirms the purpose exists AND is the booking-scoped
// `transactional` purpose BEFORE the surface writes anything — a public
// capture may not create a person it cannot attach a recordable consent
// to, and may not reach beyond its own consent lane.
func (a bookingConsentAdapter) ValidatePurpose(ctx context.Context, purposeID ids.UUID) error {
	purposes, err := a.store.ListPurposes(ctx)
	if err != nil {
		return err
	}
	return admitBookingPurpose(purposes, purposeID)
}

// admitBookingPurpose is the pure admission decision: the id must resolve
// within the tracked catalog and be the booking-scoped purpose. An
// unknown id and an out-of-scope purpose are both a 422 — neither leaks
// which of the two it was beyond what the caller already supplied.
func admitBookingPurpose(purposes []consent.Purpose, purposeID ids.UUID) error {
	for _, p := range purposes {
		if p.ID.UUID == purposeID {
			if p.Key != bookingScopedPurposeKey {
				return httperr.Validation("consent.purpose_id", "invalid",
					"public booking may only record consent for the transactional purpose")
			}
			return nil
		}
	}
	return httperr.Validation("consent.purpose_id", "invalid", "not a tracked consent purpose")
}

func (a bookingConsentAdapter) CaptureBookingConsent(ctx context.Context, personID ids.UUID, c activities.BookingConsent) error {
	source := "public_booking"
	_, err := a.store.Record(ctx, consent.RecordInput{
		PersonID:         ids.From[ids.PersonKind](personID),
		PurposeID:        ids.From[ids.PurposeKind](c.PurposeID),
		NewState:         "granted",
		Source:           &source,
		DoubleOptInToken: c.DoubleOptInToken,
		PolicyText:       c.Wording,
		PolicyVersion:    &c.PolicyVersion,
		// Anyone knowing an email can post this form: a decision already
		// on record — above all a withdrawal — must stand.
		NeverOverrideExisting: true,
	})
	// The consent module's client-fault type is its own; the booking
	// transport only knows the platform vocabulary — translate here so a
	// bad DOI token reads as the 422 it is, not a 500.
	var invalid *consent.ValidationError
	if errors.As(err, &invalid) {
		return httperr.Validation(invalid.Field, "invalid", invalid.Reason)
	}
	return err
}
