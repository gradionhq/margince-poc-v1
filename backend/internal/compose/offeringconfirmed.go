// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The cross-module edge from the growth fit to this workspace's own company
// record, injected here because that is where every such edge is injected.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"

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
	return func(ctx context.Context) (orgdossier.Offering, error) {
		company, err := store.GetCompany(ctx)
		if errors.Is(err, apperrors.ErrNotFound) {
			return orgdossier.Offering{}, nil
		}
		if err != nil {
			return orgdossier.Offering{}, err
		}
		return orgdossier.Offering{
			Confirmed:   company.MinimumComplete,
			Fingerprint: offeringFingerprint(company),
		}, nil
	}
}

// offeringFingerprint digests what this workspace says it sells, so a cached
// growth fit is invalidated when that changes.
//
// It hashes rather than carries the text: the value rides a cache key that
// nothing renders, and an offering in a fingerprint cannot be mistaken for one
// the assessment may quote. Field names are sorted so the same profile digests
// the same way across processes, which a map iteration would not.
func offeringFingerprint(company people.Company) string {
	names := make([]string, 0, len(company.Fields))
	for name := range company.Fields {
		names = append(names, name)
	}
	sort.Strings(names)
	digested := fmt.Appendf(nil, "%s\x00", company.DisplayName)
	for _, name := range names {
		digested = fmt.Appendf(digested, "%s\x00%s\x00", name, company.Fields[name])
	}
	sum := sha256.Sum256(digested)
	return hex.EncodeToString(sum[:])
}
