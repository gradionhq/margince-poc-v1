// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package project360

// The record-shaped sections: the company, the phase history, the deals and
// the stakeholder seats. Each is one module store's transaction-taking read
// plus the carry onto the wire shape; the grant is the store's own.

import (
	"errors"

	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/modules/deals"
	"github.com/gradionhq/margince/backend/internal/modules/people"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// readOrganization names the company the project is for. A project whose
// organization_id is masked — the caller may not read that company — is
// reported as an omission, not an empty header: the mask IS the refusal,
// and the organization read would answer the same way if it were asked.
func (a *assembly) readOrganization() error {
	if a.out.Project.OrganizationId == nil {
		return apperrors.ErrPermissionDenied
	}
	orgID := ids.From[ids.OrganizationKind](ids.UUID(*a.out.Project.OrganizationId))
	org, err := a.svc.people.GetOrganizationTx(a.ctx, a.tx, orgID, storekit.LiveOnly, a.cats.org)
	if err != nil {
		return err
	}
	a.out.Organization = &crmcontracts.Project360Organization{
		Id:   org.Id,
		Name: org.DisplayName,
	}
	return nil
}

// readPhaseHistory carries every transition and the fold over them. Its
// gate is the project grant, which the anchor already held, so the section
// is effectively always present; it is still listed in sections_omitted's
// vocabulary because the store gates it in its own right.
func (a *assembly) readPhaseHistory() error {
	history, err := a.svc.deals.ListProjectPhaseHistoryTx(a.ctx, a.tx, a.projectID)
	if err != nil {
		return err
	}
	data := make([]crmcontracts.Project360PhaseTransition, 0, len(history))
	for _, t := range history {
		row := crmcontracts.Project360PhaseTransition{
			Id:        openapi_types.UUID(t.ID),
			FromPhase: t.FromPhase,
			ToPhase:   t.ToPhase,
			Reason:    t.Reason,
			ChangedAt: t.OccurredAt,
		}
		row.ChangedBy.Id = t.ChangedBy
		row.ChangedBy.DisplayName = t.ChangedByName
		data = append(data, row)
	}
	durations := deals.FoldPhaseDurations(history, a.now)
	folded := make([]crmcontracts.Project360PhaseDuration, 0, len(durations))
	for _, d := range durations {
		folded = append(folded, crmcontracts.Project360PhaseDuration{
			Phase: d.Phase, Seconds: d.Seconds, Current: d.Current,
		})
	}
	a.out.PhaseHistory = &crmcontracts.Project360PhaseHistory{Data: data, PhaseDurations: folded}
	return nil
}

// readDeals is the deal list narrowed to the project, every status: which
// of them counts as sold is the reader's judgement, and a filter here would
// hide the open pursuit from someone who has to know it is still running.
func (a *assembly) readDeals() error {
	limit := sectionLimit
	rows, page, err := a.svc.deals.ListDealsTx(a.ctx, a.tx, deals.ListDealsInput{
		ProjectID: &a.projectID, Limit: &limit,
	}, a.cats.deal)
	if err != nil {
		return err
	}
	a.out.Deals = &struct {
		Data []crmcontracts.Deal   `json:"data"`
		Page crmcontracts.PageInfo `json:"page"`
	}{Data: rows, Page: pageInfo(page)}
	return nil
}

// readStakeholders reads the seats through the relationship list, so the
// edge's own visibility rule applies, and names each person under the
// person grant. A caller holding the edge grant but not the person grant
// still sees the seats — the role is the field a handover is judged on —
// with the names withheld, which the contract spells as a null name.
func (a *assembly) readStakeholders() error {
	limit := sectionLimit
	kind := people.ProjectStakeholderKind
	edges, page, err := a.svc.people.ListRelationshipsTx(a.ctx, a.tx, people.ListRelationshipsInput{
		Kind: &kind, ProjectID: &a.projectID, Limit: &limit,
	})
	if err != nil {
		return err
	}
	seated := make([]ids.PersonID, 0, len(edges))
	for _, e := range edges {
		if e.PersonID != nil {
			seated = append(seated, *e.PersonID)
		}
	}
	names, err := a.svc.people.PersonNamesTx(a.ctx, a.tx, seated)
	if errors.Is(err, apperrors.ErrPermissionDenied) {
		names = map[ids.UUID]string{}
	} else if err != nil {
		return err
	}
	data := make([]crmcontracts.Project360Stakeholder, 0, len(edges))
	for _, e := range edges {
		if e.PersonID == nil {
			continue
		}
		seat := crmcontracts.Project360Stakeholder{
			RelationshipId: openapi_types.UUID(e.ID),
			PersonId:       openapi_types.UUID(e.PersonID.UUID),
			Role:           e.Role,
		}
		if name, known := names[e.PersonID.UUID]; known {
			seat.PersonName = &name
		}
		data = append(data, seat)
	}
	a.out.Stakeholders = &struct {
		Data []crmcontracts.Project360Stakeholder `json:"data"`
		Page crmcontracts.PageInfo                `json:"page"`
	}{Data: data, Page: pageInfo(page)}
	return nil
}
