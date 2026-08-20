// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package agents

// The distinction the surface was missing: a colleague is not a contact.
var listColleaguesCopy = toolCopy{
	Purpose: "List the people who work HERE — colleagues holding a seat, not the contacts stored " +
		"as person records.",
	Limits:  "Reads only, and answers seats: no permissions, and an archived one is absent.",
	Instead: "search_records/person finds a CUSTOMER contact; this finds a colleague.",
	Retain: "user_id is what assignee_id and owner_id take. Check active before assigning, and " +
		"never assign to an is_agent seat.",
}
