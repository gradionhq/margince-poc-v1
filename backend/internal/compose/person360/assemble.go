// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// Package person360 assembles the person record page in one round trip —
// the person half of the one-composite-read doctrine (PO-EXT-3).
//
// It is the organization 360's sibling and deliberately its mirror: one
// workspace transaction so the sections describe one moment, a mandatory
// root read whose refusal is the whole read's refusal, and every other
// section attempted independently and OMITTED-AND-NAMED when the caller
// lacks its grant. Empty and forbidden are different facts, and a page
// that renders them the same way tells the reader the relationship is
// cold when it is only invisible.
package person360

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/modules/ai"
	"github.com/gradionhq/margince/backend/internal/modules/consent"
	"github.com/gradionhq/margince/backend/internal/modules/people"
	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// sectionCap bounds every nested collection. These are summaries, not
// paging surfaces: page two comes from the endpoint that owns the
// collection, with its own cursor vocabulary.
const sectionCap = 25

// Service assembles the composite read from the module stores it composes.
type Service struct {
	pool    *pgxpool.Pool
	people  *people.Store
	consent *consent.Store
	// feedback is the correction ledger, consulted so a moment a human
	// dismissed does not come back.
	feedback *ai.FeedbackStore
	now      func() time.Time
}

// NewService binds the composite read to its module stores. now is the
// injected clock — a test pins a fixed instant so a strength half-life
// cannot flake between seeding and reading.
func NewService(
	pool *pgxpool.Pool,
	peopleStore *people.Store,
	consentStore *consent.Store,
	feedbackStore *ai.FeedbackStore,
	now func() time.Time,
) *Service {
	return &Service{pool: pool, people: peopleStore, consent: consentStore, feedback: feedbackStore, now: now}
}

// Assemble reads the whole person page inside ONE workspace transaction.
func (s *Service) Assemble(ctx context.Context, personID ids.PersonID) (crmcontracts.Person360, error) {
	now := s.now().UTC()
	out := crmcontracts.Person360{
		AsOf:            now,
		SectionsOmitted: []crmcontracts.Person360SectionsOmitted{},
	}
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		person, err := s.people.GetPersonTx(ctx, tx, personID, storekit.LiveOnly)
		if err != nil {
			return err
		}
		out.Person = person

		for _, section := range s.sections(personID, now) {
			if err := section.read(ctx, tx, &out); err != nil {
				// A section the caller may not read is named, not returned
				// empty. Any other failure is the whole read's failure —
				// half a record page is worse than an error, because the
				// reader cannot tell which half is missing.
				if errors.Is(err, apperrors.ErrPermissionDenied) {
					out.SectionsOmitted = append(out.SectionsOmitted, section.name)
					continue
				}
				return fmt.Errorf("person 360 section %q: %w", section.name, err)
			}
		}
		return nil
	})
	if err != nil {
		return crmcontracts.Person360{}, err
	}
	return out, nil
}

// section is one independently-authorized part of the page.
type section struct {
	name crmcontracts.Person360SectionsOmitted
	read func(ctx context.Context, tx pgx.Tx, out *crmcontracts.Person360) error
}

func (s *Service) sections(personID ids.PersonID, now time.Time) []section {
	return []section{
		{name: crmcontracts.Person360SectionsOmittedStrength, read: func(ctx context.Context, tx pgx.Tx, out *crmcontracts.Person360) error {
			return s.strengthSection(ctx, tx, personID, now, out)
		}},
		{name: crmcontracts.Person360SectionsOmittedRelationshipChanges, read: func(ctx context.Context, tx pgx.Tx, out *crmcontracts.Person360) error {
			return s.relationshipChangesSection(ctx, tx, personID, now, out)
		}},
		{name: crmcontracts.Person360SectionsOmittedEmployments, read: func(ctx context.Context, tx pgx.Tx, out *crmcontracts.Person360) error {
			return s.employmentsSection(ctx, tx, personID, out)
		}},
		{name: crmcontracts.Person360SectionsOmittedDealRoles, read: func(ctx context.Context, tx pgx.Tx, out *crmcontracts.Person360) error {
			return s.dealRolesSection(ctx, tx, personID, out)
		}},
		{name: crmcontracts.Person360SectionsOmittedActivities, read: func(ctx context.Context, tx pgx.Tx, out *crmcontracts.Person360) error {
			return s.activitiesSection(ctx, tx, personID, out)
		}},
		{name: crmcontracts.Person360SectionsOmittedNextSteps, read: func(ctx context.Context, tx pgx.Tx, out *crmcontracts.Person360) error {
			return s.nextStepsSection(ctx, tx, personID, out)
		}},
		{name: crmcontracts.Person360SectionsOmittedLastTouch, read: func(ctx context.Context, tx pgx.Tx, out *crmcontracts.Person360) error {
			return s.lastTouchSection(ctx, tx, personID, out)
		}},
		{name: crmcontracts.Person360SectionsOmittedNetwork, read: func(ctx context.Context, tx pgx.Tx, out *crmcontracts.Person360) error {
			return s.networkSection(ctx, tx, personID, now, out)
		}},
		{name: crmcontracts.Person360SectionsOmittedConsent, read: func(ctx context.Context, tx pgx.Tx, out *crmcontracts.Person360) error {
			return s.consentSection(ctx, tx, personID, out)
		}},
		{name: crmcontracts.Person360SectionsOmittedProfileFields, read: func(ctx context.Context, tx pgx.Tx, out *crmcontracts.Person360) error {
			return s.profileFieldsSection(ctx, tx, personID, out)
		}},
		{name: crmcontracts.Person360SectionsOmittedSinceLastVisit, read: func(ctx context.Context, tx pgx.Tx, out *crmcontracts.Person360) error {
			return s.sinceLastVisitSection(ctx, tx, personID, out)
		}},
		// Both of these run BEFORE the moments below, because the ladder's
		// rules read them: the meeting-prep rung asks what is booked, and the
		// missing-next-step rung asks whether an open deal has nothing
		// scheduled on it.
		{name: crmcontracts.Person360SectionsOmittedCommercial, read: func(ctx context.Context, tx pgx.Tx, out *crmcontracts.Person360) error {
			return s.commercialSection(ctx, tx, personID, out)
		}},
		{name: crmcontracts.Person360SectionsOmittedNextMeeting, read: func(ctx context.Context, tx pgx.Tx, out *crmcontracts.Person360) error {
			return s.nextMeetingSection(ctx, tx, personID, now, out)
		}},
		// LAST, and it has to be: the moments are derived from what the
		// sections above gathered, so a moment can never cite evidence this
		// page is not showing, and a section withheld for want of a grant
		// contributes no moments rather than leaking through one.
		{name: crmcontracts.Person360SectionsOmittedMoments, read: func(ctx context.Context, tx pgx.Tx, out *crmcontracts.Person360) error {
			return s.momentsSection(ctx, tx, personID, now, out)
		}},
	}
}

// requireRead is the object grant for a section. A denial returns the
// sentinel unchanged so Assemble can name the section rather than fail.
func requireRead(ctx context.Context, object string) error {
	return auth.Require(ctx, object, principal.ActionRead)
}
