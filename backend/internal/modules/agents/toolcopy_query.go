// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package agents

// Written copy for the governed workspace query. See toolcopy.go for what each
// field answers.

var queryWorkspaceCopy = toolCopy{
	Purpose: "Answer a question that has STRUCTURE — a record type, conditions on its fields, a " +
		"hop to a related record, or a likeness to describe — by sending a plan and reading back " +
		"the records that satisfy it, together with what kind of answer it is.",
	Limits: "Every name in a plan comes from the published vocabulary, and a name outside it is " +
		"refused rather than guessed at: read the margince://schema/query resource first, because " +
		"that document — not this description — is what says which record types, fields, operators " +
		"and relationships this workspace can actually be asked about. A plan takes at most one " +
		"similarity clause and at most one relationship hop. It cannot group, count or total, and " +
		"there is no cursor: an answer that hit its limit says so instead of offering a next page.",
	Instead: "Use search_records when you only have a name or a phrase and no conditions to apply, " +
		"and run_report when the answer wanted is a count, a total or a breakdown rather than the " +
		"records themselves.",
	Retain: "Read `coverage` before you use the rows: `complete_exact` means every record matching " +
		"the plan is here, `ranked_semantic` means these ranked highest and others may match, and " +
		"`partial_degraded` means something in the plan could not be answered as asked — `notes` " +
		"says which. Keep each row's record_type and id for any follow-up call, and its `evidence` " +
		"for the related record that admitted it.",
}
