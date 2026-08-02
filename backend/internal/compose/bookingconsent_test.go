// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

import (
	"errors"
	"testing"

	"github.com/gradionhq/margince/backend/internal/modules/consent"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// The anonymous booking edge may assert consent for the booking-scoped
// `transactional` purpose only. A marketing (or any other) purpose, even
// though tracked, must be refused so a stranger who knows only a victim's
// email cannot plant an effective grant under it (F-008).
func TestAdmitBookingPurposeScopesToTransactional(t *testing.T) {
	transactional := consent.Purpose{ID: ids.New[ids.PurposeKind](), Key: bookingScopedPurposeKey}
	marketing := consent.Purpose{ID: ids.New[ids.PurposeKind](), Key: "marketing_email"}
	catalog := []consent.Purpose{transactional, marketing}

	if err := admitBookingPurpose(catalog, transactional.ID.UUID); err != nil {
		t.Fatalf("transactional purpose must be admitted, got %v", err)
	}

	if err := admitBookingPurpose(catalog, marketing.ID.UUID); err == nil {
		t.Fatal("a marketing purpose must be refused on the anonymous booking edge")
	} else if !isValidation(err, "consent.purpose_id") {
		t.Fatalf("out-of-scope purpose must be a consent.purpose_id validation fault, got %v", err)
	}

	unknown := ids.New[ids.PurposeKind]()
	if err := admitBookingPurpose(catalog, unknown.UUID); err == nil {
		t.Fatal("an untracked purpose id must be refused")
	} else if !isValidation(err, "consent.purpose_id") {
		t.Fatalf("unknown purpose must be a consent.purpose_id validation fault, got %v", err)
	}
}

// isValidation reports whether err is a 422 field-validation fault naming
// the given field — the booking edge's only client-fault shape here.
func isValidation(err error, field string) bool {
	var de *httperr.DetailedError
	if !errors.As(err, &de) || de.Code != "validation_error" {
		return false
	}
	for _, fieldErr := range de.Fields {
		if fieldErr.Field == field {
			return true
		}
	}
	return false
}
