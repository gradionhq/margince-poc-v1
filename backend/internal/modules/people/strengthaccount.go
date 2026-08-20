// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package people

// The ACCOUNT's roll-up, split from the per-person score in strength.go
// because they answer different questions and are gated differently. One
// person's strength is a fact about that person; an account's is a fold over
// the contacts a caller may see, and it therefore turns on the employment
// EDGE — which is why the edge grant is asked here and nowhere in the
// per-person path.

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// AccountStrength is the §4 org roll-up: the strongest current contact's
// score, which contact carries it, and how many contacts it was chosen
// from. The two extra facts exist because the number alone is not
// actionable on an account — the rep needs to know whose relationship it is.
type AccountStrength struct {
	RelationshipStrength
	// ContributorPersonID names the contact whose relationship the score is.
	// It is nil when there is no relationship to attribute: no contact the
	// caller can read, or none of them has ever interacted. A dormant
	// account has no carrier, and inventing one would read as a claim.
	ContributorPersonID *ids.PersonID
	ContactCount        int
}

// OrganizationStrength is the §4 org roll-up: the MAX over the org's
// current employees' strengths — one strong relationship makes the
// account warm; an average would dilute it. A contact outside the caller's
// row scope contributes nothing, so the roll-up never out-sees the contact
// list.
func (s *Store) OrganizationStrength(ctx context.Context, orgID ids.OrganizationID, now time.Time) (AccountStrength, error) {
	if err := auth.Require(ctx, "organization", principal.ActionRead); err != nil {
		return AccountStrength{}, err
	}
	var out AccountStrength
	err := s.db.Tx(ctx, func(tx pgx.Tx) error {
		if err := auth.EnsureVisible(ctx, tx, "organization", orgID.UUID); err != nil {
			return err
		}
		var err error
		out, err = AccountStrengthFor(ctx, tx, orgID, now)
		return err
	})
	if err != nil {
		return AccountStrength{}, err
	}
	return out, nil
}

// AccountStrengthFor is OrganizationStrength's body without the
// transaction or the organization gate, so a composite read that already
// opened one transaction and already gated the account computes the same
// roll-up inside it rather than opening a second one at a second instant.
func AccountStrengthFor(ctx context.Context, tx pgx.Tx, orgID ids.OrganizationID, now time.Time) (AccountStrength, error) {
	// The EDGE grant is asked here and REFUSED, where the person grant below is
	// swallowed into a dormant answer. The two look alike and are not: a caller
	// who may not read people sees an account with nobody they may read, and
	// dormant is the true roll-up over that empty set. A caller who may not read
	// EDGES cannot tell which people work here at all, so the roll-up is not
	// dormant — it is unknown, and answering `none` would state as a fact about
	// the account something this caller was refused the means to compute.
	//
	// It is asked before the read rather than sorted out from its error, because
	// StrengthForOrgContacts returns one sentinel for both refusals and the
	// difference between them is the whole point.
	if err := auth.Require(ctx, "relationship", principal.ActionRead); err != nil {
		return AccountStrength{}, err
	}
	contacts, err := StrengthForOrgContacts(ctx, tx, orgID, now)
	if errors.Is(err, apperrors.ErrPermissionDenied) {
		// A caller holding organization:read but not person:read sees an
		// account with no contacts they may read, so the roll-up is dormant
		// with nobody behind it. Refusing here instead would newly 403 a
		// route that has answered this shape since it shipped.
		return AccountStrength{RelationshipStrength: RelationshipStrength{Bucket: bucketNone}}, nil
	}
	if err != nil {
		return AccountStrength{}, err
	}
	return FoldAccountStrength(contacts), nil
}

// FoldAccountStrength picks the strongest contact out of an already-read
// contact set, so a caller that needs both the per-contact scores and the
// account roll-up pays for the underlying read once.
//
// Picking the strongest: A contributor is named
// only when there is a relationship to attribute: an account whose contacts
// have never interacted is dormant, and naming one of them as the carrier
// of a zero would invent a relationship that does not exist.
func FoldAccountStrength(contacts []ContactStrength) AccountStrength {
	out := AccountStrength{
		RelationshipStrength: RelationshipStrength{Bucket: bucketNone},
		ContactCount:         len(contacts),
	}
	for i := range contacts {
		c := contacts[i]
		if c.Strength.LastInteraction == nil {
			continue
		}
		if out.ContributorPersonID != nil && c.Strength.Strength <= out.Strength {
			continue
		}
		out.RelationshipStrength = c.Strength
		personID := c.PersonID
		out.ContributorPersonID = &personID
	}
	return out
}
