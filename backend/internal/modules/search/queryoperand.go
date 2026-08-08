// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package search

// Operand checking: does the value a clause carries match the KIND the field
// was admitted with, and is it in the member the operator actually reads.
//
// It sits beside the validator rather than inside it because it answers a
// different question. The validator decides whether a NAME is in the caller's
// vocabulary — the security boundary. This decides whether a VALUE has the
// shape that name's type takes, which is a question about the caller's data and
// not about their authority. Keeping the two apart is what lets the vocabulary
// half be read on its own.

import (
	"bytes"
	"encoding/json"
	"strconv"

	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
)

// checkOperand checks the clause's value against the operator's arity and the
// field's kind. It is where a plan that puts a string where a number belongs
// is refused rather than coerced — a coerced operand silently asks a
// different question.
func checkOperand(at string, field Field, clause Predicate) (apperrors.FieldRefusal, bool) {
	// The operand member an operator does NOT read is refused when present
	// rather than ignored — including when it is present as null. An ignored
	// member is a plan half-answered: a caller who wrote `op: "eq"` and
	// filled `values` meant the list, and silently matching on `value`
	// instead answers a different question.
	if unused, present := unusedOperand(clause); present {
		return operandRefusal(at+"."+unused, CodeValueNotApplicable,
			quote(clause.Op)+" reads "+quote(operandMember(clause.Op))+", not "+quote(unused)), true
	}
	if clause.Op == OpIn {
		return checkListOperand(at, field, clause)
	}
	if len(clause.Value) == 0 {
		return operandRefusal(at+"."+memberValue, CodeValueMissing,
			quote(clause.Op)+" needs a "+quote(memberValue)), true
	}
	if !operandMatches(field.Kind, clause.Value) {
		return operandRefusal(at+"."+memberValue, CodeValueTypeMismatch, operandMessage(clause.Field, field.Kind)), true
	}
	return apperrors.FieldRefusal{}, false
}

// checkListOperand judges the `in` operator's list: it must be present, be a
// list, be non-empty, and every element must have the field's shape.
func checkListOperand(at string, field Field, clause Predicate) (apperrors.FieldRefusal, bool) {
	var values []json.RawMessage
	if len(clause.Values) == 0 || isJSONNull(clause.Values) || json.Unmarshal(clause.Values, &values) != nil {
		return listOperandMissing(at), true
	}
	if len(values) == 0 {
		return listOperandMissing(at), true
	}
	if len(values) > maxOperandList {
		return operandRefusal(at+"."+memberValues, CodePlanTooComplex,
			"an `in` list may carry at most "+strconv.Itoa(maxOperandList)+
				" values; narrow the question, or ask it as more than one plan"), true
	}
	for i, v := range values {
		if !operandMatches(field.Kind, v) {
			return operandRefusal(at+"."+memberValues+"["+strconv.Itoa(i)+"]", CodeValueTypeMismatch,
				operandMessage(clause.Field, field.Kind)), true
		}
	}
	return apperrors.FieldRefusal{}, false
}

// listOperandMissing is the one refusal an absent, null, non-list or empty
// `in` operand gets: all four are the same thing to a caller — the list they
// have to supply is not there.
func listOperandMissing(at string) apperrors.FieldRefusal {
	return operandRefusal(at+"."+memberValues, CodeValueMissing,
		quote(OpIn)+" needs a non-empty "+quote(memberValues)+" list")
}

func operandRefusal(path, code, message string) apperrors.FieldRefusal {
	return apperrors.FieldRefusal{Field: path, Code: code, Message: message}
}

// operandMember names the member an operator reads: `in` takes a list,
// everything else a single value.
func operandMember(op string) string {
	if op == OpIn {
		return memberValues
	}
	return memberValue
}

// unusedOperand answers the operand member this operator does NOT read, when
// the plan filled it in anyway.
func unusedOperand(clause Predicate) (string, bool) {
	if clause.Op == OpIn {
		return memberValue, len(clause.Value) > 0
	}
	return memberValues, len(clause.Values) > 0
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
	if isJSONNull(raw) {
		return false
	}
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
	if isJSONNull(raw) {
		return false
	}
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
