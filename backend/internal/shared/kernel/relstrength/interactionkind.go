// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package relstrength

import "strings"

// interactionKinds is the closed set of activity kinds that represent a real
// exchange between two people. It is unexported because a caller that could
// append to it would silently change both the scoring and the SQL below. It
// lives here because four places need to agree on it: live capture stamping, hand-logged stamping, the historical
// backfill's SQL, and any future producer of participant rows.
//
// A task is intent and a note is a record of thinking; neither means two people
// spoke, and counting them would let a rep's own to-do list score as a
// relationship. When they disagreed, a captured note became an interaction while
// an identical hand-logged one did not — the same conversation scoring
// differently depending on how it arrived.
var interactionKinds = []string{"email", "call", "meeting"}

// IsInteractionKind answers whether an activity of this kind means two people
// spoke.
func IsInteractionKind(kind string) bool {
	for _, k := range interactionKinds {
		if k == kind {
			return true
		}
	}
	return false
}

// InteractionKindSQLList renders the set as a SQL IN list, so a query filters on
// the same set the Go paths do instead of restating it as a literal that can
// drift. The values are compile-time constants of this package, never input.
func InteractionKindSQLList() string {
	return "'" + strings.Join(interactionKinds, "','") + "'"
}
