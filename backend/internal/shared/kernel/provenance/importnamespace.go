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
