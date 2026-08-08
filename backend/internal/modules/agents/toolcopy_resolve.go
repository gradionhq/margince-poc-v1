// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package agents

// Written copy for identity resolution. See toolcopy.go for what each field
// answers.

var resolveEntitiesCopy = toolCopy{
	Purpose: "Find out whether the people and companies named in something you are holding already " +
		"exist here, matched on addresses, phone numbers and company domains rather than on text.",
	Limits: "It reads only — nothing is created, changed or merged — and it answers person and " +
		"organization, never leads. A near match comes back `ambiguous` however close it is.",
	Instead: "Use search_records to find a record you know exists, and merge_records once a person " +
		"has decided that two records are one.",
	Retain: "Call this BEFORE creating a person or company from anything you did not type. Act on " +
		"`matched`; on `ambiguous` ask which is meant; on `unresolved` creating is safe.",
}
