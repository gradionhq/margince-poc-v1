// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package deals

import (
	"fmt"
	"net/http"
	"time"

	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// GetDealHealth serves the health formula with the fact behind each factor.
// This is the reading's first production caller: the store has computed it
// since the formula landed, and a number nobody could see was nobody's to
// disagree with.
func (h Handlers) GetDealHealth(w http.ResponseWriter, r *http.Request, id crmcontracts.Id) {
	now := time.Now().UTC()
	health, err := h.store.DealHealth(r.Context(), pathID[ids.DealKind](id), now)
	if err != nil {
		httperr.Write(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, healthReading(health, now))
}

// healthReading renders the four factors with their reasons. The reason is
// the fact the factor was read from, stated so a reader can disagree with the
// number rather than only with the mood.
func healthReading(h DealHealth, now time.Time) crmcontracts.DealHealthReading {
	ev := h.Evidence
	var recent *openapi_types.UUID
	if ev.MostRecentActivityID != nil {
		u := openapi_types.UUID(*ev.MostRecentActivityID)
		recent = &u
	}
	return crmcontracts.DealHealthReading{
		DealId:     openapi_types.UUID(h.DealID.UUID),
		Health:     round2(h.Health),
		AtRisk:     h.AtRisk,
		ComputedAt: now,
		Factors: []crmcontracts.DealHealthFactor{
			{
				Key: "activity_recency", Value: round2(h.Factors.ActivityRecency), Weight: round2(h.Weights.ActivityRecency),
				Reason: recencyReason(ev.LastActivityAt, ev.Stalled, now), ActivityId: recent,
			},
			{
				Key: "stage_velocity", Value: round2(h.Factors.StageVelocity), Weight: round2(h.Weights.StageVelocity),
				Reason: fmt.Sprintf("%d days in this stage; won deals spent about %d.", int(ev.DaysInStage), int(ev.ExpectedDaysInStage)),
			},
			{
				Key: "engagement", Value: round2(h.Factors.Engagement), Weight: round2(h.Weights.Engagement),
				Reason: engagementReason(len(ev.EngagedStakeholderIDs)),
			},
			{
				Key: "commitments", Value: round2(h.Factors.Commitments), Weight: round2(h.Weights.Commitments),
				Reason: commitmentsReason(len(ev.OverdueTaskIDs)),
			},
		},
	}
}

func recencyReason(last *time.Time, stalled bool, now time.Time) string {
	if last == nil {
		return "No activity has been logged on this deal."
	}
	days := int(now.Sub(*last).Hours() / 24)
	when := fmt.Sprintf("%d days ago", days)
	switch days {
	case 0:
		when = "today"
	case 1:
		when = "yesterday"
	}
	if stalled {
		return fmt.Sprintf("Last activity %s — the deal counts as stalled.", when)
	}
	return fmt.Sprintf("Last activity %s.", when)
}

func engagementReason(engaged int) string {
	switch engaged {
	case 0:
		return "No stakeholder has been in two-way contact in the last 90 days."
	case 1:
		return "One stakeholder in two-way contact in the last 90 days."
	default:
		return fmt.Sprintf("%d stakeholders in two-way contact in the last 90 days.", engaged)
	}
}

func commitmentsReason(overdue int) string {
	switch overdue {
	case 0:
		return "No overdue task on this deal."
	case 1:
		return "One task is overdue."
	default:
		return fmt.Sprintf("%d tasks are overdue.", overdue)
	}
}

// round2 keeps a factor readable on the wire; the formula is not that precise.
func round2(v float64) float32 {
	return float32(int(v*100+0.5)) / 100
}
