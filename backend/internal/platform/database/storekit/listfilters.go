// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package storekit

// Binding a caller's `name=value` filters onto a store's list input.
//
// A record list is narrowed by a handful of typed fields, and every store
// spells the same three operand kinds — an id, a closed word, a flag. Spelling
// the binding once here is what keeps the two halves of a filter inseparable:
// the NAME a surface may advertise and the FIELD that name narrows come out of
// one declaration, so a filter cannot be published by one half and dropped by
// the other. A dropped filter answers a wider question than the caller asked
// while looking exactly like an answer, which is the failure worth designing
// out rather than testing for.
//
// It is the binding, not the vocabulary: which filters exist is the contract's
// to say (each list operation's own declared parameters), and each module
// declares only how its store takes them.

import (
	"fmt"
	"maps"
	"slices"
	"strconv"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// FilterBinding narrows one list input by one caller-supplied value.
type FilterBinding[I any] func(in *I, value string) error

// FilterSet is one record type's whole enumerable vocabulary, keyed by the
// name a caller asks for.
type FilterSet[I any] map[string]FilterBinding[I]

// Names is the vocabulary a surface may publish, sorted so it is byte-stable
// across processes — a client that caches a tool schema must not read a map
// reshuffle as a contract change.
func (s FilterSet[I]) Names() []string {
	return slices.Sorted(maps.Keys(s))
}

// Apply folds a caller's filters into the list input.
//
// An unknown name is REFUSED, never ignored. Ignoring it would run the
// enumeration unnarrowed and answer a question nobody asked, in a shape
// indistinguishable from the right answer.
func (s FilterSet[I]) Apply(in *I, filters map[string]string) error {
	for _, name := range slices.Sorted(maps.Keys(filters)) {
		bind, ok := s[name]
		if !ok {
			return fmt.Errorf("storekit: %q is not a filter this record type can be listed by", name)
		}
		if err := bind(in, filters[name]); err != nil {
			// The FILTER is named here and the SHAPE by the binding, so neither
			// half has to know the other's — and the operand itself is named by
			// nobody, since it is caller text on its way back to the caller.
			return fmt.Errorf("storekit: the %s filter %w", name, err)
		}
	}
	return nil
}

// FilterWord binds a closed-vocabulary or free-text operand. The VALUE is not
// validated here: a word outside the contract's enum reaches the store as an
// equality match that selects nothing, which is the honest answer to a filter
// nothing matches, and the surface that published the enum is the one that
// refuses a word outside it.
func FilterWord[I any](set func(*I, *string)) FilterBinding[I] {
	return func(in *I, value string) error {
		set(in, &value)
		return nil
	}
}

// FilterID binds a reference operand, parsed as the kind the field holds so a
// person id cannot be handed to a pipeline filter.
func FilterID[K ids.EntityKind, I any](set func(*I, *ids.ID[K])) FilterBinding[I] {
	return func(in *I, value string) error {
		id, err := ids.ParseAs[K](value)
		if err != nil {
			return operandShape("a uuid, in the 8-4-4-4-12 hex form")
		}
		set(in, &id)
		return nil
	}
}

// FilterFlag binds a boolean operand, spelled as JSON spells it.
func FilterFlag[I any](set func(*I, *bool)) FilterBinding[I] {
	return func(in *I, value string) error {
		flag, err := strconv.ParseBool(value)
		if err != nil {
			return operandShape("true or false")
		}
		set(in, &flag)
		return nil
	}
}

// operandShape says what a filter's operand must look like, and does NOT carry
// the parse error.
//
// That is deliberate rather than a swallowed cause: every parse failure here
// says one thing — this value is not of that shape — and the only detail the
// cause adds is the value itself. This message travels back to a caller who may
// be an agent, so it lands in that run's later prompts, and echoing an operand
// of the caller's choosing there is the unbounded write the surface's other
// echoes are already bounded against. What a caller needs in order to fix the
// call is the field and the shape, and Apply supplies the field.
func operandShape(takes string) error {
	return fmt.Errorf("takes %s", takes)
}
