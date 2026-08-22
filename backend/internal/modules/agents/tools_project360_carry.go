// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package agents

// The per-section carries behind project360Result. Each is a rename from
// the contract row to the surface's row: no filtering, no judging beyond
// what the page already decided.

import (
	"time"

	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

func project360Project(p crmcontracts.Project) Project360Project {
	out := Project360Project{
		ProjectID:      ids.UUID(p.Id),
		Name:           p.Name,
		Key:            orBlank(p.Key),
		ClosedReason:   orBlank(p.ClosedReason),
		Description:    orBlank(p.Description),
		OrganizationID: (*ids.UUID)(p.OrganizationId),
		OwnerID:        (*ids.UUID)(p.OwnerId),
		StartedAt:      dateTime(p.StartedAt),
		TargetEndDate:  dateTime(p.TargetEndDate),
		EndedAt:        dateTime(p.EndedAt),
	}
	if p.Phase != nil {
		out.Phase = string(*p.Phase)
	}
	return out
}

func project360History(h crmcontracts.Project360PhaseHistory) *Project360PhaseHistory {
	out := &Project360PhaseHistory{
		Transitions: make([]Project360Transition, 0, len(h.Data)),
		Durations:   make([]Project360PhaseDuration, 0, len(h.PhaseDurations)),
	}
	for _, t := range h.Data {
		out.Transitions = append(out.Transitions, Project360Transition{
			FromPhase:     orBlank(t.FromPhase),
			ToPhase:       t.ToPhase,
			Reason:        orBlank(t.Reason),
			ChangedAt:     t.ChangedAt,
			ChangedBy:     t.ChangedBy.Id,
			ChangedByName: orBlank(t.ChangedBy.DisplayName),
		})
	}
	for _, d := range h.PhaseDurations {
		out.Durations = append(out.Durations, Project360PhaseDuration{
			Phase: d.Phase, Seconds: d.Seconds, Current: d.Current,
		})
	}
	return out
}

func project360Deals(rows []crmcontracts.Deal) []HandoffDeal {
	out := make([]HandoffDeal, 0, len(rows))
	for _, d := range rows {
		out = append(out, HandoffDeal{
			DealID: ids.UUID(d.Id), Name: d.Name, Status: string(d.Status),
			AmountMinor: d.AmountMinor, Currency: d.Currency,
		})
	}
	return out
}

func project360Stakeholders(rows []crmcontracts.Project360Stakeholder) []HandoffStakeholder {
	out := make([]HandoffStakeholder, 0, len(rows))
	for _, s := range rows {
		out = append(out, HandoffStakeholder{
			PersonID: ids.UUID(s.PersonId), Name: orBlank(s.PersonName), Role: orBlank(s.Role),
		})
	}
	return out
}

func project360Contracts(rows []crmcontracts.Contract) []Project360Contract {
	out := make([]Project360Contract, 0, len(rows))
	for _, c := range rows {
		item := Project360Contract{
			ContractID:     ids.UUID(c.Id),
			Title:          c.Title,
			ContractNumber: orBlank(c.ContractNumber),
			ValueMinor:     c.ValueMinor,
			Currency:       orBlank(c.Currency),
			StartsOn:       dateTime(c.StartsOn),
			EndsOn:         dateTime(c.EndsOn),
		}
		if c.Status != nil {
			item.Status = string(*c.Status)
		}
		if c.UnderContract != nil {
			item.UnderContract = *c.UnderContract
		}
		out = append(out, item)
	}
	return out
}

func project360Documents(rows []crmcontracts.Attachment) []Project360Document {
	out := make([]Project360Document, 0, len(rows))
	for _, a := range rows {
		item := Project360Document{
			AttachmentID: ids.UUID(a.Id), Filename: a.Filename, Title: orBlank(a.Title), CreatedAt: a.CreatedAt,
		}
		if a.Category != nil {
			item.Category = string(*a.Category)
		}
		if a.DocState != nil {
			item.DocState = string(*a.DocState)
		}
		out = append(out, item)
	}
	return out
}

// project360Commitments judges each open task against the page's own as_of,
// through the same carry review_commitments uses, so a promise reads the same
// state on both surfaces.
func project360Commitments(rows []crmcontracts.Project360Commitment, asOf time.Time) []CommitmentItem {
	out := make([]CommitmentItem, 0, len(rows))
	for _, c := range rows {
		promise := OpenCommitment{
			TaskID: ids.UUID(c.ActivityId), Subject: c.Subject, DueAt: c.DueAt,
			AssigneeID: (*ids.UUID)(c.AssigneeId), AssigneeName: orBlank(c.AssigneeName),
		}
		out = append(out, promise.wire(asOf))
	}
	return out
}

func project360Activities(rows []crmcontracts.Activity) []Project360Activity {
	out := make([]Project360Activity, 0, len(rows))
	for _, a := range rows {
		item := Project360Activity{
			ActivityID: ids.UUID(a.Id), Kind: string(a.Kind), Subject: orBlank(a.Subject), OccurredAt: a.OccurredAt,
		}
		if a.Direction != nil {
			item.Direction = string(*a.Direction)
		}
		out = append(out, item)
	}
	return out
}

func project360Rollups(r crmcontracts.Project360Rollups) *Project360Rollups {
	out := &Project360Rollups{
		Currency:        orBlank(r.OpenDealValue.Currency),
		OpenCommitments: r.OpenCommitments,
		LastActivityAt:  r.LastActivityAt,
		ActivityCount:   r.ActivityCount,
	}
	if r.OpenDealValue.AmountMinor != nil {
		out.OpenDealValueMinor = *r.OpenDealValue.AmountMinor
	}
	if r.WonDealValue.AmountMinor != nil {
		out.WonDealValueMinor = *r.WonDealValue.AmountMinor
	}
	return out
}

func orBlank(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func dateTime(d *openapi_types.Date) *time.Time {
	if d == nil {
		return nil
	}
	return &d.Time
}
