// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package org360

import (
	"context"
	"errors"
	"math"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/modules/activities"
	"github.com/gradionhq/margince/backend/internal/modules/approvals"
	"github.com/gradionhq/margince/backend/internal/modules/people"
	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// The section names, spelled once. They are the contract's
// sections_omitted vocabulary and the keys the assembly reasons about, so
// a rename cannot leave the two halves disagreeing.
const (
	sectionPeople          = crmcontracts.Organization360SectionsOmitted("people")
	sectionDeals           = crmcontracts.Organization360SectionsOmitted("deals")
	sectionStrength        = crmcontracts.Organization360SectionsOmitted("strength")
	sectionActivities      = crmcontracts.Organization360SectionsOmitted("activities")
	sectionTags            = crmcontracts.Organization360SectionsOmitted("tags")
	sectionListMemberships = crmcontracts.Organization360SectionsOmitted("list_memberships")
	sectionApprovals       = crmcontracts.Organization360SectionsOmitted("pending_approvals")
	sectionNextSteps       = crmcontracts.Organization360SectionsOmitted("next_steps")
	sectionSinceLastVisit  = crmcontracts.Organization360SectionsOmitted("since_last_visit")
)

// Service assembles the 360 and maintains the visit baseline.
type Service struct {
	pool      *pgxpool.Pool
	people    *people.Store
	approvals *approvals.Service
	now       func() time.Time
}

// NewService binds the composite read to the module stores it composes.
// now is the read's injected clock (the house shape: a test pins a fixed
// instant so a strength half-life or a stall window cannot flake between
// seeding and reading).
func NewService(pool *pgxpool.Pool, peopleStore *people.Store, approvalsSvc *approvals.Service, now func() time.Time) *Service {
	return &Service{pool: pool, people: peopleStore, approvals: approvalsSvc, now: now}
}

// Assemble reads the whole company page inside ONE workspace transaction.
// The organization read is mandatory and its refusal is the whole read's
// refusal; every other section is attempted, and a section refused for
// lack of a grant is omitted and named rather than returned empty.
func (s *Service) Assemble(ctx context.Context, orgID ids.OrganizationID) (crmcontracts.Organization360, error) {
	now := s.now().UTC()
	out := crmcontracts.Organization360{AsOf: now, SectionsOmitted: []crmcontracts.Organization360SectionsOmitted{}}
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		org, err := s.people.GetOrganizationTx(ctx, tx, orgID, storekit.LiveOnly)
		if err != nil {
			return err
		}
		out.Organization = org
		return s.sections(ctx, tx, orgID, now, &out)
	})
	if err != nil {
		return crmcontracts.Organization360{}, err
	}
	return out, nil
}

