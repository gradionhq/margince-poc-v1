// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package agents

// Written copy for the ranked retrieval primitive. See toolcopy.go for what each
// field answers.

var searchContextCopy = toolCopy{
	Purpose: "Find the records most relevant to a description, ranked by meaning as well as by " +
		"wording, each with the excerpt that ranked it.",
	Limits: "Ranked, never exhaustive: records that also match may be absent, and no count of them " +
		"exists. It takes no filters and does not group or total.",
	Instead: "Use query_workspace when the question has conditions, a date bound or a related record " +
		"to reach through, and search_records when you have the exact name or phrase.",
	Retain: "Read `coverage`: `partial_degraded` means `notes` matters, and " +
		"`semantic_ranking_degraded_to_lexical` there means the ranking fell back to word overlap. " +
		"Keep each hit's record_type and id.",
}
