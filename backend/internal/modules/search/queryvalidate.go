// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package search

// The validator — the security boundary of the whole query feature
// (SEARCH-AC-14). Everything a plan says is checked for MEMBERSHIP in the
// caller's resolved vocabulary, and anything outside it is refused with a
// typed clarification. There is no branch here that narrows a plan to the
// part it recognised: a query that half-parses is worse than one that fails,
// because its answer looks like every other answer.
//
// No execution lives here or anywhere else yet. A ValidatedPlan is a plan the
// executor may run, and producing it is the whole of this PR's job.

import (
	"bytes"
	"context"
	"encoding/json"
	"math"
	"slices"
	"strconv"

	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
)

// ValidatedPlan is a plan every token of which is in the caller's vocabulary.
// The executor takes one of these and nothing else, so a plan cannot reach
// execution without having passed through here.
type ValidatedPlan struct {
	Plan Plan
	// Target and Hop are the resolved vocabularies the plan was checked
	// against, carried so the executor joins on the derived relation rather
	// than re-deriving it from the plan's text.
	Target TargetVocabulary
	Hop    *Relation
	// Limit is the effective page size: the plan's, or the contract default
	// when it named none.
	Limit int
	// Unavailable names the predicates this deployment cannot answer as
	// asked — today only `within_radius`, which is declared in the
	// vocabulary and answers `distance_ranking_unavailable` (SEARCH-AC-17).
	//
	// A NON-EMPTY Unavailable is not advisory. The executor must answer with
	// these notes rather than with rows: returning results while quietly
	// dropping the predicate would be a ranking with nothing behind it,
	// which is the failure declaring the operator exists to avoid.
	Unavailable []Unavailable
}

// Unavailable is one predicate that validated but cannot be answered.
type Unavailable struct {
	// Path is the plan-document path of the predicate; Code the contract's
	// machine name for why it cannot be answered.
	Path string
	Code string
}

// CodeDistanceRankingUnavailable is the answer `within_radius` gives until
// normalized coordinates exist (SEARCH-AC-17). The operator is DECLARED
// rather than omitted on purpose: omitting it sends a caller to a text match
// on a city name, which quietly returns a different answer.
const CodeDistanceRankingUnavailable = "distance_ranking_unavailable"

// PlanValidator checks a plan against the caller's resolved vocabulary.
type PlanValidator struct {
	vocab *VocabularyResolver
}

// NewPlanValidator builds a validator over a vocabulary resolver.
func NewPlanValidator(vocab *VocabularyResolver) *PlanValidator {
	return &PlanValidator{vocab: vocab}
}

// Validate answers the validated plan, or a *PlanRefusal naming every token
// that was not understood.
//
// It refuses the plan's VERSION and TARGET first and returns immediately on
// either, because both decide what the rest of the plan even means: reporting
// predicate refusals against a vocabulary the caller did not ask for would
// name fields that have nothing to do with the question.
func (v *PlanValidator) Validate(ctx context.Context, plan Plan) (ValidatedPlan, error) {
	if plan.Version != PlanVersion {
		return ValidatedPlan{}, refuse("version", CodeUnknownPlanVersion,
			"this server validates query plans of version "+quote(PlanVersion)+" only")
	}
	vocab, err := v.vocab.Resolve(ctx, append([]string{plan.Target}, hopTargets(plan)...)...)
	if err != nil {
		return ValidatedPlan{}, err
	}
	target, ok := vocab.Target(plan.Target)
	if !ok {
		return ValidatedPlan{}, refuse("target", classify(plan.Target, CodeUnknownTarget),
			"the query plan cannot ask about "+quote(plan.Target)+
				"; read margince://schema/query for the record types available to you")
	}

	validated := ValidatedPlan{Plan: plan, Target: target}
	var refusals []apperrors.FieldRefusal
	refusals = append(refusals, checkPredicates(target, "where", plan.Where, &validated)...)
	refusals = append(refusals, v.checkTraversal(vocab, plan.Traverse, &validated)...)
	limit, limitRefusal := effectiveLimit(plan.Limit)
	refusals = append(refusals, limitRefusal...)
	validated.Limit = limit

	if len(refusals) > 0 {
		return ValidatedPlan{}, &PlanRefusal{Refusals: refusals}
	}
	return validated, nil
}

