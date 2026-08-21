// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package search

// What order an answer comes back in, once SQL has returned the rows.
//
// There are three orders a plan can ask for and only two of them are settled in
// SQL. The exact lane orders by t.id there; a radius orders by distance there.
// The similarity lane cannot — the retriever ranked the ids before the
// statement ran, so the ordering is applied here afterwards.
//
// Which of these two functions runs is the whole decision: orderByRank imposes
// the retriever's order, scoreOnly leaves SQL's alone. A radius plan takes the
// second, because it was already sorted nearest-first and re-sorting would undo
// the answer's whole point.

import (
	"slices"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// scoreOnly attaches similarity scores WITHOUT reordering.
//
// It exists for the one plan that asks for both a radius and a similarity: the
// rows come back nearest-first from SQL, and that is the order the answer keeps
// — but a caller reading a score still deserves the real one rather than a
// zero. Splitting this out of orderByRank keeps the two decisions separate:
// what the order IS, and what the scores ARE.
func scoreOnly(rows []QueryRow, ranked []Hit, limit int) []QueryRow {
	if len(ranked) > 0 {
		score := make(map[ids.UUID]float64, len(ranked))
		for _, hit := range ranked {
			score[hit.ID] = hit.Score
		}
		for i := range rows {
			rows[i].Score = score[rows[i].ID]
		}
	}
	if len(rows) > limit {
		rows = rows[:limit]
	}
	return rows
}

func orderByRank(rows []QueryRow, ranked []Hit, limit int) []QueryRow {
	if len(ranked) == 0 {
		return rows
	}
	position := make(map[ids.UUID]int, len(ranked))
	score := make(map[ids.UUID]float64, len(ranked))
	for i, hit := range ranked {
		position[hit.ID], score[hit.ID] = i, hit.Score
	}
	slices.SortFunc(rows, func(a, b QueryRow) int { return position[a.ID] - position[b.ID] })
	for i := range rows {
		rows[i].Score = score[rows[i].ID]
	}
	if len(rows) > limit {
		rows = rows[:limit]
	}
	return rows
}

// unavailableNotes renders the validator's unavailable predicates as the notes
// the answer carries instead of rows.
func unavailableNotes(plan ValidatedPlan) []QueryNote {
	notes := make([]QueryNote, 0, len(plan.Unavailable))
	for _, unavailable := range plan.Unavailable {
		notes = append(notes, QueryNote{
			Code: unavailable.Code, Path: unavailable.Path,
			Detail: unavailableDetail(unavailable.Code),
		})
	}
	if len(notes) == 0 {
		return nil
	}
	return notes
}

// unavailableDetail is the advice one unavailable code carries. It switches on
// the CODE rather than sharing a sentence: "ask for a city instead" is the
// right next step for a radius and nonsense for anything else, and a second
// code inheriting it would send a caller somewhere unrelated.
func unavailableDetail(code string) string {
	const cannotAnswer = "this deployment cannot answer that predicate, so no rows are returned"
	if code == CodeDistanceRankingUnavailable {
		return cannotAnswer + "; records carry no normalized coordinates yet — " +
			"ask for a city or region as an exact predicate instead"
	}
	return cannotAnswer
}

// answerShape is what answering the plan turned out to cost — the whole of
// what the verdict is decided from, beyond the plan itself.
type answerShape struct {
	// truncated: more rows matched than the page carries.
	truncated bool
	// degraded: a lane could not run as asked.
	degraded bool
	// abandoned: the statement ran out of its time budget, so there are no
	// rows at all rather than a short page of them.
	abandoned bool
}

// CoverageClasses is the CLOSED set coverageOf can answer with.
//
// It is exported so the surface that publishes these words can be held to
// publishing all of them: the tool restates them on its own side (it may not
// import this package) and refuses a class it does not know, so a fourth class
// added here without the wire learning about it would become a refused call at
// runtime rather than a build failure. The composition layer sees both and
// compares the two sets.
func CoverageClasses() []string {
	return []string{CoverageCompleteExact, CoverageRankedSemantic, CoveragePartialDegraded}
}

// coverageOf decides the verdict. Degradation dominates ranking and ranking
// dominates completeness, so the only answer that ever claims to be complete
// is one that ran exactly, whole, and undegraded. An abandoned statement is
// the same verdict from the other end — it never ran whole at all — which is
// why it needs no class of its own.
//
// Truncation is a verdict on the EXACT lane only. A ranked answer is a top-N
// by construction — that is what ranked_semantic says — so counting its bound
// as a degradation would leave the verdict unreachable and tell a caller
// nothing they did not ask for.
