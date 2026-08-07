// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package agents

// The claims a tool's schema makes about its NUMERIC arguments, and their
// enforcement — the sibling of idargs.go, at the same chokepoint and for the
// same reason: the bounds are read off the schema once at registration and held
// at Registry.Invoke, so what tools/list advertises is true of the SURFACE
// rather than of whichever handlers remembered to check.
//
// A `minimum`/`maximum` is not decoration. It is the only thing a client has to
// size a call by, and a server that advertises `maximum: 50` while serving
// 999999 teaches the next caller that the rest of the schema is advisory too —
// after which every other declared claim has to be re-discovered by experiment.
// decodeArgs already refuses a value of the wrong TYPE for these fields and any
// argument the schema does not declare; the advertised RANGE was the one claim
// with nothing behind it.
//
// Scope: top-level integer/number properties. `maxItems` on an array is a
// different claim about a different thing — the cost of the reads that array
// buys — and is held where that cost is paid (bookingLinks).

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// numBound is one numeric argument's advertised range. Both ends are optional:
// a schema may declare a floor, a ceiling, or both, and a property declaring
// neither is not carried at all.
type numBound struct {
	name string
	min  *float64
	max  *float64
}

// declaredNumBounds reads a schema's numeric bounds once, at registration.
//
// float64 holds every bound this surface declares and every value JSON can
// carry in these fields, and the comparison it serves is a directional one —
// so it does not owe the exactness a stored amount would.
func declaredNumBounds(inputSchema json.RawMessage) []numBound {
	var schema struct {
		Properties map[string]struct {
			Type    string   `json:"type"`
			Minimum *float64 `json:"minimum"`
			Maximum *float64 `json:"maximum"`
		} `json:"properties"`
	}
	// assertObjectSchemas has already confirmed this is valid JSON declaring an
	// object, but it decodes only `type` — a schema whose `properties` is not a
	// map, or whose bound is not a number, gets here and fails. That is a schema
	// defect in whatever registered it (an extension tool, most likely), so it is
	// named as one: this runs while cmd wiring boots, never on a request.
	if err := json.Unmarshal(inputSchema, &schema); err != nil {
		//craft:ignore panic-in-domain composition-time registration assertion — fires only while cmd wiring runs, never on a request path
		panic("crmagents: input schema declares an unreadable `minimum`/`maximum`: " + err.Error())
	}
	var bounds []numBound
	for name, prop := range schema.Properties {
		if prop.Type != "integer" && prop.Type != "number" {
			continue
		}
		if prop.Minimum == nil && prop.Maximum == nil {
			continue
		}
		bounds = append(bounds, numBound{name: name, min: prop.Minimum, max: prop.Maximum})
	}
	// Sorted, so a call breaking two bounds is refused in the same words every
	// time rather than in Go's map order.
	sort.Slice(bounds, func(i, j int) bool { return bounds[i].name < bounds[j].name })
	return bounds
}

// requireDeclaredArgs holds a call to every claim its own tools/list entry makes
// about its arguments: the required ones are there, the ids name something, and
// the numbers sit inside the advertised range. Invoke calls THIS rather than the
// three checks in turn, so a claim read off the schema later cannot be wired
// into one of Invoke's two placements and forgotten in the other.
//
// Presence and the ids answer TOGETHER. Each collects faithfully on its own, but
// they split the required arguments between them — the id-shaped ones belong to
// idargs.go — so a call missing one of each was refused for the plain argument,
// and told about the id only after the caller had fixed the first and called
// again. That is the call-per-field waste all three of these files say the
// collection exists to end, reappearing at the seam between them.
//
// The bounds stay separate: an argument that is missing has no range to be wrong
// about, so there is nothing to say about it in the same breath.
func (r *Registry) requireDeclaredArgs(name string, args json.RawMessage) error {
	if err := joinArgRefusals(r.requireDeclaredPresence(name, args), r.requireDeclaredIDs(name, args)); err != nil {
		return err
	}
	return r.requireDeclaredBounds(name, args)
}

