// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package agents

// The identity every write already carries and nothing published.
var whoamiCopy = toolCopy{
	Purpose: "Name the human this passport acts for: their id, display name, email and language.",
	Limits:  "It reads only, and answers this call's acting user — not a directory.",
	Retain: "acting_user_id is what owner_id and assignee_id take for \"me\". Write stored prose " +
		"in locale when it is set.",
}