// sections runs each optional section behind its own grant, in a fixed
// order so two reads of the same account produce the same
// sections_omitted list. A section that refuses with
// apperrors.ErrPermissionDenied is omitted and named; any other error
// fails the whole read, because a section that broke for a real reason
// must never be reported as one the caller may not see.
func (s *Service) sections(ctx context.Context, tx pgx.Tx, orgID ids.OrganizationID, now time.Time, out *crmcontracts.Organization360) error {
	each := []struct {
		name crmcontracts.Organization360SectionsOmitted
		read func(context.Context, pgx.Tx, ids.OrganizationID, time.Time, *crmcontracts.Organization360) error
	}{
		{sectionPeople, s.readContacts},
		{sectionStrength, s.readStrength},
		{sectionDeals, s.readDeals},
		{sectionActivities, s.readTimeline},
		{sectionNextSteps, s.readNextSteps},
		{sectionTags, s.readTags},
		{sectionListMemberships, s.readListMemberships},
		{sectionApprovals, s.readPendingApprovals},
		{sectionSinceLastVisit, s.readSinceLastVisit},
	}
	for _, section := range each {
		err := section.read(ctx, tx, orgID, now, out)
		if errors.Is(err, apperrors.ErrPermissionDenied) {
			out.SectionsOmitted = append(out.SectionsOmitted, section.name)
			continue
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) readContacts(ctx context.Context, tx pgx.Tx, orgID ids.OrganizationID, now time.Time, out *crmcontracts.Organization360) error {
	if err := auth.Require(ctx, "person", principal.ActionRead); err != nil {
		return err
	}
	data, page, err := contactsSection(ctx, tx, orgID, now)
	if err != nil {
		return err
	}
	out.People = &struct {
		Data []crmcontracts.Organization360Contact `json:"data"`
		Page crmcontracts.PageInfo                 `json:"page"`
	}{Data: data, Page: page}
	return nil
}

// readStrength rides the PERSON grant, not the organization one: the
// roll-up is computed over the account's contacts, and reading an account
// does not entitle the caller to a number derived from people they may
// not see.
func (s *Service) readStrength(ctx context.Context, tx pgx.Tx, orgID ids.OrganizationID, now time.Time, out *crmcontracts.Organization360) error {
	if err := auth.Require(ctx, "person", principal.ActionRead); err != nil {
		return err
	}
	account, err := people.AccountStrengthFor(ctx, tx, orgID, now)
	if err != nil {
		return err
	}
	out.Strength = accountStrengthToWire(account, now)
	return nil
}

func (s *Service) readDeals(ctx context.Context, tx pgx.Tx, orgID ids.OrganizationID, now time.Time, out *crmcontracts.Organization360) error {
	if err := auth.Require(ctx, "deal", principal.ActionRead); err != nil {
		return err
	}
	section, err := dealsSection(ctx, tx, orgID, now)
	if err != nil {
		return err
	}
	out.Deals = &section
	return nil
}

// readTimeline reads the first page of the account's timeline through the
// activities module's own list, so the section and GET /activities can
// never disagree about ordering or row scope.
func (s *Service) readTimeline(ctx context.Context, tx pgx.Tx, orgID ids.OrganizationID, _ time.Time, out *crmcontracts.Organization360) error {
	orgUUID := orgID.UUID
	entityType := "organization"
	limit := sectionLimit
	data, page, err := activities.ListActivitiesTx(ctx, tx, activities.ListActivitiesInput{
		EntityType: &entityType,
		EntityID:   &orgUUID,
		Limit:      &limit,
	})
	if err != nil {
		return err
	}
	out.Activities = &crmcontracts.ActivityListResponse{Data: data, Page: pageInfo(page)}
	return nil
}

func (s *Service) readNextSteps(ctx context.Context, tx pgx.Tx, orgID ids.OrganizationID, now time.Time, out *crmcontracts.Organization360) error {
	if err := auth.Require(ctx, "activity", principal.ActionRead); err != nil {
		return err
	}
	data, page, err := nextStepsSection(ctx, tx, orgID, now)
	if err != nil {
		return err
	}
	out.NextSteps = &struct {
		Data []crmcontracts.Organization360NextStep `json:"data"`
		Page crmcontracts.PageInfo                  `json:"page"`
	}{Data: data, Page: page}
	return nil
}

func (s *Service) readTags(ctx context.Context, tx pgx.Tx, orgID ids.OrganizationID, _ time.Time, out *crmcontracts.Organization360) error {
	if err := auth.Require(ctx, "tag", principal.ActionRead); err != nil {
		return err
	}
	tags, err := tagsSection(ctx, tx, orgID)
	if err != nil {
		return err
	}
	out.Tags = &tags
	return nil
}

func (s *Service) readListMemberships(ctx context.Context, tx pgx.Tx, orgID ids.OrganizationID, _ time.Time, out *crmcontracts.Organization360) error {
	if err := auth.Require(ctx, "list", principal.ActionRead); err != nil {
		return err
	}
	lists, err := listMembershipsSection(ctx, tx, orgID)
	if err != nil {
		return err
	}
	out.ListMemberships = &lists
	return nil
}

// readPendingApprovals asks the approvals service, never its SQL: the
// decidability rule (authority + target visibility) is that module's, and
// a record page that re-derived it would become the workspace-wide side
// channel the inbox refuses to be. Triage is human work, so a
// passport-driven read is refused there and the section is simply absent.
func (s *Service) readPendingApprovals(ctx context.Context, tx pgx.Tx, orgID ids.OrganizationID, _ time.Time, out *crmcontracts.Organization360) error {
	staged, err := s.approvals.PendingForTarget(ctx, tx, entityTypeOrganization, orgID.UUID, sectionLimit)
	if err != nil {
		return err
	}
	data, page := truncate(staged)
	out.PendingApprovals = &struct {
		Data []crmcontracts.Approval `json:"data"`
		Page crmcontracts.PageInfo   `json:"page"`
	}{Data: data, Page: page}
	return nil
}

func (s *Service) readSinceLastVisit(ctx context.Context, tx pgx.Tx, orgID ids.OrganizationID, _ time.Time, out *crmcontracts.Organization360) error {
	if err := auth.Require(ctx, "activity", principal.ActionRead); err != nil {
		return err
	}
	delta, err := s.sinceLastVisit(ctx, tx, orgID)
	if err != nil {
		return err
	}
	out.SinceLastVisit = &delta
	return nil
}

// pageInfo carries a store page onto the wire shape.
func pageInfo(p storekit.Page) crmcontracts.PageInfo {
	info := crmcontracts.PageInfo{HasMore: p.HasMore}
	if p.NextCursor != "" {
		info.NextCursor = &p.NextCursor
	}
	return info
}

// strengthToWire renders one contact's §4 result onto the shared contract
// shape. direction has no dedicated domain field — the §4 computation
// derives it internally on the way to reciprocity, and it is surfaced here
// from the same two counts rather than invented.
func strengthToWire(rs people.RelationshipStrength, now time.Time) crmcontracts.RelationshipStrength {
	inbound, outbound := rs.Inbound90d, rs.Outbound90d
	direction := 0.0
	if directed := inbound + outbound; directed > 0 {
		direction = 1 - math.Abs(float64(inbound-outbound))/float64(directed)
	}
	computedAt := now
	wire := crmcontracts.RelationshipStrength{
		Score:           rs.Strength,
		Bucket:          bucketToWire(rs.Bucket),
		LastInteraction: rs.LastInteraction,
		ComputedAt:      &computedAt,
		Inbound90d:      &inbound,
		Outbound90d:     &outbound,
	}
	wire.Factors.Recency = float32(rs.Recency)
	wire.Factors.Frequency = float32(rs.Frequency)
	wire.Factors.Reciprocity = float32(rs.Reciprocity)
	wire.Factors.Direction = float32(direction)
	return wire
}

// accountStrengthToWire adds the two account-only facts to the shared
// shape: whose relationship carries the score, and how many contacts it
// was chosen from.
func accountStrengthToWire(account people.AccountStrength, now time.Time) *crmcontracts.OrganizationStrength {
	base := strengthToWire(account.RelationshipStrength, now)
	out := crmcontracts.OrganizationStrength{
		Score:           base.Score,
		Bucket:          crmcontracts.OrganizationStrengthBucket(base.Bucket),
		Factors:         base.Factors,
		ComputedAt:      base.ComputedAt,
		LastInteraction: base.LastInteraction,
		Inbound90d:      base.Inbound90d,
		Outbound90d:     base.Outbound90d,
		ContactCount:    account.ContactCount,
	}
	if account.ContributorPersonID != nil {
		v := openapi_types.UUID(account.ContributorPersonID.UUID)
		out.ContributorPersonId = &v
	}
	return &out
}

// bucketToWire maps the domain's display bucket onto the contract
// vocabulary. The domain emits only the four cases below; anything else
// reads as dormant rather than as a wire value the enum never declared.
func bucketToWire(bucket string) crmcontracts.RelationshipStrengthBucket {
	switch bucket {
	case "weak":
		return crmcontracts.RelationshipStrengthBucketWeak
	case "moderate":
		return crmcontracts.RelationshipStrengthBucketWarm
	case "strong":
		return crmcontracts.RelationshipStrengthBucketStrong
	default: // "none"
		return crmcontracts.RelationshipStrengthBucketDormant
	}
}
