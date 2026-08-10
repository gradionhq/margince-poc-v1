// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package httperr

// How many records one response hands over, and the seam that lets the surface
// bounding an agent's reads charge for them.

import "reflect"

// ServedMeter is implemented by a ResponseWriter its owner has wrapped to count
// what leaves this door. WriteJSON reports every body it is about to write, so
// the count is taken at the ONE place a record becomes a REST response instead
// of in the ~290 handlers that would each have to remember.
//
// This package holds no request and knows nothing about quotas, so a meter that
// cannot record its charge answers the request ITSELF and reports proceed=false;
// WriteJSON then writes nothing more. That keeps the decision about what an
// uncountable answer costs with the door that has the context to make it.
type ServedMeter interface {
	NoteServed(n int) (proceed bool)
}

// recordsIn counts the records a response body hands over.
//
// It reads the CONTRACT's own list envelope rather than a list of response type
// names: every generated list response is a struct carrying a `Data` slice, so
// the shape answers the question and a list added tomorrow is counted without an
// edit here. TestEveryListResponseCarriesADataSlice holds that shape, so a list
// this rule would silently count as one fails the build instead.
//
// Anything else is one thing handed over, and that deliberately counts a
// settings or status body as a record: it is still an answer this credential was
// given, and separating "real" records from the rest is the maintained list
// again — with the tool added next missing from it.
//
//craft:ignore naked-any it counts WriteJSON's own body argument, and takes the same type that seam does
func recordsIn(body any) int {
	value := reflect.ValueOf(body)
	for value.Kind() == reflect.Pointer || value.Kind() == reflect.Interface {
		if value.IsNil() {
			return 0
		}
		value = value.Elem()
	}
	switch value.Kind() {
	case reflect.Invalid:
		// A nil body writes JSON "null" and hands over nothing.
		return 0
	case reflect.Slice:
		return value.Len()
	case reflect.Struct:
		if data := value.FieldByName("Data"); data.IsValid() && data.Kind() == reflect.Slice {
			return data.Len()
		}
		return 1
	default:
		return 1
	}
}
