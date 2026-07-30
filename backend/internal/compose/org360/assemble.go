// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package org360

import (
	"context"
	"errors"
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
	sectionSuggestions     = crmcontracts.Organization360SectionsOmitted("suggestions")
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
	a := &assembly{svc: s, ctx: ctx, tx: tx, orgID: orgID, now: now, out: out}
	each := []struct {
		name crmcontracts.Organization360SectionsOmitted
		read func() error
	}{
		{sectionPeople, a.readContacts},
		{sectionStrength, a.readStrength},
		{sectionDeals, a.readDeals},
		{sectionActivities, a.readTimeline},
		{sectionNextSteps, a.readNextSteps},
		{sectionTags, a.readTags},
		{sectionListMemberships, a.readListMemberships},
		{sectionApprovals, a.readPendingApprovals},
		{sectionSinceLastVisit, a.readSinceLastVisit},
		// Last because it is derived, not because it reads the sections above —
		// the rules issue their own queries (suggestionreads.go), under the same
		// grants and the same row scope, so they can see further back than a
		// truncated section page without ever seeing wider.
		{sectionSuggestions, a.readSuggestions},
	}
	for _, section := range each {
		err := section.read()
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

// assembly is one 360's working state. Several sections are built from the
// same underlying read — the contact list and the account roll-up both need
// every contact's score, and the approvals section and the since-last-visit
// count both need the decidable stagings — so those reads are taken once
// here and shared, rather than each section paying for its own copy of the
// same rows at the same instant.
type assembly struct {
	svc   *Service
	ctx   context.Context
	tx    pgx.Tx
	orgID ids.OrganizationID
	now   time.Time
	out   *crmcontracts.Organization360

	contacts      []people.ContactStrength
	contactsRead  bool
	staged        []crmcontracts.Approval
	stagedRead    bool
	stagedRefused bool
}

// contactStrengths reads every visible contact's §4 score once per request.
func (a *assembly) contactStrengths() ([]people.ContactStrength, error) {
	if a.contactsRead {
		return a.contacts, nil
	}
	contacts, err := people.StrengthForOrgContacts(a.ctx, a.tx, a.orgID, a.now)
	if err != nil {
		return nil, err
	}
	a.contacts, a.contactsRead = contacts, true
	return contacts, nil
}

// pendingApprovals reads the decidable stagings once per request.
// stagedRefused records a permission refusal so the count half can answer
// "not counted" without asking again and getting the same refusal.
func (a *assembly) pendingApprovals() ([]crmcontracts.Approval, bool, error) {
	if a.stagedRead {
		return a.staged, !a.stagedRefused, nil
	}
	staged, err := a.svc.approvals.PendingForTarget(a.ctx, a.tx, entityTypeOrganization, a.orgID.UUID, approvals.PendingScanCap)
	a.stagedRead = true
	if errors.Is(err, apperrors.ErrPermissionDenied) {
		a.stagedRefused = true
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	a.staged = staged
	return staged, true, nil
}

func (a *assembly) readContacts() error {
	if err := auth.Require(a.ctx, "person", principal.ActionRead); err != nil {
		return err
	}
	strengths, err := a.contactStrengths()
	if err != nil {
		return err
	}
	data, page, err := contactsSection(a.ctx, a.tx, a.orgID, a.now, strengths)
	if err != nil {
		return err
	}
	a.out.People = &struct {
		Data []crmcontracts.Organization360Contact `json:"data"`
		Page crmcontracts.PageInfo                 `json:"page"`
	}{Data: data, Page: page}
	return nil
}

// readStrength rides the PERSON grant, not the organization one: the
// roll-up is computed over the account's contacts, and reading an account
// does not entitle the caller to a number derived from people they may
// not see.
func (a *assembly) readStrength() error {
	if err := auth.Require(a.ctx, "person", principal.ActionRead); err != nil {
		return err
	}
	strengths, err := a.contactStrengths()
	if err != nil {
		return err
	}
	a.out.Strength = accountStrengthToWire(people.FoldAccountStrength(strengths), a.now)
	return nil
}

func (a *assembly) readDeals() error {
	if err := auth.Require(a.ctx, "deal", principal.ActionRead); err != nil {
		return err
	}
	section, err := dealsSection(a.ctx, a.tx, a.orgID, a.now)
	if err != nil {
		return err
	}
	a.out.Deals = &section
	return nil
}

// readTimeline reads the first page of the account's timeline through the
// activities module's own list, so the section and GET /activities can
// never disagree about ordering or row scope. Its gate is that list's own
// auth.Require, which refuses with the same error every other section uses
// to declare itself omitted.
func (a *assembly) readTimeline() error {
	orgUUID := a.orgID.UUID
	entityType := "organization"
	limit := sectionLimit
	data, page, err := activities.ListActivitiesTx(a.ctx, a.tx, activities.ListActivitiesInput{
		EntityType: &entityType,
		EntityID:   &orgUUID,
		Limit:      &limit,
	})
	if err != nil {
		return err
	}
	a.out.Activities = &crmcontracts.ActivityListResponse{Data: data, Page: pageInfo(page)}
	return nil
}

func (a *assembly) readNextSteps() error {
	if err := auth.Require(a.ctx, "activity", principal.ActionRead); err != nil {
		return err
	}
	data, page, err := nextStepsSection(a.ctx, a.tx, a.orgID, a.now)
	if err != nil {
		return err
	}
	a.out.NextSteps = &struct {
		Data []crmcontracts.Organization360NextStep `json:"data"`
		Page crmcontracts.PageInfo                  `json:"page"`
	}{Data: data, Page: page}
	return nil
}

func (a *assembly) readTags() error {
	if err := auth.Require(a.ctx, "tag", principal.ActionRead); err != nil {
		return err
	}
	tags, err := tagsSection(a.ctx, a.tx, a.orgID)
	if err != nil {
		return err
	}
	a.out.Tags = &tags
	return nil
}

func (a *assembly) readListMemberships() error {
	if err := auth.Require(a.ctx, "list", principal.ActionRead); err != nil {
		return err
	}
	lists, err := listMembershipsSection(a.ctx, a.tx, a.orgID)
	if err != nil {
		return err
	}
	a.out.ListMemberships = &lists
	return nil
}

// readPendingApprovals asks the approvals service, never its SQL: the
// decidability rule (authority + target visibility) is that module's, and
// a record page that re-derived it would become the workspace-wide side
// channel the inbox refuses to be. Triage is human work, so a
// passport-driven read is refused there and the section is simply absent.
func (a *assembly) readPendingApprovals() error {
	staged, triageable, err := a.pendingApprovals()
	if err != nil {
		return err
	}
	if !triageable {
		return apperrors.ErrPermissionDenied
	}
	data, page := truncate(staged)
	a.out.PendingApprovals = &struct {
		Data []crmcontracts.Approval `json:"data"`
		Page crmcontracts.PageInfo   `json:"page"`
	}{Data: data, Page: page}
	return nil
}

func (a *assembly) readSinceLastVisit() error {
	if err := auth.Require(a.ctx, "activity", principal.ActionRead); err != nil {
		return err
	}
	delta, err := a.svc.sinceLastVisit(a.ctx, a.tx, a.orgID, a)
	if err != nil {
		return err
	}
	a.out.SinceLastVisit = &delta
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

// accountStrengthToWire adds the two account-only facts to the shared
// shape: whose relationship carries the score, and how many contacts it
// was chosen from. The shared half comes from compose.StrengthToWire, so
// a bucket rename is made once for both the account roll-up and the
// per-person routes.
func accountStrengthToWire(account people.AccountStrength, now time.Time) *crmcontracts.OrganizationStrength {
	base := people.StrengthToWire(account.RelationshipStrength, now)
	out := crmcontracts.OrganizationStrength{
		Score:                   base.Score,
		Bucket:                  crmcontracts.OrganizationStrengthBucket(base.Bucket),
		Factors:                 base.Factors,
		ComputedAt:              base.ComputedAt,
		LastInteraction:         base.LastInteraction,
		Inbound90d:              base.Inbound90d,
		Outbound90d:             base.Outbound90d,
		ContributingActivityIds: base.ContributingActivityIds,
		ContactCount:            account.ContactCount,
	}
	if account.ContributorPersonID != nil {
		v := openapi_types.UUID(account.ContributorPersonID.UUID)
		out.ContributorPersonId = &v
	}
	return &out
}
