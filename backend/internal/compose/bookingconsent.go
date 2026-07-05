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

// ValidatePurpose confirms the purpose exists BEFORE the surface writes
// anything — a public capture may not create a person it cannot attach
// a recordable consent to.
func (a bookingConsentAdapter) ValidatePurpose(ctx context.Context, purposeID ids.UUID) error {
	purposes, err := a.store.ListPurposes(ctx)
	if err != nil {
		return err
	}
	for _, p := range purposes {
		if p.ID == purposeID {
			return nil
		}
	}
	return httperr.Validation("consent.purpose_id", "invalid", "not a tracked consent purpose")
}

func (a bookingConsentAdapter) CaptureBookingConsent(ctx context.Context, personID ids.UUID, c activities.BookingConsent) error {
	source := "public_booking"
	_, err := a.store.Record(ctx, consent.RecordInput{
		PersonID:         personID,
		PurposeID:        c.PurposeID,
		NewState:         "granted",
		Source:           &source,
		DoubleOptInToken: c.DoubleOptInToken,
		PolicyText:       c.Wording,
		PolicyVersion:    &c.PolicyVersion,
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
