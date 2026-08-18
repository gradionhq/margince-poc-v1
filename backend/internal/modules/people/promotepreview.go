// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package people

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// PreviewLeadPromotion answers what PromoteLead would do to this lead —
// merge into a person we already hold, or create one — by running the same
// PO-F-1 ladder promoteTarget runs, without writing (ADR-0119/A170).
//
// It returns a person, so it is a read and carries the row-scope gate: a
// matched person outside the caller's scope is withheld, and the response
// still says `merge`, because the outcome is a fact about the lead. A caller
// must never read an absent person as `create`.
func (s *Store) PreviewLeadPromotion(ctx context.Context, id ids.LeadID) (crmcontracts.PromoteLeadPreview, error) {
	if err := auth.Require(ctx, "lead", principal.ActionRead); err != nil {
		return crmcontracts.PromoteLeadPreview{}, err
	}
	active, err := s.activeColumns(ctx, "person")
	if err != nil {
		return crmcontracts.PromoteLeadPreview{}, err
	}
	out := crmcontracts.PromoteLeadPreview{Outcome: crmcontracts.PromoteLeadPreviewOutcomeCreate}
	err = s.tx(ctx, func(tx pgx.Tx) error {
		if err := auth.EnsureVisible(ctx, tx, "lead", id.UUID); err != nil {
			return err
		}
		// A decision read: the lead's core columns are what the ladder
		// consumes; custom columns play no part.
		lead, err := readLead(ctx, tx, id, storekit.IncludeArchived, nil)
		if err != nil {
			return fmt.Errorf("read lead before preview: %w", err)
		}
		if lead.Status == crmcontracts.LeadStatusPromoted {
			e := &AlreadyPromotedError{}
			if lead.PromotedPersonId != nil {
				e.PersonID = ids.From[ids.PersonKind](ids.UUID(*lead.PromotedPersonId))
			}
			return e
		}
		if lead.ArchivedAt != nil {
			// Disqualified: nothing promotion could do, so nothing to preview.
			return apperrors.ErrConflict
		}
		match, err := s.previewTarget(ctx, tx, lead)
		if err != nil {
			return err
		}
		if match.Decision != DecisionExactCollision {
			return nil
		}
		out.Outcome = crmcontracts.PromoteLeadPreviewOutcomeMerge
		// Two gates before a person is returned: the object grant (may this
		// role read people at all) and the row scope (may this caller see
		// THIS person). Failing either withholds. The outcome still says
		// merge, which promotion itself already discloses with its 409.
		grantErr := auth.Require(ctx, "person", principal.ActionRead)
		if grantErr != nil && !errors.Is(grantErr, apperrors.ErrPermissionDenied) {
			return grantErr
		}
		visible, err := auth.VisibleTo(ctx, tx, "person", match.PersonID.UUID)
		if err != nil {
			return err
		}
		if grantErr != nil || !visible {
			withheld := true
			out.PersonWithheld = &withheld
			return nil
		}
		person, err := readPerson(ctx, tx, match.PersonID, storekit.LiveOnly, active)
		if err != nil {
			return fmt.Errorf("read merge-target person: %w", err)
		}
		out.Person = &person
		return nil
	})
	return out, err
}

// previewTarget is the read half of promoteTarget: the same candidate the
// promotion would resolve, through the same ladder, so the preview and the
// promotion cannot disagree about who the lead already is.
func (s *Store) previewTarget(ctx context.Context, tx pgx.Tx, lead crmcontracts.Lead) (PersonResolution, error) {
	name := deref(lead.FullName)
	if name == "" && lead.Email != nil {
		name = string(*lead.Email)
	}
	var emails []string
	if lead.Email != nil {
		emails = []string{string(*lead.Email)}
	}
	consumerMail, err := s.consumerMailMatcher(ctx, tx)
	if err != nil {
		return PersonResolution{}, err
	}
	return DedupePerson(ctx, tx, PersonCandidate{
		FullName: name, Emails: emails, ConsumerMail: consumerMail,
	})
}
