// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The cross-module edge from the growth fit to this workspace's own company
// record, injected here because that is where every such edge is injected.

import (
	"context"
	"errors"

	"github.com/gradionhq/margince/backend/internal/compose/orgdossier"
	"github.com/gradionhq/margince/backend/internal/modules/ai"
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
	return func(ctx context.Context) (orgdossier.Offering, error) {
		company, err := store.GetCompany(ctx)
		if errors.Is(err, apperrors.ErrNotFound) {
			return orgdossier.Offering{}, nil
		}
		if err != nil {
			return orgdossier.Offering{}, err
		}
		fingerprint, err := offeringFingerprint(ctx, store)
		if err != nil {
			return orgdossier.Offering{}, err
		}
		return orgdossier.Offering{Confirmed: company.MinimumComplete, Fingerprint: fingerprint}, nil
	}
}

// offeringFingerprint digests what this workspace says it sells, so a cached
// growth fit is invalidated when that changes.
//
// It is the fingerprint of the COMPANY CONTEXT the growth-fit task actually
// sends, resolved from that task's own declared scopes — not a second digest
// rolled here. The two would drift, and the way they would drift is silent:
// this surface's own profile fields are only part of what the context carries.
// It also folds in the anchor's offering and signal FACTS, and a digest that
// covered the fields alone would let a new proof point change what the model is
// told we sell while every cached band stayed exactly where it was.
//
// The content never leaves as text — a fit derived from what WE sell is an
// assessment about THEM and must still cite their records (DOSS-AC-6). Only the
// digest travels, onto a cache key nothing renders.
func offeringFingerprint(ctx context.Context, store *people.Store) (string, error) {
	scopes, err := companyContextScopesFor(ai.TaskGrowthFit)
	if err != nil {
		return "", err
	}
	assembled, err := store.GetCompanyContext(ctx, scopes)
	if err != nil {
		return "", err
	}
	return assembled.Fingerprint, nil
}
