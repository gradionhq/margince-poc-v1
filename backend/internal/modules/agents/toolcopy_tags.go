// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package agents

// The tag verbs. Short on purpose: four tools ride every listing, and the tag
// vocabulary is the simplest thing on this surface.
var applyTagCopy = toolCopy{
	Purpose: "Tag a person, company, deal or lead — by tag_id, or by tag_name, which reuses the " +
		"workspace's word or adds it.",
	Limits: "Applying the same tag twice is refused as a conflict. A name matches case-insensitively; " +
		"a near-miss makes a NEW word, so prefer a tag_id you already hold.",
}

var removeTagCopy = toolCopy{
	Purpose: "Take one tag off one record — by tag_id or tag_name — leaving the word itself.",
	Limits:  "Removing one that is not there succeeds. archive_record on a tag retires it for all.",
}
