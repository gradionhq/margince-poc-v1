// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package project360

// The work-shaped sections: contracts, documents, the open commitments, the
// timeline, the filing coverage and the header roll-ups.

import (
	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/modules/activities"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
)

// entityTypeProject is the vocabulary word the activities module files a
// project under, spelled from the datasource kind so the two cannot drift.
var entityTypeProject = string(datasource.RecordProject)

func (a *assembly) readContracts() error {
	limit := sectionLimit
	section, err := a.svc.contracts.ListProjectContractsTx(a.ctx, a.tx, a.projectID, &limit)
	if err != nil {
		return err
	}
	a.out.Contracts = &section
	return nil
}

// readDocuments lists the files attached to the project itself. Its gate is
// the project grant — an attachment inherits its parent's authority — which
// the anchor read already held, so the section has no omission case.
func (a *assembly) readDocuments() error {
	limit := sectionLimit
	rows, page, err := a.svc.activities.ListAttachmentsTx(a.ctx, a.tx, entityTypeProject, a.projectID.UUID, nil, &limit)
	if err != nil {
		return err
	}
	a.out.Documents = &struct {
		Data []crmcontracts.Attachment `json:"data"`
		Page crmcontracts.PageInfo     `json:"page"`
	}{Data: rows, Page: pageInfo(page)}
	return nil
}

// readCommitments is the open-task sweep narrowed to the project, in the
// order a reviewer works it: soonest due first, undated last.
func (a *assembly) readCommitments() error {
	tasks, truncated, err := a.svc.activities.ListOpenTasksTx(a.ctx, a.tx, activities.ListOpenTasksInput{
		EntityType: &entityTypeProject, EntityID: &a.projectID.UUID, Limit: sectionLimit,
	})
	if err != nil {
		return err
	}
	data := make([]crmcontracts.Project360Commitment, 0, len(tasks))
	for _, t := range tasks {
		item := crmcontracts.Project360Commitment{
			ActivityId: openapi_types.UUID(t.ID),
			Subject:    t.Subject,
			DueAt:      t.DueAt,
			AssigneeId: uuidPtr(t.AssigneeID),
			Overdue:    t.DueAt != nil && t.DueAt.Before(a.now),
		}
		if t.AssigneeID != nil {
			name := t.AssigneeName
			item.AssigneeName = &name
		}
		data = append(data, item)
	}
	a.out.Commitments = &struct {
		Data []crmcontracts.Project360Commitment `json:"data"`
		Page crmcontracts.PageInfo               `json:"page"`
	}{Data: data, Page: crmcontracts.PageInfo{HasMore: truncated}}
	return nil
}

// readTimeline reads the first page of the project's timeline through the
// activities module's own list, so the section and GET /activities can never
// disagree about ordering or row scope.
func (a *assembly) readTimeline() error {
	limit := sectionLimit
	rows, page, err := activities.ListActivitiesTx(a.ctx, a.tx, activities.ListActivitiesInput{
		EntityType: &entityTypeProject, EntityID: &a.projectID.UUID, Limit: &limit,
	})
	if err != nil {
		return err
	}
	a.out.Activities = &crmcontracts.ActivityListResponse{Data: rows, Page: pageInfo(page)}
	return nil
}

func (a *assembly) readCoverage() error {
	facts, err := a.activityFacts()
	if err != nil {
		return err
	}
	a.out.Coverage = &crmcontracts.Project360Coverage{
		Attributed:         facts.Attributed,
		UnattributedNearby: facts.UnattributedNearby,
		AwaitingDecision:   facts.AwaitingDecision,
	}
	return nil
}

// readRollups needs both the deal and the activity grant: the header shows
// money beside work, and a header with one half withheld would read as a
// project with no deals or with no work.
func (a *assembly) readRollups() error {
	totals, err := a.svc.deals.ProjectDealTotalsTx(a.ctx, a.tx, a.projectID)
	if err != nil {
		return err
	}
	facts, err := a.activityFacts()
	if err != nil {
		return err
	}
	open, won := totals.OpenMinor, totals.WonMinor
	openCurrency, wonCurrency := totals.Currency, totals.Currency
	a.out.Rollups = &crmcontracts.Project360Rollups{
		OpenDealValue:   crmcontracts.Money{AmountMinor: &open, Currency: &openCurrency},
		WonDealValue:    crmcontracts.Money{AmountMinor: &won, Currency: &wonCurrency},
		OpenCommitments: facts.OpenCommitments,
		LastActivityAt:  facts.LastActivityAt,
		ActivityCount:   facts.Attributed,
	}
	return nil
}

func uuidPtr(id *ids.UUID) *openapi_types.UUID {
	if id == nil {
		return nil
	}
	v := openapi_types.UUID(*id)
	return &v
}