// hopTarget names the record type a traversal lands on, so Resolve reads that
// vocabulary in the same pass. The relation is not resolved yet — the name is
// mapped to a target only after the vocabulary exists — so this asks for
// every record type SOME relation of that name could reach, which is at most
// one per searchable entity and in practice one.
func hopTargets(plan Plan) []string {
	if plan.Traverse == nil {
		return nil
	}
	var targets []string
	for entity := range contractRecords {
		if plan.Traverse.Relation == entity || plan.Traverse.Relation == entity+"s" {
			targets = append(targets, entity)
		}
	}
	return targets
}

// checkTraversal admits at most one hop. A nested second hop is refused by
// NAME rather than dropped, so the caller learns the depth cap instead of
// receiving an answer to the shallower question it did not ask.
func (v *PlanValidator) checkTraversal(vocab Vocabulary, hop *Traversal, into *ValidatedPlan) []apperrors.FieldRefusal {
	if hop == nil {
		return nil
	}
	if hop.Traverse != nil {
		return []apperrors.FieldRefusal{{
			Field: "traverse.traverse", Code: CodeTraversalDepthExceeded,
			Message: "a v1 query plan takes one relationship hop; ask the second hop as its own query",
		}}
	}
	relation, ok := into.Target.Relation(hop.Relation)
	if !ok {
		return unknownRelation(into.Target.Target, hop.Relation)
	}
	hopVocab, ok := vocab.Target(relation.Target)
	if !ok {
		// Fail-closed, and the same refusal a caller would get for a hop that
		// does not exist — so even reaching here discloses nothing. What keeps
		// it unreachable is that relation NAMES and the narrowing in
		// hopTargets are derived from the same entity names, which
		// TestEveryDerivedRelationNameIsResolvableByTheValidatorsNarrowing
		// asserts; rename one without the other and that test fails rather
		// than a valid hop quietly refusing.
		return unknownRelation(into.Target.Target, hop.Relation)
	}
	into.Hop = &relation
	return checkPredicates(hopVocab, "traverse.where", hop.Where, into)
}

// unknownRelation is the ONE refusal a hop this caller may not take gets —
// whether the relation never existed, or exists and lands on a record type
// they cannot read. One spelling, because two would be a discovery channel.
func unknownRelation(target, name string) []apperrors.FieldRefusal {
	return []apperrors.FieldRefusal{{
		Field: "traverse.relation", Code: classify(name, CodeUnknownRelation),
		Message: quote(target) + " has no relationship named " + quote(name) +
			"; read margince://schema/query for the hops available to you",
	}}
}

// checkPredicates checks every clause of one where-list and returns a
// refusal per bad clause — all of them, not the first.
func checkPredicates(vocab TargetVocabulary, path string, clauses []Predicate, into *ValidatedPlan) []apperrors.FieldRefusal {
	var refusals []apperrors.FieldRefusal
	for i, clause := range clauses {
		at := path + "[" + strconv.Itoa(i) + "]"
		if refusal, ok := checkPredicate(vocab, at, clause, into); ok {
			refusals = append(refusals, refusal)
		}
	}
	return refusals
}

