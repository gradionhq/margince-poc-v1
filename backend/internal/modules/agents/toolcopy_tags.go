// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package agents

// The tag verbs. Short on purpose: four tools ride every listing.
var listTagsCopy = toolCopy{
	Purpose: "List the workspace's tags — the shared vocabulary records are grouped by.",
	Limits:  "Reads only; archived tags are absent unless asked for.",
	Retain:  "tag_id is what apply_tag and remove_tag take.",
}

var createTagCopy = toolCopy{
	Purpose: "Add a word to the workspace's tag vocabulary.",
	Limits:  "It creates the TAG, not a tagging. apply_tag is what puts it on a record.",
	Instead: "list_tags first — a tag that already exists should be reused, not duplicated.",
}

var applyTagCopy = toolCopy{
	Purpose: "Put an existing tag on a person, company, deal or lead.",
	Limits:  "The tag must exist; create_tag makes one. Applying twice is refused as a conflict.",
	Retain:  "The record is unchanged otherwise — a tag is a label, not a field.",
}

var removeTagCopy = toolCopy{
	Purpose: "Take one tag off one record, leaving the tag itself in the vocabulary.",
	Limits: "Removing a tag that is not on the record succeeds. archive_record on a tag is a " +
		"different act: it retires the word for everybody.",
}
