// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package ai

// The AIRT-WIRE-1 transport slice: GET /ai/usage serves the meter's
// day × task × tier aggregates plus the budget band. Token-denominated
// only — no cost estimation is configured, so cost_est_minor and
// currency are honestly omitted rather than invented.

import (
	"net/http"

	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
)

// GetAiUsage implements (GET /ai/usage).
func (h Handlers) GetAiUsage(w http.ResponseWriter, r *http.Request, params crmcontracts.GetAiUsageParams) {
	from, to := h.meter.UsageWindow()
	if params.From != nil {
		from = params.From.Time
	}
	if params.To != nil {
		to = params.To.Time
	}
	days, budget, err := h.meter.UsageReport(r.Context(), h.budget, from, to)
	if err != nil {
		httperr.Write(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, wireAiUsage(days, budget))
}

// aiUsage type aliases: the generated schema nests anonymous structs,
// so the mapping names them once here.
type aiUsageDay = struct {
	Date  openapi_types.Date `json:"date"`
	Tasks []aiUsageTask      `json:"tasks"`
}

type aiUsageTask = struct {
	CachedHits   *int `json:"cached_hits,omitempty"`
	Calls        int  `json:"calls"`
	CostEstMinor *int `json:"cost_est_minor,omitempty"`

	// Task capture_classify, enrich, summarize, …
	Task string `json:"task"`

	// Tier local_small, cheap_cloud, premium, local_large.
	Tier      string `json:"tier"`
	TokensIn  int    `json:"tokens_in"`
	TokensOut int    `json:"tokens_out"`
}

func wireAiUsage(days []DayUsage, budget BudgetStatus) crmcontracts.AiUsage {
	out := crmcontracts.AiUsage{Days: make([]aiUsageDay, 0, len(days))}
	for _, day := range days {
		wireDay := aiUsageDay{
			Date:  openapi_types.Date{Time: day.Day},
			Tasks: make([]aiUsageTask, 0, len(day.Tasks)),
		}
		for _, task := range day.Tasks {
			cached := task.CachedHits
			wireDay.Tasks = append(wireDay.Tasks, aiUsageTask{
				Task:       task.Task,
				Tier:       task.Tier,
				Calls:      task.Calls,
				CachedHits: &cached,
				TokensIn:   task.TokensIn,
				TokensOut:  task.TokensOut,
			})
		}
		out.Days = append(out.Days, wireDay)
	}
	out.Budget.MonthlyTokens = int(budget.MonthlyTokens)
	out.Budget.SpentTokens = int(budget.SpentTokens)
	out.Budget.Band = crmcontracts.AiUsageBudgetBand(budget.Band)
	return out
}