// checkPredicate checks one clause, reporting at most one refusal: the first
// thing wrong with it decides what the rest of it even means, so an unknown
// field is not also reported as an unknown operator.
func checkPredicate(vocab TargetVocabulary, at string, clause Predicate, into *ValidatedPlan) (apperrors.FieldRefusal, bool) {
	field, ok := vocab.Field(clause.Field)
	if !ok {
		// The wording here is the ONE a caller sees for both an invented
		// field and a real field they may not read (SEARCH-AC-16). Nothing
		// about it may vary with which of the two it was, or vocabulary
		// probing becomes field discovery.
		return apperrors.FieldRefusal{
			Field: at + ".field", Code: classify(clause.Field, CodeUnknownField),
			Message: "the query plan cannot name " + quote(clause.Field) + " on " + quote(vocab.Target) +
				"; read margince://schema/query for the fields available to you",
		}, true
	}
	if !fieldAdmitsOp(field, clause.Op) {
		return apperrors.FieldRefusal{
			Field: at + ".op", Code: classify(clause.Op, CodeUnknownOperator),
			Message: quote(clause.Field) + " is a " + string(field.Kind) + " field and admits " +
				joinQuoted(field.Ops) + "; " + quote(clause.Op) + " is not one of them",
		}, true
	}
	if refusal, bad := checkOperand(at, field, clause); bad {
		return refusal, true
	}
	if clause.Op == OpWithinRadius {
		into.Unavailable = append(into.Unavailable, Unavailable{Path: at, Code: CodeDistanceRankingUnavailable})
	}
	return apperrors.FieldRefusal{}, false
}

// fieldAdmitsOp reports whether the operator is one this field's kind gives
// it. Membership, not a blocklist: an operator absent from the kind's set is
// refused whether or not this file has ever heard of it.
func fieldAdmitsOp(field Field, op string) bool {
	return slices.Contains(field.Ops, op)
}

// effectiveLimit answers the plan's page size, or the contract default when
// it named none. A limit outside the CAP-PAGE window is REFUSED rather than
// clamped: clamping answers a narrower question than the caller asked
// without saying so, which is the silent narrowing SEARCH-AC-14 forbids.
func effectiveLimit(limit *int) (int, []apperrors.FieldRefusal) {
	if limit == nil {
		return storekit.ClampLimit(nil), nil
	}
	if *limit < 1 || *limit > maxPlanLimit {
		return 0, []apperrors.FieldRefusal{{
			Field: "limit", Code: CodeLimitOutOfRange,
			Message: "limit must be between 1 and " + strconv.Itoa(maxPlanLimit) + "; ask for a page and follow it with another",
		}}
	}
	return *limit, nil
}

// maxPlanLimit is the contract's CAP-PAGE ceiling, DISCOVERED from the shared
// clamp rather than restated here: asking the clamp for an absurd page
// returns the ceiling itself, so a plan can never disagree with what every
// other list on the surface will serve.
var maxPlanLimit = storekit.ClampLimit(&absurdPageRequest)

var absurdPageRequest = math.MaxInt32

// joinQuoted renders an operator set for a refusal message.
func joinQuoted(ops []string) string {
	quoted := make([]string, len(ops))
	for i, op := range ops {
		quoted[i] = quote(op)
	}
	return joinWithCommas(quoted)
}

func joinWithCommas(parts []string) string {
	out := ""
	for i, p := range parts {
		switch {
		case i == 0:
			out = p
		case i == len(parts)-1:
			out += " and " + p
		default:
			out += ", " + p
		}
	}
	return out
}

// checkOperand checks the clause's value against the operator's arity and the
// field's kind. It is where a plan that puts a string where a number belongs
// is refused rather than coerced — a coerced operand silently asks a
// different question.
func checkOperand(at string, field Field, clause Predicate) (apperrors.FieldRefusal, bool) {
	// The operand member an operator does NOT read is refused when present
	// rather than ignored. An ignored member is a plan half-answered: a
	// caller who wrote `op: "eq"` and filled `values` meant the list, and
	// silently matching on `value` instead answers a different question.
	if unused, present := unusedOperand(clause); present {
		return operandRefusal(at+"."+unused, CodeValueNotApplicable,
			quote(clause.Op)+" reads "+quote(operandMember(clause.Op))+", not "+quote(unused)), true
	}
	if clause.Op == OpIn {
		if len(clause.Values) == 0 {
			return operandRefusal(at+".values", CodeValueMissing,
				quote(OpIn)+" needs a non-empty "+quote("values")+" list"), true
		}
		for i, v := range clause.Values {
			if !operandMatches(field.Kind, v) {
				return operandRefusal(at+".values["+strconv.Itoa(i)+"]", CodeValueTypeMismatch,
					operandMessage(clause.Field, field.Kind)), true
			}
		}
		return apperrors.FieldRefusal{}, false
	}
	if len(clause.Value) == 0 {
		return operandRefusal(at+".value", CodeValueMissing,
			quote(clause.Op)+" needs a "+quote("value")), true
	}
	if !operandMatches(field.Kind, clause.Value) {
		return operandRefusal(at+".value", CodeValueTypeMismatch, operandMessage(clause.Field, field.Kind)), true
	}
	return apperrors.FieldRefusal{}, false
}

