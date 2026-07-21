// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package ai

// The AIRT-WIRE-1 transport slice: GET /ai/usage serves the meter's
// day × task × tier aggregates plus the budget band and, per ADR-0067
// (price-on-read), each task line's estimated cost — priced at read
// time from the workspace's ai_model_rate sheet, never computed by the
// router/meter/adapters. A task line with no matching rate for any of
// its window's calls omits cost_est_minor rather than reporting a
// fabricated 0 (global constraint: cost is transparency, never a gate).

import (
	"net/http"
	"time"

	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
)

// maxUsageWindowDays bounds one report request: the aggregation is
// unpaginated, so an unbounded window would let a single call scan the
// whole metering history. A year covers every UI view.
const maxUsageWindowDays = 366

// GetAiUsage implements (GET /ai/usage).
func (h Handlers) GetAiUsage(w http.ResponseWriter, r *http.Request, params crmcontracts.GetAiUsageParams) {
	from, to := h.meter.UsageWindow()
	if params.From != nil {
		from = params.From.Time
	}
	if params.To != nil {
		to = params.To.Time
	}
	if to.Before(from) {
		httperr.Write(w, r, &httperr.DetailedError{
			Status: http.StatusUnprocessableEntity, Code: "window_inverted",
			Detail: "`from` must not be after `to`.",
		})
		return
	}
	if to.Sub(from) > maxUsageWindowDays*24*time.Hour {
		httperr.Write(w, r, &httperr.DetailedError{
			Status: http.StatusUnprocessableEntity, Code: "window_too_wide",
			Detail: "The usage window is capped at 366 days — narrow `from`/`to`.",
		})
		return
	}
	days, budget, err := h.meter.UsageReport(r.Context(), h.budget, h.rates, from, to)
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

// microUSDPerMinor converts ADR-0067's micro-USD price grain to wire
// minor units (USD cents): 1 cent = $0.01 = 1e4 micro-USD.
const microUSDPerMinor = 10_000

func wireAiUsage(days []DayUsage, budget BudgetStatus) crmcontracts.AiUsage {
	out := crmcontracts.AiUsage{Days: make([]aiUsageDay, 0, len(days))}
	for _, day := range days {
		wireDay := aiUsageDay{
			Date:  openapi_types.Date{Time: day.Day},
			Tasks: make([]aiUsageTask, 0, len(day.Tasks)),
		}
		for _, task := range day.Tasks {
			cached := task.CachedHits
			wireTask := aiUsageTask{
				Task:       task.Task,
				Tier:       task.Tier,
				Calls:      task.Calls,
				CachedHits: &cached,
				TokensIn:   task.TokensIn,
				TokensOut:  task.TokensOut,
			}
			// A task line that is ENTIRELY unpriced (every window call
			// lacking a rate row, so the summed cost is exactly 0 with no
			// priced call behind it) omits cost_est_minor rather than
			// reporting a fabricated 0 — the same "unpriced, not free"
			// distinction CostReport draws (price-on-read, ADR-0067). A
			// line with any priced cost reports it even if some of its
			// calls were unpriced: the number is a real, if partial, dollar
			// total, not an invented one.
			if task.CostEstMicroUSD > 0 || task.UnpricedCalls == 0 {
				minor := int(task.CostEstMicroUSD / microUSDPerMinor)
				wireTask.CostEstMinor = &minor
			}
			wireDay.Tasks = append(wireDay.Tasks, wireTask)
		}
		out.Days = append(out.Days, wireDay)
	}
	out.Budget.MonthlyTokens = int(budget.MonthlyTokens)
	out.Budget.SpentTokens = int(budget.SpentTokens)
	out.Budget.Band = crmcontracts.AiUsageBudgetBand(budget.Band)
	currency := "USD"
	out.Budget.Currency = &currency
	return out
}
