// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package search

// The typed clarification a refused plan carries (SEARCH-AC-14): what was not
// understood, where, and what to do instead. It rides apperrors.FieldFaults,
// the plural fault form, so a plan with three bad predicates names all three
// — a caller told about the first of three has to make three round trips to
// learn what it could have learned in one.
//
// Implementing the shared fault interface is also what makes the refusal
// legible on every transport at once: the httperr choke point renders it as a
// 422 naming each path, and the MCP tool surface renders the same list
// in-band, with neither side hand-writing a mapping.

import (
	"encoding/json"
	"errors"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
)

// The refusal classes. One code per way a plan can fail to be a plan; the
// caller branches on these, so they are contract surface and their spellings
// are stable.
const (
	// CodeMalformedPlan: the document is not a well-formed plan at all.
	CodeMalformedPlan = "malformed_plan"
	// CodeDuplicateMember: one object names the same member twice. Refused
	// because the JSON decoder would otherwise resolve it last-wins in
	// silence, dropping the caller's first question.
	CodeDuplicateMember = "duplicate_member"
	// CodeUnknownPlanVersion: a grammar this validator does not implement.
	CodeUnknownPlanVersion = "unknown_plan_version"
	// CodeUnknownPlanMember: a member the v1 grammar has no place for —
	// where a free expression or an embedded fragment most often arrives.
	CodeUnknownPlanMember = "unknown_plan_member"
	// CodeUnknownTarget: not a record type this workspace searches. A plan
	// naming a TABLE lands here, since the target set is closed.
	CodeUnknownTarget = "unknown_target"
	// CodeUnknownField: not a field this caller may name on this target.
	// A field the caller cannot read is reported with this code and this
	// wording, identical to an invented one (SEARCH-AC-16).
	CodeUnknownField = "unknown_field"
	// CodeUnknownOperator: not an operator the field's type admits.
	CodeUnknownOperator = "unknown_operator"
	// CodeUnknownRelation: not a relationship the target declares.
	CodeUnknownRelation = "unknown_relation"
	// CodeSQLFragment: the token reads as SQL rather than as a name.
	CodeSQLFragment = "sql_fragment"
	// CodeFreeExpression: the token is an expression rather than a name.
	CodeFreeExpression = "free_expression"
	// CodeValueTypeMismatch: the operand shape does not match the field type.
	CodeValueTypeMismatch = "value_type_mismatch"
	// CodeValueMissing: the operator's operand member is absent or empty.
	CodeValueMissing = "value_missing"
	// CodeValueNotApplicable: the operand member this operator does not read
	// was filled in — refused rather than ignored, since an ignored member is
	// a question the caller asked and did not get answered.
	CodeValueNotApplicable = "value_not_applicable"
	// CodeLimitOutOfRange: outside the contract's CAP-PAGE window.
	CodeLimitOutOfRange = "limit_out_of_range"
	// CodePlanTooComplex: the plan is well-formed and every name in it is in
	// vocabulary, but it asks for more work than one statement may carry — too
	// many conditions in a list, or too many values in an `in`. It is its own
	// code because the caller's fix is different from every other refusal
	// here: nothing they wrote is wrong, there is simply too much of it.
	CodePlanTooComplex = "plan_too_complex"
	// CodeTraversalDepthExceeded: a second hop. Depth is capped in the
	// grammar, so this is a refusal and never a truncation.
	CodeTraversalDepthExceeded = "traversal_depth_exceeded"
)

// PlanRefusal is one or more typed clarifications about a single plan.
type PlanRefusal struct {
	Refusals []apperrors.FieldRefusal
}

// Error summarises the refusal for logs and errors.Is chains. The full,
// caller-facing detail is in FieldFaults — this string is never the thing a
// client renders.
func (r *PlanRefusal) Error() string {
	parts := make([]string, len(r.Refusals))
	for i, f := range r.Refusals {
		parts[i] = f.Field + ": " + f.Code
	}
	return "search: query plan refused (" + strings.Join(parts, ", ") + ")"
}

// FieldFaults renders every clarification, which is what a caller has to see
// to fix the plan in one edit rather than several.
func (r *PlanRefusal) FieldFaults() []apperrors.FieldRefusal { return r.Refusals }

// refuse builds a single-clarification refusal. path is the plan-document
// path of the offending member (`target`, `where[1].op`, `traverse.relation`)
// so the caller can find it without guessing.
func refuse(path, code, message string) *PlanRefusal {
	return &PlanRefusal{Refusals: []apperrors.FieldRefusal{{Field: path, Code: code, Message: message}}}
}