func operandRefusal(path, code, message string) apperrors.FieldRefusal {
	return apperrors.FieldRefusal{Field: path, Code: code, Message: message}
}

// operandMember names the member an operator reads: `in` takes a list,
// everything else a single value.
func operandMember(op string) string {
	if op == OpIn {
		return "values"
	}
	return "value"
}

// unusedOperand answers the operand member this operator does NOT read, when
// the plan filled it in anyway.
func unusedOperand(clause Predicate) (string, bool) {
	if clause.Op == OpIn {
		return "value", len(clause.Value) > 0
	}
	return "values", clause.Values != nil
}

func operandMessage(field string, kind FieldKind) string {
	return quote(field) + " is a " + string(kind) + " field; its operand must be a " + operandShape(kind)
}

// operandShape names the JSON shape a kind's operand takes, for the message.
func operandShape(kind FieldKind) string {
	switch kind {
	case KindNumber:
		return "number"
	case KindBoolean:
		return "boolean"
	case KindGeo:
		return `{"center": <text>, "radius_km": <number>}`
	case KindText, KindID, KindDate, KindTimestamp:
		return "string"
	default:
		return "string"
	}
}

// operandMatches reports whether a raw JSON operand has the shape the kind
// takes. Each kind decodes into its OWN Go type rather than into a generic
// value that is then type-switched: decoding is the check, so a `1` offered
// where a string belongs fails at the decode instead of after it.
//
// Dates and timestamps are strings on the wire (the contract's own encoding);
// their FORMAT is the executor's business, and refusing a malformed date here
// would duplicate a check with nothing to compare it to.
func operandMatches(kind FieldKind, raw json.RawMessage) bool {
	switch kind {
	case KindNumber:
		var v float64
		return json.Unmarshal(raw, &v) == nil
	case KindBoolean:
		var v bool
		return json.Unmarshal(raw, &v) == nil
	case KindGeo:
		return geoOperandMatches(raw)
	case KindText, KindID, KindDate, KindTimestamp:
		var v string
		return json.Unmarshal(raw, &v) == nil
	default:
		return false
	}
}

// radiusOperand is what `within_radius` takes: a place to measure from and a
// distance. RadiusKM is a pointer so an ABSENT radius is distinguishable from
// a zero one — both are refused, but for different reasons, and a plan that
// meant `0` should not read as a plan that forgot.
type radiusOperand struct {
	Center   string   `json:"center"`
	RadiusKM *float64 `json:"radius_km"`
}

// geoOperandMatches checks the radius operand's shape. The operator does not
// run — it answers `distance_ranking_unavailable` — but the shape is still
// checked, so the note a caller gets back is about this deployment's
// capability rather than about their own malformed request. The decode is
// strict for the same reason the plan's is: a `unit: miles` this validator
// drops is a request answered differently from the one that was sent.
func geoOperandMatches(raw json.RawMessage) bool {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var operand radiusOperand
	if err := dec.Decode(&operand); err != nil {
		return false
	}
	return operand.Center != "" && operand.RadiusKM != nil && *operand.RadiusKM > 0
}

// assertion that the refusal really is the plural fault form the transports
// render; a refusal that stopped implementing it would degrade to an
// unclassified internal error on the tool surface.
var _ apperrors.FieldFaults = (*PlanRefusal)(nil)
