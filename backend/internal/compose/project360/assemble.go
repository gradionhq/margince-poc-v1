// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package project360

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/modules/activities"
	"github.com/gradionhq/margince/backend/internal/modules/contracts"
	"github.com/gradionhq/margince/backend/internal/modules/deals"
	"github.com/gradionhq/margince/backend/internal/modules/people"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// sectionLimit is how many rows of a nested collection one 360 carries.
// The section is a summary with a "there is more" flag, not a paging
// surface: follow-up pages come from the endpoint that owns the collection.
const sectionLimit = 25

// Service assembles the project page from the module stores it composes.
type Service struct {
	pool       *pgxpool.Pool
	deals      *deals.Store
	people     *people.Store
	contracts  *contracts.Store
	activities *activities.Store
	now        func() time.Time
}

// NewService binds the composite read to its module stores. now is the
// injected clock: the phase durations and the overdue flag are duration
// comparisons against "now", and a test pins the instant so neither can
// flake between seeding and reading.
func NewService(
	pool *pgxpool.Pool,
	dealStore *deals.Store,
	peopleStore *people.Store,
	contractStore *contracts.Store,
	activityStore *activities.Store,
	now func() time.Time,
) *Service {
	return &Service{
		pool: pool, deals: dealStore, people: peopleStore,
		contracts: contractStore, activities: activityStore, now: now,
	}
}

// catalogs is what the page reads ABOVE its transaction: each custom-field
// catalog opens a connection of its own, and the page holds the only
// connection its sections have for as long as it runs.
//
// The organization catalog is NOT here: reading it takes organization:read,
// and a refusal above the transaction would fail the whole page for a caller
// who may read the project but not its company. The organization section
// reads it itself, so that refusal lands as an omission.
type catalogs struct {
	project deals.CustomColumns
	deal    deals.CustomColumns
}

func (s *Service) readCatalogs(ctx context.Context) (catalogs, error) {
	var c catalogs
	var err error
	if c.project, err = s.deals.ActiveProjectColumns(ctx); err != nil {
		return catalogs{}, err
	}
	if c.deal, err = s.deals.ActiveDealColumns(ctx); err != nil {
		return catalogs{}, err
	}
	return c, nil
}

// Assemble reads the whole project page inside ONE workspace transaction.
// The project read is mandatory and its refusal is the whole read's
// refusal; every other section is attempted, and a section refused for
// lack of a grant is omitted and named rather than returned empty.
func (s *Service) Assemble(ctx context.Context, projectID ids.ProjectID) (crmcontracts.Project360, error) {
	now := s.now().UTC()
	out := crmcontracts.Project360{AsOf: now, SectionsOmitted: []crmcontracts.Project360Section{}}
	cats, err := s.readCatalogs(ctx)
	if err != nil {
		return crmcontracts.Project360{}, err
	}
	err = database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		project, err := s.deals.GetProjectTx(ctx, tx, projectID, storekit.LiveOnly, cats.project)
		if err != nil {
			return err
		}
		out.Project = project
		a := &assembly{svc: s, ctx: ctx, tx: tx, projectID: projectID, now: now, cats: cats, out: &out}
		return a.sections()
	})
	if err != nil {
		return crmcontracts.Project360{}, err
	}
	return out, nil
}

// assembly is one 360's working state. The activity facts feed two sections
// (coverage and rollups), so they are read once here and shared rather than
// counted twice at two instants.
type assembly struct {
	svc       *Service
	ctx       context.Context
	tx        pgx.Tx
	projectID ids.ProjectID
	now       time.Time
	cats      catalogs
	out       *crmcontracts.Project360

	facts     activities.ProjectActivityFacts
	factsRead bool
}

// sections runs each optional section behind its own grant, in a fixed
// order so two reads of the same project produce the same sections_omitted
// list. A section that refuses with apperrors.ErrPermissionDenied is
// omitted and named; any other error fails the whole read, because a
// section that broke for a real reason must never be reported as one the
// caller may not see.
func (a *assembly) sections() error {
	each := []struct {
		name crmcontracts.Project360Section
		read func() error
	}{
		{crmcontracts.Project360SectionOrganization, a.readOrganization},
		{crmcontracts.Project360SectionPhaseHistory, a.readPhaseHistory},
		{crmcontracts.Project360SectionDeals, a.readDeals},
		{crmcontracts.Project360SectionStakeholders, a.readStakeholders},
		{crmcontracts.Project360SectionContracts, a.readContracts},
		{crmcontracts.Project360SectionCommitments, a.readCommitments},
		{crmcontracts.Project360SectionActivities, a.readTimeline},
		{crmcontracts.Project360SectionCoverage, a.readCoverage},
		{crmcontracts.Project360SectionRollups, a.readRollups},
	}
	for _, section := range each {
		err := section.read()
		if errors.Is(err, apperrors.ErrPermissionDenied) {
			a.out.SectionsOmitted = append(a.out.SectionsOmitted, section.name)
			continue
		}
		if err != nil {
			return fmt.Errorf("project 360 section %q: %w", section.name, err)
		}
	}
	// Documents ride the project grant, which the anchor read already held,
	// so the section is present whenever the page is — and it runs after
	// the loop because it has no omission case to name.
	return a.readDocuments()
}

// activityFacts reads the timeline's counts once per request.
func (a *assembly) activityFacts() (activities.ProjectActivityFacts, error) {
	if a.factsRead {
		return a.facts, nil
	}
	facts, err := a.svc.activities.ProjectActivityFactsTx(a.ctx, a.tx, a.projectID)
	if err != nil {
		return activities.ProjectActivityFacts{}, err
	}
	a.facts, a.factsRead = facts, true
	return facts, nil
}

// pageInfo carries a store page onto the wire shape. The cursor is
// deliberately dropped: a section is a summary, and page two comes from the
// endpoint that owns the collection.
func pageInfo(p storekit.Page) crmcontracts.PageInfo {
	return crmcontracts.PageInfo{HasMore: p.HasMore}
}
