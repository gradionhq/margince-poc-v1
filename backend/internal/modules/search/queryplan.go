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
	"errors"
	"io"
	"reflect"
	"slices"
	"strings"
	"sync"
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
	//
	// Raw rather than *int because a POINTER cannot tell an ABSENT member from
	// an explicit null — encoding/json produces nil for both — and the two
	// mean different things here: absent asks for the default, while null is
	// a value the grammar does not have. Silently reading null as absent
	// would answer a page size the caller never asked for.
	Limit json.RawMessage `json:"limit,omitempty"`
}

// Predicate is one exact `field op value` clause. Value carries the operand
// of a single-operand operator and Values the operand list of `in`; which
// member an operator reads is part of the operator's own definition, so
// filling the wrong one is a refusal, not a coercion.
type Predicate struct {
	Field string `json:"field"`
	Op    string `json:"op"`
	// Both operands stay RAW for the same reason Limit does: a decoded slice
	// cannot tell an absent `values` from an explicit null, so an operator
	// filling the member it does not read with null would slip past the
	// unused-operand refusal and be silently ignored.
	Value  json.RawMessage `json:"value,omitempty"`
	Values json.RawMessage `json:"values,omitempty"`
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
	// Anything after the plan is bytes the caller did not mean to send, and
	// accepting the first value while ignoring the rest is the same silent
	// narrowing DisallowUnknownFields exists to stop.
	//
	// The test is a second decode reaching io.EOF, not dec.More(). More()
	// reports whether another VALUE follows, so a stray delimiter — a plan
	// with a trailing `]` — leaves it false and the garbage accepted.
	var trailing json.RawMessage
	if err := dec.Decode(&trailing); !errors.Is(err, io.EOF) {
		return Plan{}, refuse("", CodeMalformedPlan,
			"the request carries more than one query plan document, or bytes after the end of this one; send exactly one")
	}
	return p, nil
}

// jsonFrame is one open container while the member scan walks a document.
// Only objects carry a seen-set; an array frame exists so the scan knows its
// elements are values rather than member names.
type jsonFrame struct {
	object    bool
	seen      map[string]bool
	expectKey bool
	// member is the name whose value is currently being read, which is what
	// tells a child container whether it is still grammar.
	member string
	// grammar marks a container the v1 GRAMMAR defines. An operand payload
	// (the object a `within_radius` operand carries) is a caller-authored
	// value, not grammar, so the canonical-spelling rule must not reach it —
	// `{"center": …}` is a legitimate operand, not an unknown plan member.
	// Repeated members are still refused inside one, because last-wins is
	// just as silent there.
	grammar bool
}

// The two operand members, named once so the grammar, the validator and the
// payload boundary below cannot disagree about their spelling.
const (
	memberValue  = "value"
	memberValues = "values"
)

// operandMembers are the grammar members whose VALUES are caller payloads.
// Anything below one of them stops being grammar.
var operandMembers = []string{memberValue, memberValues}

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
			if frame.grammar && !slices.Contains(canonicalMembers(), member) {
				return unknownPlanMember(member)
			}
			if frame.seen[member] {
				return refuse(member, CodeDuplicateMember,
					"the plan names "+quote(member)+" more than once; a member may appear once, "+
						"and this server will not choose which of them you meant")
			}
			frame.seen[member] = true
			frame.member = member
			frame.expectKey = false
			continue
		}
		if delim, isDelim := tok.(json.Delim); isDelim {
			switch delim {
			case '{':
				stack = append(stack, &jsonFrame{
					object: true, seen: map[string]bool{}, expectKey: true, grammar: childIsGrammar(frame),
				})
			case '[':
				stack = append(stack, &jsonFrame{grammar: childIsGrammar(frame)})
			case '}', ']':
				stack = stack[:len(stack)-1]
				valueConsumed(top())
			}
			continue
		}
		valueConsumed(frame)
	}
}

// childIsGrammar answers whether a container opening inside frame is still
// part of the grammar. The root is; a container under `value` or `values` is
// the caller's own payload; anything else inherits its parent.
func childIsGrammar(frame *jsonFrame) bool {
	if frame == nil {
		return true
	}
	if frame.object && slices.Contains(operandMembers, frame.member) {
		return false
	}
	return frame.grammar
}

// canonicalMembers is every member name the v1 grammar spells, derived from
// the grammar's own struct tags rather than listed beside them.
//
// The scan needs it because encoding/json matches member names
// CASE-INSENSITIVELY, and DisallowUnknownFields matches the same way: a plan
// carrying `"TARGET": "person"` alongside `"target": "deal"` is neither an
// unknown member nor — to a scan comparing exact strings — a duplicate, and
// the decoder resolves it last-wins. The caller's target is silently replaced.
// Requiring the canonical spelling is what closes that, and deriving the set
// means a member added to the grammar is admitted without a second edit.
var canonicalMembers = sync.OnceValue(func() []string {
	var names []string
	for _, t := range []reflect.Type{
		reflect.TypeOf(Plan{}), reflect.TypeOf(Predicate{}), reflect.TypeOf(Traversal{}),
	} {
		for i := range t.NumField() {
			name, _, _ := strings.Cut(t.Field(i).Tag.Get("json"), ",")
			if name != "" && name != "-" && !slices.Contains(names, name) {
				names = append(names, name)
			}
		}
	}
	return names
})

// valueConsumed tells an enclosing object that its member's value is complete,
// so the next token there is another member name.
func valueConsumed(frame *jsonFrame) {
	if frame != nil && frame.object {
		frame.expectKey = true
	}
}
