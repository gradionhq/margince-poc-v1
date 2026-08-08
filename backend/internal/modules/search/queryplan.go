// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package search

// The v1 query-plan grammar (search-and-retrieval SEARCH-PARAM-7 / A140).
// A plan is what a natural-language layer compiles an utterance INTO, and
// this file is the whole of what a plan may say: a target, exact predicates,
// one similarity clause, one traversal hop, a limit.
//
// The grammar is the first half of the security boundary and the reason the
// shape below has no free-text member anywhere a name is expected. There is
// nowhere in it to put a table, a fragment of SQL or an expression: those can
// only arrive as the VALUE of a closed member, where the vocabulary refuses
// them. The second half is queryvalidate.go, which decides membership; this
// file only decides shape.

import (
	"bytes"
	"encoding/json"
)

// PlanVersion is the only grammar this validator accepts. A plan naming any
// other version is refused rather than interpreted: a v2 plan carries members
// (grouping, ranking, multi-hop, coverage) whose absence a v1 executor would
// silently ignore, and an ignored member is an answer to a different question.
const PlanVersion = "v1"

// Plan is one compiled question.
type Plan struct {
	Version string `json:"version"`
	// Target names the record type the plan asks about, from the closed set
	// this module already searches.
	Target string      `json:"target"`
	Where  []Predicate `json:"where,omitempty"`
	// SimilarTo is the single similarity clause v1 admits (SEARCH-PARAM-7:
	// "one similarity clause over the existing hybrid arm"). It is a member
	// rather than a list precisely so a second one cannot be expressed.
	SimilarTo string     `json:"similar_to,omitempty"`
	Traverse  *Traversal `json:"traverse,omitempty"`
	// Limit is the page size, bounded by the contract's CAP-PAGE window.
	// Absent means the contract default; out of range is refused rather than
	// clamped, because a clamp answers a narrower question than the one asked
	// without saying so.
	Limit *int `json:"limit,omitempty"`
}

// Predicate is one exact `field op value` clause. Value carries the operand
// of a single-operand operator and Values the operand list of `in`; which
// member an operator reads is part of the operator's own definition, so
// filling the wrong one is a refusal, not a coercion.
type Predicate struct {
	Field  string            `json:"field"`
	Op     string            `json:"op"`
	Value  json.RawMessage   `json:"value,omitempty"`
	Values []json.RawMessage `json:"values,omitempty"`
}

// Traversal is one relationship hop.
//
// Traverse nests DELIBERATELY. v1 admits depth 1, and the cap belongs to the
// grammar rather than to a runtime budget — but a grammar that simply had no
// member for a second hop would leave a strict decoder rejecting a depth-2
// plan as a malformed document, which tells the caller nothing about the
// limit it hit. Carrying the member and refusing it by name is what makes the
// cap legible: `traversal_depth_exceeded`, naming the hop that was too many.
type Traversal struct {
	Relation string      `json:"relation"`
	Where    []Predicate `json:"where,omitempty"`
	Traverse *Traversal  `json:"traverse,omitempty"`
}

// DecodePlan parses a plan document STRICTLY: a member the grammar has no
// place for is refused, never dropped. A dropped member is the permissive
// default this whole feature exists to prevent — a plan carrying
// `"raw_sql": "…"` that decodes cleanly into a Plan without it has been
// silently narrowed into a different question whose answer looks like every
// other answer.
func DecodePlan(raw []byte) (Plan, error) {
	// Duplicate members are checked BEFORE the struct decode, because the
	// struct decode cannot see them: encoding/json silently takes the LAST
	// value, so a plan carrying two `where` lists validates on the second and
	// the first question is dropped without a word.
	if refusal := refuseDuplicateMembers(raw); refusal != nil {
		return Plan{}, refusal
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var p Plan
	if err := dec.Decode(&p); err != nil {
		return Plan{}, planDecodeRefusal(err)
	}
	// A second JSON value after the plan is a document the caller did not
	// mean to send; accepting the first and ignoring the rest is the same
	// silent narrowing DisallowUnknownFields exists to stop.
	if dec.More() {
		return Plan{}, refuse("", CodeMalformedPlan,
			"the request carries more than one JSON document; send exactly one query plan")
	}
	return p, nil
}

// jsonFrame is one open container while the duplicate-member scan walks a
// document. Only objects carry a seen-set; an array frame exists so the scan
// knows its elements are values rather than member names.
type jsonFrame struct {
	object    bool
	seen      map[string]bool
	expectKey bool
}

// refuseDuplicateMembers refuses a document that names the same member twice
// inside one object, at any depth.
//
// It exists because DisallowUnknownFields does not cover this: a REPEATED
// member is not an unknown one, and encoding/json resolves it last-wins in
// silence. `{"target":"deal","where":[…],"where":[]}` decodes without error
// into an unfiltered scan — the caller's actual question dropped, and the
// answer indistinguishable from any other answer. That is the exact failure
// SEARCH-AC-14 forbids, so the document is refused rather than resolved.
//
// A document that is not well-formed JSON answers nil here; the struct decode
// that follows is what reports it, so there is one malformed-plan refusal
// rather than two spellings of it.
func refuseDuplicateMembers(raw []byte) *PlanRefusal {
	dec := json.NewDecoder(bytes.NewReader(raw))
	var stack []*jsonFrame
	top := func() *jsonFrame {
		if len(stack) == 0 {
			return nil
		}
		return stack[len(stack)-1]
	}
	for {
		tok, err := dec.Token()
		if err != nil {
			// Neither outcome is this pass's to report: io.EOF is the clean
			// end of a fully scanned document, and a syntax error is reported
			// once by the struct decode that follows. Answering a refusal here
			// would give a malformed plan two different spellings.
			//nolint:nilerr // a scan fault is deliberately not a refusal; see above
			return nil
		}
		frame := top()
		if frame != nil && frame.object && frame.expectKey {
			member, isKey := tok.(string)
			if !isKey {
				// The only non-string token where a member name may stand is
				// the '}' closing this object.
				stack = stack[:len(stack)-1]
				valueConsumed(top())
				continue
			}
			if frame.seen[member] {
				return refuse(member, CodeDuplicateMember,
					"the plan names "+quote(member)+" more than once; a member may appear once, "+
						"and this server will not choose which of them you meant")
			}
			frame.seen[member] = true
			frame.expectKey = false
			continue
		}
		if delim, isDelim := tok.(json.Delim); isDelim {
			switch delim {
			case '{':
				stack = append(stack, &jsonFrame{object: true, seen: map[string]bool{}, expectKey: true})
			case '[':
				stack = append(stack, &jsonFrame{})
			case '}', ']':
				stack = stack[:len(stack)-1]
				valueConsumed(top())
			}
			continue
		}
		valueConsumed(frame)
	}
}

// valueConsumed tells an enclosing object that its member's value is complete,
// so the next token there is another member name.
func valueConsumed(frame *jsonFrame) {
	if frame != nil && frame.object {
		frame.expectKey = true
	}
}
