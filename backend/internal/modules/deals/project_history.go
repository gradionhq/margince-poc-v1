// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package deals

// The project's phase history, read back: every transition in the order it
// happened, and the fold that turns those rows into "how long were we
// selling versus delivering". The two writers (createProjectTx and
// recordPhaseTransition) append; this is the one reader.

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// PhaseTransition is one project_phase_history row. FromPhase is nil on the
// birth row, exactly as the writer leaves it.
type PhaseTransition struct {
	ID        ids.UUID
	FromPhase *string
	ToPhase   string
	Reason    *string
	// ChangedBy is the principal id the writer stamped (storekit.CapturedBy);
	// ChangedByName is that seat's display name when the id names an app
	// user, nil for a principal the user table does not hold.
	ChangedBy     string
	ChangedByName *string
	OccurredAt    time.Time
}

// PhaseDuration is the time a project has spent in one phase so far, summed
// over every visit — a re-opened project visits a phase twice, and the
// question "how long were we selling" wants the total.
type PhaseDuration struct {
	Phase   string
	Seconds int64
	// Current marks the phase the project is in now, whose duration is still
	// growing and was measured up to the instant the fold was given.
	Current bool
}

// ListProjectPhaseHistoryTx reads a project's transitions oldest first,
// inside a caller-opened transaction. The project must be visible to the
// caller: its history is a fact about the record, gated exactly as the
// record is.
func (s *Store) ListProjectPhaseHistoryTx(ctx context.Context, tx pgx.Tx, id ids.ProjectID) ([]PhaseTransition, error) {
	if err := auth.Require(ctx, projectObject, principal.ActionRead); err != nil {
		return nil, err
	}
	if err := auth.EnsureVisible(ctx, tx, projectObject, id.UUID); err != nil {
		return nil, err
	}
	// changed_by is a principal id, which names a person only under the human
	// namespace — the one spelling of that prefix is principal's, bound here
	// rather than retyped.
	var args []any
	arg := func(v any) int { args = append(args, v); return len(args) }
	rows, err := tx.Query(ctx, storekit.SQLf(`
		SELECT h.id, h.from_phase, h.to_phase, h.reason, h.changed_by, u.display_name, h.occurred_at
		FROM project_phase_history h
		LEFT JOIN app_user u ON $%d || u.id::text = h.changed_by
		WHERE h.project_id = $%d
		ORDER BY h.occurred_at ASC, h.id ASC`, arg(principal.HumanIDPrefix), arg(id)), args...)
	if err != nil {
		return nil, err
	}
	history, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (PhaseTransition, error) {
		var t PhaseTransition
		err := row.Scan(&t.ID, &t.FromPhase, &t.ToPhase, &t.Reason, &t.ChangedBy, &t.ChangedByName, &t.OccurredAt)
		return t, err
	})
	if err != nil {
		return nil, err
	}
	if history == nil {
		history = []PhaseTransition{}
	}
	return history, nil
}

// FoldPhaseDurations turns an oldest-first history into the time spent per
// phase. Each transition opens its to_phase; the phase closes at the next
// transition, or at now for the last one. Phases are reported in the order
// they were first entered, so a reader sees the ladder as the project walked
// it. An empty history folds to nothing: a project with no birth row is a
// row the writer never produced, not a project in no phase.
func FoldPhaseDurations(history []PhaseTransition, now time.Time) []PhaseDuration {
	out := []PhaseDuration{}
	index := map[string]int{}
	for i, t := range history {
		end := now
		if i+1 < len(history) {
			end = history[i+1].OccurredAt
		}
		seconds := int64(end.Sub(t.OccurredAt) / time.Second)
		if seconds < 0 {
			// A clock that ran backwards between two writes, or a now older
			// than the last transition: a negative stay is not a duration.
			seconds = 0
		}
		at, seen := index[t.ToPhase]
		if !seen {
			index[t.ToPhase] = len(out)
			out = append(out, PhaseDuration{Phase: t.ToPhase})
			at = len(out) - 1
		}
		out[at].Seconds += seconds
	}
	if len(history) > 0 {
		// Only the phase the LAST transition entered is current — a phase
		// revisited earlier is summed, not flagged.
		out[index[history[len(history)-1].ToPhase]].Current = true
	}
	return out
}
