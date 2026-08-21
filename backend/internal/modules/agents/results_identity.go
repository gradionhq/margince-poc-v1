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
	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
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

// ListTagsResult is the workspace's tag vocabulary. Empty is a real answer —
// a workspace that has coined no words yet — never an error.
//
// `truncated` is not optional decoration. The store caps its read, and the
// caller's reason for reading is to find out whether a word already exists —
// so a capped list presented as the whole vocabulary answers "no such tag" for
// every word past the cap, and the caller coins the duplicate that reading the
// vocabulary was meant to prevent.
type ListTagsResult struct {
	Tags      []Tag `json:"tags"`
	Truncated bool  `json:"truncated,omitempty"`
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

// ImportRunResult is one migrate-in run: what it is importing, where it has
// got to, and why it stopped if it did.
//
// `checkpoint` is here rather than hidden because a failed run is resumable
// from it, and a caller told only "failed" would report a dead end where the
// product has a resume.
type ImportRunResult struct {
	RunID      string `json:"run_id"`
	Object     string `json:"object"`
	State      string `json:"state"`
	Checkpoint int    `json:"checkpoint"`
	Error      string `json:"error,omitempty"`
}

// ImportPreviewResult is what a dry run produced, plus what it decided about
// the file's columns.
//
// `unmapped` is the field a caller most needs and would least think to ask
// for: a column nothing reads is dropped silently otherwise, and a mistyped
// field name looks exactly like a clean import until somebody notices the data
// is not there.
type ImportPreviewResult struct {
	Run      ImportRunResult   `json:"run"`
	Mapping  map[string]string `json:"mapping"`
	Columns  []string          `json:"columns"`
	Unmapped []string          `json:"unmapped,omitempty"`
}

// ImportReportResult carries the run's own report unchanged — one shape before
// and after the commit, so a person comparing what will happen with what did
// compares like with like.
type ImportReportResult struct {
	Report crmcontracts.ImportRunReport `json:"report"`
}
