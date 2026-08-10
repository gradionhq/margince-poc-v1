// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The seams behind review_commitments and prepare_handoff.
//
// Both are assembled HERE because both cross modules: an open promise is an
// activity row, a handover is a project plus the deals rolled up to it plus
// the people attached to it plus those same promises — and a module never
// imports a sibling (ADR-0054 §9). Every read below is a module's own gated
// store path, so object RBAC and row scope apply exactly as they do on the
// HTTP surface; there is no raw SQL here and no second spelling of a filter.
//
// THE CLOCK LIVES ON THIS SIDE OF THE SEAM. The tools judge a promise as
// overdue or upcoming, and that judgement is a pure function of the due date
// and an instant. Reading the instant here — ONCE per call, and passing it
// across — is what makes the tool's derivation reproducible in a test with no
// clock in it, and what stops two rows in one answer being judged against two
// different nows.

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/modules/activities"
	"github.com/gradionhq/margince/backend/internal/modules/agents"
	"github.com/gradionhq/margince/backend/internal/modules/deals"
	"github.com/gradionhq/margince/backend/internal/modules/identity"
	"github.com/gradionhq/margince/backend/internal/modules/people"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
)

// handoffScanLimit bounds each of the handover's list reads. A handover is a
// briefing rather than an export: a project with more than this many deals,
// stakeholders or open promises has a reading problem the tool cannot fix by
// returning more rows.
const handoffScanLimit = 50

// commitmentLister serves the open-promise set from the activities module's
// own gated read.
func commitmentLister(pool *pgxpool.Pool) agents.CommitmentLister {
	store := activities.NewStore(pool)
	return func(ctx context.Context, in agents.CommitmentQuery) (agents.CommitmentSweep, error) {
		tasks, truncated, err := store.ListOpenTasks(ctx, activities.ListOpenTasksInput{
			AssigneeID: in.AssigneeID,
			Limit:      in.Limit,
		})
		if err != nil {
			return agents.CommitmentSweep{}, err
		}
		return agents.CommitmentSweep{
			AsOf:        clockNow(),
			Commitments: asCommitments(tasks),
			Truncated:   truncated,
		}, nil
	}
}

// asCommitments carries the store rows across the seam. It is a rename and
// nothing else: no filtering, no re-ordering, no judging — the store decided
// which rows, and the tool decides what each row's state is.
func asCommitments(tasks []activities.OpenTask) []agents.OpenCommitment {
	out := make([]agents.OpenCommitment, 0, len(tasks))
	for _, t := range tasks {
		about := make([]agents.CommitmentAbout, 0, len(t.About))
		for _, a := range t.About {
			about = append(about, agents.CommitmentAbout{
				EntityType: a.EntityType, EntityID: a.EntityID, Name: a.Name,
			})
		}
		out = append(out, agents.OpenCommitment{
			TaskID: t.ID, Subject: t.Subject, DueAt: t.DueAt,
			AssigneeID: t.AssigneeID, AssigneeName: t.AssigneeName, About: about,
		})
	}
	return out
}