// identifier is the shape every NAME in a plan has: a lowercase snake_case
// word, optionally one dotted segment for a nested contract object
// (`address.city`). It is not an admission test — membership is decided by
// the resolved vocabulary — it only tells a refused token apart from an
// expression, so the caller is told which of the two it sent.
var identifier = regexp.MustCompile(`^[a-z][a-z0-9_]*(\.[a-z][a-z0-9_]*)?$`)

// sqlTokens are the words that make a refused token read as SQL rather than
// as a misspelled name.
var sqlTokens = []string{
	"select", "insert", "update", "delete", "drop", "alter", "truncate",
	"union", "join", "from", "where", "having", "exec",
}

// classify explains an ALREADY-REFUSED token. It never decides admission:
// membership in the resolved vocabulary is settled first and separately, and
// this runs only on what that check has already rejected.
//
// The ordering is the whole point. A classifier consulted FIRST would be a
// blocklist, and a blocklist admits everything it fails to recognise — the
// permissive default SEARCH-PARAM-7 exists to prevent. Consulted last, the
// worst it can do is choose a less apt wording for a refusal that has already
// happened.
func classify(token, unknownCode string) string {
	switch {
	case token == "":
		// An omitted name is not an expression and not SQL — it is a plan
		// that did not say what it was asking about, and calling it a free
		// expression would send the caller looking for one.
		return unknownCode
	case looksLikeSQL(token):
		return CodeSQLFragment
	case !identifier.MatchString(token):
		return CodeFreeExpression
	default:
		return unknownCode
	}
}

// looksLikeSQL reports whether the token carries statement punctuation or a
// SQL keyword as a whole word — `name` is a name, `select name` is not.
func looksLikeSQL(token string) bool {
	lower := strings.ToLower(token)
	if strings.ContainsAny(lower, ";") || strings.Contains(lower, "--") || strings.Contains(lower, "/*") {
		return true
	}
	words := strings.FieldsFunc(lower, func(r rune) bool { return r < 'a' || r > 'z' })
	return slices.ContainsFunc(words, func(w string) bool { return slices.Contains(sqlTokens, w) })
}

// unknownFieldMember matches the standard library's DisallowUnknownFields
// message, the one signal that distinguishes "you sent a member the grammar
// lacks" from "this is not JSON". The member name it quotes is the caller's
// own text, so passing it back leaks nothing.
var unknownFieldMember = regexp.MustCompile(`^json: unknown field "([^"]*)"$`)

// planDecodeRefusal turns a decode failure into the same typed clarification
// every other refusal uses, so a caller has one shape to read rather than a
// JSON error here and a plan refusal there.
func planDecodeRefusal(err error) *PlanRefusal {
	if m := unknownFieldMember.FindStringSubmatch(err.Error()); m != nil {
		return unknownPlanMember(m[1])
	}
	var typeErr *json.UnmarshalTypeError
	if errors.As(err, &typeErr) && typeErr.Field != "" {
		return refuse(typeErr.Field, CodeValueTypeMismatch,
			"the value of "+quote(typeErr.Field)+" is not the shape the v1 query plan gives it")
	}
	return refuse("", CodeMalformedPlan, "the request body is not a well-formed v1 query plan document")
}

// unknownPlanMember is the ONE refusal a member the grammar lacks gets,
// whether the raw scan caught it (a case-variant spelling, which the decoder
// would resolve last-wins) or the strict decode did (a name the grammar has
// never had). Two spellings of the same verdict would have a caller fixing
// two different things.
// It is deliberately NOT classified. classify explains a refused VOCABULARY
// token, where a caller who pasted SQL needs to be told so; a member name is
// a grammar question, and telling someone who wrote `"TARGET"` that they sent
// a free expression sends them looking for one. The member they must fix is
// named instead.
func unknownPlanMember(member string) *PlanRefusal {
	return refuse(member, CodeUnknownPlanMember,
		"the v1 query plan has no member named "+quote(member)+
			"; read margince://schema/query for the plan grammar")
}

// quote renders a caller-supplied token for a message.
//
// It ESCAPES rather than merely wrapping in quotes. The token is the caller's
// own text, and a token carrying a quote or a newline concatenated bare would
// end the quoted run early or split the message across lines — a refusal an
// agent parses as two, or as one it cannot tell the boundaries of.
func quote(s string) string { return strconv.Quote(s) }
