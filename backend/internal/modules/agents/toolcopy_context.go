// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package agents

// Written copy for the ranked retrieval primitive. See toolcopy.go for what each
// field answers.

var searchContextCopy = toolCopy{
	Purpose: "Find the records most relevant to a description, ranked by meaning as well as by " +
		"wording, and read back each one together with the excerpt that made it rank.",
	Limits: "This is a RANKED answer, never an exhaustive one: it returns the top of an ordering, " +
		"so records that also match may not be here and a count of them is not available. It takes " +
		"no filters — no owner, no date bound, no relationship — and it does not group, count or " +
		"total.",
	Instead: "Use query_workspace when the question has conditions to apply, a related record to " +
		"reach through, or a date bound, and search_records when you already have the name or exact " +
		"phrase that appears on the record.",
	Retain: "Read `coverage` before you use the hits: `ranked_semantic` is the normal answer and " +
		"`partial_degraded` means `notes` has something you need — in particular, " +
		"`semantic_ranking_degraded_to_lexical` means the ranking fell back to word overlap, so a " +
		"description sharing no words with a record could not rank it. Keep each hit's record_type " +
		"and id for any follow-up call.",
}
