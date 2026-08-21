// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package agents

// The tag verbs. Short on purpose: four tools ride every listing, and the tag
// vocabulary is the simplest thing on this surface.
var listTagsCopy = toolCopy{
	Purpose: "The workspace's words for grouping records, with the tag_id apply_tag takes.",
	Limits:  "Archived words come only on request and cannot be applied.",
}

var applyTagCopy = toolCopy{
	Purpose: "Tag a person, company, deal or lead by tag_id, or by tag_name, which reuses the " +
		"workspace's word or coins it.",
	Limits: "Prefer a tag_id from list_tags: a name matches case-insensitively, and a near-miss " +
		"makes a NEW word. The same tag twice is a conflict.",
}

var removeTagCopy = toolCopy{
	Purpose: "Take one tag off one record — by tag_id or tag_name — leaving the word itself.",
	Limits:  "Removing one that is not there succeeds. archive_record on a tag retires it for all.",
}
