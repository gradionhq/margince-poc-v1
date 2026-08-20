// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package agents

// The results that answer WHO and WHAT-IT-IS-CALLED rather than what a record
// holds: the acting human, the colleague roster, and the tag vocabulary.
//
// Split out of results.go when that file crossed the 500-line cap. They belong
// together: none of them is a record, all three answer a question a caller asks
// BEFORE it can name a record — whose id goes in owner_id, which colleague
// gets the task, which word to tag with.

import (
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// WhoamiResult is the human a passport acts for. Every field can be empty:
// a system principal acts for nobody, and a person who never chose a language
// has no locale — an empty one is the honest answer, not 'en'.
type WhoamiResult struct {
	ActingUserID ids.UUID `json:"acting_user_id"`
	DisplayName  string   `json:"display_name"`
	Email        string   `json:"email"`
	Locale       string   `json:"locale,omitempty"`
	Timezone     string   `json:"timezone,omitempty"`
}

// ListColleaguesResult is the workspace roster. Empty is a real answer — a
// filter that matches nobody — never an error.
type ListColleaguesResult struct {
	Colleagues []Colleague `json:"colleagues"`
	// Truncated says the roster is longer than one answer. A caller told
	// nothing would read a capped list as the whole roster and report that a
	// colleague does not work here.
	Truncated bool `json:"truncated,omitempty"`
}

// TagAppliedResult reports one tagging. `applied` is false for a removal,
// which is the same shape rather than a second one: a caller that acted on a
// record wants the record back either way.
type TagAppliedResult struct {
	Applied    bool     `json:"applied"`
	TagID      ids.UUID `json:"tag_id"`
	RecordType string   `json:"record_type"`
	RecordID   ids.UUID `json:"record_id"`
}
