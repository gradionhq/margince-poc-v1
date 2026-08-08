// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The brief, as the agent surface reads it.
//
// briefs is a compose subpackage and agents is a module, so the edge between
// them is wired here like every other cross-module edge (ADR-0054 §9). What
// crosses is one function: the acting human's latest PERSISTED run. The
// refresh, and the per-item marks, are not offered — they are how a person
// notices what an agent did.

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/compose/briefs"
	"github.com/gradionhq/margince/backend/internal/modules/agents"
	"github.com/gradionhq/margince/backend/internal/modules/people"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// briefReader binds the tool to the same engine entry point the home route
// calls, so an agent and the person it acts for read one queue rather than two
// readings of it. The engine resolves the run through the acting principal's
// own user id and requires deal-read, so no scoping is added or re-decided
// here.
func briefReader(pool *pgxpool.Pool) agents.BriefReader {
	engine := briefs.NewBriefEngine(pool, people.NewStore(pool))
	return func(ctx context.Context) (agents.ReadBriefResult, error) {
		// The instant is the read's own, exactly as the HTTP handler passes it:
		// LatestRun resolves snoozes against it, so a run read now is what the
		// rep should see now.
		run, err := engine.LatestRun(ctx, time.Now().UTC())
		if err != nil {
			return agents.ReadBriefResult{}, err
		}
		return briefRunToTool(run), nil
	}
}

func briefRunToTool(run briefs.BriefRun) agents.ReadBriefResult {
	items := make([]agents.BriefItem, 0, len(run.Items))
	for _, item := range run.Items {
		items = append(items, agents.BriefItem{
			ItemID: item.ID, DealID: item.DealID, Rank: item.Rank,
			Composite: item.Composite, State: item.State,
			// Never null on the wire: an empty evidence list and an unread one
			// are different facts, and only one of them can be true here —
			// the brief's own evidence-or-omit rule means an item without
			// evidence was never queued.
			EvidenceIDs:  append(make([]ids.UUID, 0, len(item.EvidenceIDs)), item.EvidenceIDs...),
			SnoozedUntil: item.SnoozedUntil,
		})
	}
	return agents.ReadBriefResult{
		BriefID: run.ID, GeneratedAt: run.GeneratedAt, AsOf: run.AsOf,
		CandidateCount: run.CandidateCount, Items: items,
	}
}
