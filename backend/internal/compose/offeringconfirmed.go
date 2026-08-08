// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The cross-module edge from the growth fit to this workspace's own company
// record, injected here because that is where every such edge is injected.

import (
	"context"
	"errors"

	"github.com/gradionhq/margince/backend/internal/compose/orgdossier"
	"github.com/gradionhq/margince/backend/internal/modules/people"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
)

// offeringConfirmed reports whether this installation has described what it
// sells well enough for a growth fit to be measured against it (DOSS-AC-13).
//
// "Confirmed" is the anchor organization's own `minimum_complete`: a display
// name, an offer summary and an ideal-customer profile, each written by a
// human through the company form. That is the same bar onboarding uses to
// decide the installation has finished describing itself, so the growth fit
// and the onboarding flow cannot disagree about whether we know who we are.
//
// An installation that has never saved its company reads as NOT confirmed
// rather than as an error. The 404 from GetCompany is the onboarding signal,
// not a fault, and a workspace mid-onboarding should still get capped bands
// with the reason spelled out — not a broken panel.
func offeringConfirmed(store *people.Store) orgdossier.SelfOffering {
	return func(ctx context.Context) (bool, error) {
		company, err := store.GetCompany(ctx)
		if errors.Is(err, apperrors.ErrNotFound) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		return company.MinimumComplete, nil
	}
}