// handoffReader assembles one project's handover material.
//
// FOUR GATED READS, NOT ONE JOIN. Each of them enforces its own object grant
// and its own row scope: a caller who may read the project but not its deals
// is answered a handover with no deals in it rather than one assembled around
// a gate nobody asked. The project read runs FIRST and its refusal is
// returned unchanged, so a project outside the caller's scope answers
// not-found exactly as reading it directly would.
func handoffReader(pool *pgxpool.Pool) agents.HandoffReader {
	dealStore := deals.NewStore(pool, identity.BaseCurrencyOf)
	peopleStore := people.NewStore(pool)
	taskStore := activities.NewStore(pool)
	return func(ctx context.Context, projectID ids.UUID) (agents.HandoffFacts, error) {
		project, err := dealStore.GetProject(ctx,
			ids.From[ids.ProjectKind](projectID), storekit.LiveOnly)
		if err != nil {
			return agents.HandoffFacts{}, err
		}
		facts := agents.HandoffFacts{AsOf: clockNow(), Project: handoffProject(project)}
		if facts.Deals, err = handoffDeals(ctx, dealStore, projectID); err != nil {
			return agents.HandoffFacts{}, err
		}
		if facts.Stakeholders, err = handoffStakeholders(ctx, peopleStore, projectID); err != nil {
			return agents.HandoffFacts{}, err
		}
		projectType := string(datasource.RecordProject)
		tasks, truncated, err := taskStore.ListOpenTasks(ctx, activities.ListOpenTasksInput{
			EntityType: &projectType, EntityID: &projectID, Limit: handoffScanLimit,
		})
		if err != nil {
			return agents.HandoffFacts{}, err
		}
		facts.OpenCommitments, facts.CommitmentsTruncated = asCommitments(tasks), truncated
		return facts, nil
	}
}

// handoffProject carries the project row across the seam.
func handoffProject(p crmcontracts.Project) agents.HandoffProject {
	out := agents.HandoffProject{
		ProjectID:      ids.UUID(p.Id),
		Name:           p.Name,
		OrganizationID: ids.UUID(p.OrganizationId),
		OwnerID:        (*ids.UUID)(p.OwnerId),
	}
	if p.Key != nil {
		out.Key = *p.Key
	}
	if p.Phase != nil {
		out.Phase = string(*p.Phase)
	}
	if p.Description != nil {
		out.Description = *p.Description
	}
	if p.StartedAt != nil {
		out.StartedAt = &p.StartedAt.Time
	}
	if p.TargetEndDate != nil {
		out.TargetEndDate = &p.TargetEndDate.Time
	}
	return out
}

// handoffDeals reads what is rolled up to the project. Every deal is carried,
// won or not: which of them counts as sold is the tool's judgement, and a
// filter here would hide the open ones from a reader who has to know a
// pursuit is still running.
func handoffDeals(ctx context.Context, store *deals.Store, projectID ids.UUID) ([]agents.HandoffDeal, error) {
	limit := handoffScanLimit
	project := ids.From[ids.ProjectKind](projectID)
	rows, _, err := store.ListDeals(ctx, deals.ListDealsInput{ProjectID: &project, Limit: &limit})
	if err != nil {
		return nil, err
	}
	out := make([]agents.HandoffDeal, 0, len(rows))
	for _, d := range rows {
		out = append(out, agents.HandoffDeal{
			DealID: ids.UUID(d.Id), Name: d.Name, Status: string(d.Status),
			AmountMinor: d.AmountMinor, Currency: d.Currency,
		})
	}
	return out, nil
}

// handoffStakeholders reads the people attached to the project, through the
// generic relationship list so the edge's own visibility rules apply.
//
// A seat is an id and a role, with no name — the same shape account_coverage
// answers a deal's stakeholder seats in. Naming them would need a gated
// person read per seat, and the caller already has read_record for the one
// they want to reach; the field a handover is judged on is the role, and that
// is here.
func handoffStakeholders(ctx context.Context, store *people.Store, projectID ids.UUID) ([]agents.HandoffStakeholder, error) {
	limit := handoffScanLimit
	project := ids.From[ids.ProjectKind](projectID)
	kind := people.ProjectStakeholderKind
	edges, _, err := store.ListRelationships(ctx, people.ListRelationshipsInput{
		Kind: &kind, ProjectID: &project, Limit: &limit,
	})
	if err != nil {
		return nil, err
	}
	out := make([]agents.HandoffStakeholder, 0, len(edges))
	for _, e := range edges {
		if e.PersonID == nil {
			continue
		}
		stakeholder := agents.HandoffStakeholder{PersonID: e.PersonID.UUID}
		if e.Role != nil {
			stakeholder.Role = *e.Role
		}
		out = append(out, stakeholder)
	}
	return out, nil
}
