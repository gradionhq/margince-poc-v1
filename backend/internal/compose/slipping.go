// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The pipeline-risk seams behind whats_slipping_this_week and
// draft_follow_ups_for (interfaces.md §2.2): the candidate set comes
// from the deals module's own row-scoped list path (RBAC + row scope
// apply exactly as on the HTTP surface — never raw SQL around the
// store), and each follow-up draft lands through the same composite
// provider write path every tool rides.

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/modules/activities"
	"github.com/gradionhq/margince/backend/internal/modules/agents"
	"github.com/gradionhq/margince/backend/internal/modules/deals"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
)

// slippingScanLimit bounds each list sweep. An honest bound: a workspace
// with more open deals than this sees the most recently created ones —
// the tool is a triage set, not an exhaustive report (run_report is).
const slippingScanLimit = 50

// slippingLister serves the formulas-§8 candidate set: stalled open
// deals plus open deals whose expected close date is already past.
func slippingLister(pool *pgxpool.Pool) agents.SlippingLister {
	store := deals.NewStore(pool)
	return func(ctx context.Context) ([]agents.SlippingDeal, error) {
		limit := slippingScanLimit
		stalledOnly := true
		stalled, _, err := store.ListDeals(ctx, deals.ListDealsInput{Stalled: &stalledOnly, Limit: &limit})
		if err != nil {
			return nil, err
		}
		openStatus := "open"
		open, _, err := store.ListDeals(ctx, deals.ListDealsInput{Status: &openStatus, Limit: &limit})
		if err != nil {
			return nil, err
		}

		now := time.Now().UTC()
		today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
		out := make([]agents.SlippingDeal, 0, len(stalled))
		seen := map[ids.UUID]bool{}
		for _, d := range append(stalled, open...) {
			candidate := slippingCandidate(d, today)
			if seen[candidate.DealID] || (!candidate.Stalled && !candidate.CloseOverdue) {
				continue
			}
			seen[candidate.DealID] = true
			out = append(out, candidate)
		}
		return out, nil
	}
}

// slippingCandidate carries a deal row across the seam with its risk
// flags and the fields that evidence them; the tool drops any flag its
// evidence field cannot ground.
func slippingCandidate(d crmcontracts.Deal, today time.Time) agents.SlippingDeal {
	candidate := agents.SlippingDeal{
		DealID:         ids.UUID(d.Id),
		Name:           d.Name,
		AmountMinor:    d.AmountMinor,
		Currency:       d.Currency,
		Stalled:        d.Stalled != nil && *d.Stalled,
		LastActivityAt: d.LastActivityAt,
		CreatedAt:      d.CreatedAt,
	}
	if d.ExpectedCloseDate != nil {
		closeDate := d.ExpectedCloseDate.Time
		candidate.ExpectedCloseDate = &closeDate
		candidate.CloseOverdue = closeDate.Before(today)
	}
	return candidate
}

// followUpDrafter persists one follow-up as a draft NOTE on the deal's
// timeline — a note never reads as a sent (or even existing) email, so
// the draft cannot masquerade as communication that happened. It shares
// the deterministic draft voice with draft_email.
func followUpDrafter(provider datasource.SystemOfRecordProvider) agents.FollowUpDrafter {
	return func(ctx context.Context, deal agents.SlippingDeal) (ids.UUID, string, error) {
		subject, body := activities.DeterministicEmailDraft(deal.Name, "")
		fields, err := json.Marshal(map[string]any{
			"kind":    "note",
			"subject": "Draft follow-up: " + subject,
			"body":    body,
			"links": []map[string]any{
				{"entity_type": "deal", "entity_id": deal.DealID},
			},
		})
		if err != nil {
			return ids.Nil, "", err
		}
		ref, err := provider.Create(ctx, datasource.CreateInput{
			EntityType: datasource.EntityActivity,
			Fields:     fields,
			// The MCP provenance channel, same as every write on the tool
			// surface; captured_by comes from the principal, never from here.
			Source: "mcp",
		})
		if err != nil {
			return ids.Nil, "", err
		}
		return ref.ID, subject, nil
	}
}
