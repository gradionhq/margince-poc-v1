// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The cross-module edge behind resolve_entities: the people module owns the
// match ladder, the agents module owns the tool. Neither imports the other
// (ADR-0054 §3), so the edge is composed here.
//
// This file carries the two seam types across and NOTHING else. In particular
// it makes no decision about a match: the ladder's verdict travels verbatim and
// the tool reads it through what the caller may see. A translation layer that
// decided anything here would be a third opinion about identity, in the one
// place neither owner could see it.

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/modules/agents"
	"github.com/gradionhq/margince/backend/internal/modules/people"
)

// entityResolver adapts the people store's batch resolve to the tool seam.
func entityResolver(pool *pgxpool.Pool) agents.EntityResolver {
	store := people.NewStore(pool)
	return func(ctx context.Context, in []agents.ResolveCandidate) ([]agents.ResolveOutcome, error) {
		candidates := make([]people.ResolveCandidate, 0, len(in))
		for _, c := range in {
			candidates = append(candidates, people.ResolveCandidate{
				Kind:      people.ResolveKind(c.Kind),
				Name:      c.Name,
				LegalName: c.LegalName,
				Emails:    c.Emails,
				Phones:    c.Phones,
				Domains:   c.Domains,
			})
		}
		resolved, err := store.Resolve(ctx, candidates)
		if err != nil {
			return nil, err
		}
		out := make([]agents.ResolveOutcome, 0, len(resolved))
		for _, outcome := range resolved {
			answer := agents.ResolveOutcome{
				Verdict: string(outcome.Verdict),
				Refs:    make([]agents.ResolveRef, 0, len(outcome.Refs)),
			}
			for _, ref := range outcome.Refs {
				answer.Refs = append(answer.Refs, agents.ResolveRef{
					Kind:       string(ref.Kind),
					ID:         ref.ID,
					Confidence: ref.Confidence,
					MatchedOn:  ref.MatchedOn,
				})
			}
			out = append(out, answer)
		}
		return out, nil
	}
}
