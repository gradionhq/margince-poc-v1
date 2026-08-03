// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package provenance

import "strings"

// ReservedSourceSystemPrefix namespaces a source_system only an IMPORT
// may write.
//
// The lead and activity stores key their idempotent replay on
// (source_system, source_id), and both columns arrive from the client on
// their create wire. Without a reserved namespace a caller could
// pre-plant a row under a guessed incumbent record id and have the store
// hand it back to a later import as already existing — silently
// suppressing the real record, and (because activities resolve their
// links through the same identity) attaching the incumbent's timeline to
// the planted row. The importer writes inside this namespace; every
// client-facing create path refuses it.
const ReservedSourceSystemPrefix = "mirror:"

// ReservedSourceSystem reports whether a client-supplied source system
// trespasses on the importer's namespace.
func ReservedSourceSystem(sourceSystem string) bool {
	return strings.HasPrefix(sourceSystem, ReservedSourceSystemPrefix)
}

// ReservedError refuses a client write into the importer's namespace,
// naming the field it arrived on. One type rather than one per module:
// the rule is a single invariant — no client-facing path may write this
// namespace — and three copies of it would be three places for the next
// provenance field to be forgotten.
type ReservedError struct{ Field, Value string }

func (e *ReservedError) Error() string {
	return e.Field + " " + e.Value + " is reserved for imports; omit it or use a value outside the " + ReservedSourceSystemPrefix + " namespace"
}

// FieldFault states the refusal as caller-fixable, which is how it
// reaches every surface — the HTTP mapper and the MCP tool surface both
// read this rather than each module restating it.
func (e *ReservedError) FieldFault() (field, code, message string) {
	return e.Field, "reserved_source_system", e.Error()
}

// Refuse guards ONE provenance field on a create wire. The flip stamps
// its own writes inside this namespace and reads them back to recognize
// records a crashed attempt landed, which is safe only while nothing
// else can spell the prefix.
func Refuse(field, value string) error {
	if ReservedSourceSystem(value) {
		return &ReservedError{Field: field, Value: value}
	}
	return nil
}