// joinArgRefusals answers with both refusals when both fired, and with whichever
// one did otherwise.
//
// A refusal this surface did not build WINS, unjoined. Both checks answer
// *BadArgsError for an argument the caller got wrong; anything else — an
// authority error surfacing from a lookup, a wrapped infrastructure fault — is
// not a sentence to splice into another one, and it must not be dropped in
// favour of one either. Answering the argument refusal while a real failure was
// also in hand would tell a caller to fix its arguments and try again, against
// a server that was going to fail anyway.
func joinArgRefusals(first, second error) error {
	var firstArgs, secondArgs *BadArgsError
	firstIsArgs, secondIsArgs := errors.As(first, &firstArgs), errors.As(second, &secondArgs)
	if first != nil && !firstIsArgs {
		return first
	}
	if second != nil && !secondIsArgs {
		return second
	}
	if !firstIsArgs || !secondIsArgs {
		return cmp.Or(first, second)
	}
	return &BadArgsError{
		Cause:    fmt.Errorf("%w; %w", firstArgs.Cause, secondArgs.Cause),
		Guidance: firstArgs.Guidance,
	}
}

// requireDeclaredBounds holds every supplied value to the range its tool
// advertises for it.
//
// An ABSENT argument is a legal call — an optional bound argument omitted is a
// complete call, and the handler's own default answers it — so only a value that
// is PRESENT is bounded. An explicit null is absent for the same reason: it
// carries no number to place inside or outside a range.
//
// Every violation is collected before answering, for the reason the id check
// gives: reporting them one per round trip is accurate and still wasteful, since
// an agent then spends a call per field to learn what one refusal could have told
// it.
func (r *Registry) requireDeclaredBounds(name string, args json.RawMessage) error {
	r.mu.RLock()
	bounds := r.numArgs[name]
	r.mu.RUnlock()
	if len(bounds) == 0 {
		return nil
	}
	present, isObject := argsAsObject(args)
	if !isObject {
		// Not an object at all, so there are no members to bound. The shape verdict
		// belongs to the steps that own it — the argument split, then the handler's
		// own decode — each of which names what it wanted; a second, vaguer answer
		// to the same question is worse than none.
		return nil
	}
	var refusals []string
	for _, bound := range bounds {
		raw, supplied := present[bound.name]
		if !supplied {
			continue
		}
		var value *float64
		if json.Unmarshal(raw, &value) != nil || value == nil {
			// Not a number at all, or an explicit null. decodeArgs answers the
			// first in the handler's own terms, naming the field and the type it
			// wanted, and treats the second as the absent argument it is.
			continue
		}
		if refusal, broken := bound.violation(*value); broken {
			refusals = append(refusals, refusal)
		}
	}
	if len(refusals) > 0 {
		return &BadArgsError{Cause: errors.New(strings.Join(refusals, "; "))}
	}
	return nil
}

// violation reports whether value falls outside the bound, and how to say so:
// the argument, what it carried, and the bound it broke — everything the caller
// needs to reissue the call, and nothing about how this server is built.
//
// The bound itself is ours, a literal in this package's schemas. The value is
// the caller's, and it is rendered from the NUMBER rather than echoed as
// written, so the refusal cannot carry whatever else was in that position.
func (b numBound) violation(value float64) (string, bool) {
	switch {
	case b.min != nil && value < *b.min:
		return fmt.Sprintf("`%s` is %s, below its declared minimum of %s",
			b.name, formatBound(value), formatBound(*b.min)), true
	case b.max != nil && value > *b.max:
		return fmt.Sprintf("`%s` is %s, above its declared maximum of %s",
			b.name, formatBound(value), formatBound(*b.max)), true
	}
	return "", false
}

// formatBound renders a number the way the schema and the caller wrote it: `50`,
// not `50.000000`. The shortest exact form, and never an unbounded run of digits
// for a value near the float range.
func formatBound(v float64) string { return strconv.FormatFloat(v, 'g', -1, 64) }
