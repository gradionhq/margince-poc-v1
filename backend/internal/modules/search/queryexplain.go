// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package search

// The executed plan, in plain language.
//
// It is rendered from the VALIDATED plan — the thing that actually ran — and
// deterministically, with no model in the path. A sentence generated from the
// caller's request rather than from the executed plan would describe the query
// it hoped for, which is precisely the failure worth catching: rows read beside
// a sentence describing a different question are how a wrong answer becomes a
// trusted one.

import (
	"bytes"
	"encoding/json"
	"strconv"
	"strings"
)

// explainPlan renders one validated plan as a sentence.
func explainPlan(plan ValidatedPlan) string {
	sentence := plan.Target.Target + " records"
	if clause := explainPredicates(plan.Plan.Where); clause != "" {
		sentence += " where " + clause
	}
	sentence += explainHop(plan)
	if plan.Plan.SimilarTo != "" {
		sentence += ", ranked by similarity to " + quote(elideEcho(plan.Plan.SimilarTo))
	}
	sentence += "; at most " + strconv.Itoa(plan.Limit)
	if plan.Plan.SimilarTo == "" {
		sentence += ", newest first"
	}
	sentence += "."
	if unanswerable := explainUnavailable(plan); unanswerable != "" {
		sentence += " " + unanswerable
	}
	return sentence
}

// explainHop renders the traversal, naming the record type the hop lands on
// rather than the relation's own name, since a caller reads "at an
// organization" more readily than "over the organization relation".
func explainHop(plan ValidatedPlan) string {
	if plan.Hop == nil || plan.Plan.Traverse == nil {
		return ""
	}
	sentence := ", linked to " + article(plan.Hop.Target) + " " + plan.Hop.Target + " record"
	if clause := explainPredicates(plan.Plan.Traverse.Where); clause != "" {
		sentence += " where " + clause
	}
	return sentence
}

// explainUnavailable says which predicates did not run, and what that cost the
// answer. A note in a field a caller may not read is a note nobody reads.
func explainUnavailable(plan ValidatedPlan) string {
	if len(plan.Unavailable) == 0 {
		return ""
	}
	paths := make([]string, len(plan.Unavailable))
	for i, unavailable := range plan.Unavailable {
		paths[i] = unavailable.Path
	}
	return "No rows are returned: " + joinWithCommas(paths) +
		" cannot be answered by this deployment, and narrowing the plan silently would answer a different question."
}

// article picks the indefinite article a record type's name takes. It is a
// sentence a person reads, and "a organization" is the kind of seam that makes
// a generated explanation read as machine output rather than as an answer.
func article(word string) string {
	if word == "" || !strings.ContainsRune("aeiou", rune(word[0])) {
		return "a"
	}
	return "an"
}

func explainPredicates(clauses []Predicate) string {
	parts := make([]string, 0, len(clauses))
	for _, clause := range clauses {
		parts = append(parts, clause.Field+" "+comparatorPhrase(clause.Op)+" "+explainOperand(clause))
	}
	return strings.Join(parts, " and ")
}

// comparatorPhrase is each operator's reading. It is a total mapping over the
// operator set rather than a lookup with a fallback: an operator with no
// phrase would otherwise be rendered as its own machine name inside an
// English sentence, which reads as a bug in the answer rather than a gap in
// the sentence.
func comparatorPhrase(op string) string {
	switch op {
	case OpEq:
		return "is"
	case OpNeq:
		return "is not"
	case OpIn:
		return "is one of"
	case OpLt:
		return "is less than"
	case OpLte:
		return "is at most"
	case OpGt:
		return "is more than"
	case OpGte:
		return "is at least"
	case OpWithinRadius:
		return "is within"
	default:
		return op
	}
}

// explainOperand renders the operand the way the caller wrote it. It is their
// own text and already JSON, so it is compacted rather than re-encoded — a
// re-encoding would quietly normalise the value the sentence claims ran.
func explainOperand(clause Predicate) string {
	raw := clause.Value
	if clause.Op == OpIn {
		raw = clause.Values
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, raw); err != nil {
		// Unreachable on a validated plan, whose operands decoded already. An
		// operand that cannot be compacted is still shown, uncompacted, rather
		// than dropped: a sentence missing the value it filtered on is worse
		// than an untidy one.
		return elideEcho(string(raw))
	}
	return elideEcho(compact.String())
}

// maxNarrativeEcho bounds one caller-written fragment's contribution to the
// sentence.
//
// The narrative is CALLER TEXT traveling back to the caller, and on the agent
// surface that means it lands in the same run's later prompts — so an unbounded
// operand is an unbounded write into every prompt that follows. That is the
// hazard `maxBadArgsDetail` and `maxToolNameEcho` already bound on the two
// other echoes this surface has; the narrative was the third and had no bound
// at all. Generous next to any real filter value, and far short of what a plan
// can carry.
const maxNarrativeEcho = 200

// elideEcho keeps the sentence answering the question it exists for — "is this
// the query I asked for?" — without letting it become a channel. A caller who
// wrote a value this long is holding the whole of it already; what they need
// back is which clause ran, and how much of it there was.
func elideEcho(fragment string) string {
	if len(fragment) <= maxNarrativeEcho {
		return fragment
	}
	return fragment[:maxNarrativeEcho] + "…(" + strconv.Itoa(len(fragment)) + " bytes)"
}
