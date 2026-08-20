// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package agents

// The tag verbs. Short on purpose: four tools ride every listing, and the tag
// vocabulary is the simplest thing on this surface.
var applyTagCopy = toolCopy{
	Purpose: "Put an existing tag on a person, company, deal or lead.",
	Limits:  "The tag must exist. Applying twice is refused as a conflict.",
}

var removeTagCopy = toolCopy{
	Purpose: "Take one tag off one record, leaving the word in the vocabulary.",
	Limits:  "Removing one that is not there succeeds. archive_record on a tag retires it for all.",
}
