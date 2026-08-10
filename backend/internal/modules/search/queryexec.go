// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package search

// Executing a validated plan (SEARCH-PARAM-7, the execution half).
//
// The executor takes a ValidatedPlan and nothing else, so nothing reaches a
// statement that has not passed the vocabulary. What it adds on top is the
// second half of the same contract: an answer that says what KIND of answer it
// is. Rows without a coverage verdict are the failure this file exists to
// prevent — a ranked top-N and an exhaustive count look identical on the wire,
// and a caller that cannot tell them apart will read one as the other.

import (
	"context"
	"fmt"
	"slices"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// The three coverage verdicts (the closed set the subsystem pins).
const (
	// CoverageCompleteExact: every clause ran exactly and the answer fits in
	// the page. This is the only verdict that claims completeness, and the
	// only one a caller may count with.
	CoverageCompleteExact = "complete_exact"
	// CoverageRankedSemantic: a similarity clause ordered the answer. Recall
	// is bounded by the ranking, which is what asking for similarity means.
	CoverageRankedSemantic = "ranked_semantic"
	// CoveragePartialDegraded: something could not be answered as asked — an
	// unavailable predicate, a degraded ranking lane, or an answer cut off at
	// the page.
	CoveragePartialDegraded = "partial_degraded"
)

// The notes that justify a verdict. One code per reason, so a caller branches
// on the reason rather than on prose.
const (
	// CodeSemanticRankingDegraded: the vector lane contributed nothing, so a
	// similarity clause ranked lexically. It names no CAUSE — the lane can be
	// unbound, unreachable, or bound with nothing yet stored under its identity,
	// and a caller can do the same thing about all three. Told rather than
	// hidden: a degradation nobody is told about is indistinguishable from a
	// working feature.
	CodeSemanticRankingDegraded = "semantic_ranking_degraded_to_lexical"
	// CodeResultTruncated: more rows match than the page carries. v1 has no
	// cursor member, so the rest cannot be asked for — which makes truncation
	// a degradation rather than pagination.
	CodeResultTruncated = "result_truncated_at_limit"
)

// QueryResult is one answered plan.
type QueryResult struct {
	Rows     []QueryRow
	Coverage string
	Notes    []QueryNote
	// Narrative is the executed plan in plain language. A caller reading rows
	// beside a sentence describing a different query is how a wrong answer
	// becomes a trusted one.
	Narrative string
	Limit     int
}

// QueryRow is one record the plan admitted, as a REFERENCE. This module may
// not import a sibling (ADR-0054 §3), so it answers ids and the display title
// each branch already declares; the tool layer hydrates through the datasource
// seam, which is where record shaping and the overlay's trust tier live.
type QueryRow struct {
	Type  string
	ID    ids.UUID
	Title string
	// Score is the similarity rank score, and zero on a plan that asked for
	// no ranking — an exact answer has no order to justify.
	Score float64
	// Evidence is the hop record that admitted this row, when the plan took a
	// traversal. It is what makes a hop legible as a reason rather than as an
	// invisible filter.
	Evidence []QueryEvidence
}

// QueryEvidence is one record that admitted a row, and why.
type QueryEvidence struct {
	Relation string
	Type     string
	ID       ids.UUID
	Title    string
}

// QueryNote is one machine-readable reason the coverage is what it is.
type QueryNote struct {
	Code string
	// Path is the plan-document path the note is about, and empty when the
	// note is about the answer as a whole.
	Path   string
	Detail string
}

// QueryExecutor answers validated plans.
type QueryExecutor struct {
	store    *Store
	embedder Embedder
	columns  ColumnReader
}

// NewQueryExecutor builds the executor over this module's own store, its
// embedder and the schema reader. The column reader is REQUIRED: the
// vocabulary's nil pass-through widens what may be asked, and a widened
// vocabulary is exactly what must not be executed against a table nobody
// checked.
func NewQueryExecutor(store *Store, embedder Embedder, columns ColumnReader) *QueryExecutor {
	return &QueryExecutor{store: store, embedder: embedder, columns: columns}
}

// Execute answers the plan.
//
// A plan carrying an UNAVAILABLE predicate answers with its notes and no rows
// (SEARCH-AC-17, and ValidatedPlan's own contract). Returning rows while
// quietly dropping the predicate would answer a different, wider question in a
// shape indistinguishable from the right one — and the row count would leak
// the size of an answer the caller cannot have.
func (e *QueryExecutor) Execute(ctx context.Context, plan ValidatedPlan) (QueryResult, error) {
	result := QueryResult{Limit: plan.Limit, Narrative: explainPlan(plan)}
	if notes := unavailableNotes(plan); len(notes) > 0 {
		result.Coverage, result.Notes = CoveragePartialDegraded, notes
		return result, nil
	}
	if e.columns == nil {
		return QueryResult{}, fmt.Errorf("search: the query executor has no schema reader wired")
	}
	binding, err := e.bindPlan(ctx, plan)
	if err != nil {
		return QueryResult{}, err
	}
	ranked, degraded, err := e.rankCandidates(ctx, plan)
	if err != nil {
		return QueryResult{}, err
	}
	if degraded {
		result.Notes = append(result.Notes, QueryNote{
			Code:   CodeSemanticRankingDegraded,
			Detail: "the meaning lane contributed nothing to this ranking, so it is lexical; similarity recall is narrower than it would otherwise be",
		})
	}
	binding.candidates = rankedIDs(ranked)
	if plan.Plan.SimilarTo != "" && len(binding.candidates) == 0 {
		// Nothing ranked, so nothing can be admitted. Running the statement
		// with an empty membership test would answer the unfiltered question.
		result.Coverage = coverageOf(plan, answerShape{degraded: degraded})
		return result, nil
	}
	rows, err := e.answerRows(ctx, plan, binding)
	if err != nil {
		return QueryResult{}, err
	}
	truncated := plan.Plan.SimilarTo == "" && len(rows) > plan.Limit
	if truncated {
		rows = rows[:plan.Limit]
		result.Notes = append(result.Notes, QueryNote{
			Code:   CodeResultTruncated,
			Detail: fmt.Sprintf("more records match than the page of %d carries; narrow the plan to see the rest", plan.Limit),
		})
	}
	result.Rows = orderByRank(rows, ranked, plan.Limit)
	result.Coverage = coverageOf(plan, answerShape{truncated: truncated, degraded: degraded})
	return result, nil
}

// bindPlan resolves where the plan's record types are stored and what they can
// answer.
func (e *QueryExecutor) bindPlan(ctx context.Context, plan ValidatedPlan) (planBinding, error) {
	branch, ok := branchFor(plan.Target.Target)
	if !ok {
		return planBinding{}, fmt.Errorf("search: %q is not a searchable record type", plan.Target.Target)
	}
	columns, err := storageFor(ctx, e.columns, plan.Target.Target)
	if err != nil {
		return planBinding{}, err
	}
	binding := planBinding{branch: branch, columns: columns, fetch: plan.Limit + 1}
	if plan.Hop == nil {
		return binding, nil
	}
	hopBranch, ok := branchFor(plan.Hop.Target)
	if !ok {
		return planBinding{}, fmt.Errorf("search: relation %q lands on %q, which is not a searchable record type",
			plan.Hop.Name, plan.Hop.Target)
	}
	hopColumns, err := storageFor(ctx, e.columns, plan.Hop.Target)
	if err != nil {
		return planBinding{}, err
	}
	hop := newHopBinding(*plan.Hop, hopBranch, hopColumns)
	binding.hop = &hop
	return binding, nil
}

// answerRows compiles and runs the statement. An unadmitted record type answers no
// rows rather than an error: object RBAC can change under a plan, and a caller
// who lost the grant mid-flight learns the same thing an empty workspace tells
// them.
func (e *QueryExecutor) answerRows(ctx context.Context, plan ValidatedPlan, binding planBinding) ([]QueryRow, error) {
	compiler := &planCompiler{}
	sql, admitted, err := compiler.compileStatement(ctx, plan, binding)
	if err != nil || !admitted {
		return nil, err
	}
	var rows []QueryRow
	err = database.WithWorkspaceTx(ctx, e.store.pool, func(tx pgx.Tx) error {
		queried, err := tx.Query(ctx, sql, compiler.args...)
		if err != nil {
			return fmt.Errorf("search: running the query plan: %w", err)
		}
		defer queried.Close()
		rows, err = scanPlanRows(queried, plan, binding)
		return err
	})
	if err != nil {
		return nil, err
	}
	return rows, nil
}

// scanPlanRows materializes the answer, carrying the hop row as the evidence
// that admitted each record.
func scanPlanRows(queried pgx.Rows, plan ValidatedPlan, binding planBinding) ([]QueryRow, error) {
	var rows []QueryRow
	for queried.Next() {
		row := QueryRow{Type: plan.Target.Target}
		var title, hopTitle *string
		var hopID *ids.UUID
		targets := []any{&row.ID, &title}
		if binding.hop != nil {
			targets = append(targets, &hopID, &hopTitle)
		}
		if err := queried.Scan(targets...); err != nil {
			return nil, fmt.Errorf("search: reading the query plan's answer: %w", err)
		}
		if title != nil {
			row.Title = *title
		}
		if hopID != nil {
			evidence := QueryEvidence{Relation: binding.hop.relation.Name, Type: binding.hop.relation.Target, ID: *hopID}
			if hopTitle != nil {
				evidence.Title = *hopTitle
			}
			row.Evidence = append(row.Evidence, evidence)
		}
		rows = append(rows, row)
	}
	if err := queried.Err(); err != nil {
		return nil, fmt.Errorf("search: reading the query plan's answer: %w", err)
	}
	return rows, nil
}

// rankCandidates runs the similarity clause through the module's hybrid arm, narrowed to
// the plan's target type. degraded reports that the arm fell back to the
// lexical lane. The ARM answers that now rather than being asked the same
// question twice here: there are two ways to lose the vector lane — no bound
// embed model, and an embed CALL that failed — and only the arm can see the
// second. Re-deriving it from the embedder alone said "semantic" about a page
// that was ranked by word overlap.
func (e *QueryExecutor) rankCandidates(ctx context.Context, plan ValidatedPlan) (ranked []Hit, degraded bool, err error) {
	if plan.Plan.SimilarTo == "" {
		return nil, false, nil
	}
	// Narrowed to the plan's own record type and overfetched. Narrowing is
	// what makes the ranking answer the question that was asked: a global
	// page filtered afterwards spends itself on types the plan never named,
	// and can come back empty for a target that simply ranks below five
	// others — a recall hole the caller reads as "no such records".
	//
	// Overfetching is what is left honest afterwards: the exact predicates
	// still run over the ranked candidates, so a page of them can narrow
	// further. That bound IS what ranked_semantic means.
	ranked, semantic, err := e.store.HybridSearch(ctx, plan.Plan.SimilarTo, e.embedder,
		clampLimit(plan.Limit*candidateDepth), plan.Target.Target)
	if err != nil {
		return nil, false, err
	}
	return ranked, !semantic, nil
}

// candidateDepth overfetches the ranking lane relative to the page, so the
// exact predicates have more than a page of ranked candidates to narrow.
const candidateDepth = 3

func rankedIDs(ranked []Hit) []ids.UUID {
	out := make([]ids.UUID, len(ranked))
	for i, hit := range ranked {
		out[i] = hit.ID
	}
	return out
}

// orderByRank puts the answer in the ranking's order and scores each row with
// the rank it came back with. An exact answer is already in the statement's
// order and keeps it.
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
}

// coverageOf decides the verdict. Degradation dominates ranking and ranking
// dominates completeness, so the only answer that ever claims to be complete
// is one that ran exactly, whole, and undegraded.
//
// Truncation is a verdict on the EXACT lane only. A ranked answer is a top-N
// by construction — that is what ranked_semantic says — so counting its bound
// as a degradation would leave the verdict unreachable and tell a caller
// nothing they did not ask for.
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

func coverageOf(plan ValidatedPlan, shape answerShape) string {
	switch {
	case shape.degraded || shape.truncated:
		return CoveragePartialDegraded
	case plan.Plan.SimilarTo != "":
		return CoverageRankedSemantic
	default:
		return CoverageCompleteExact
	}
}
