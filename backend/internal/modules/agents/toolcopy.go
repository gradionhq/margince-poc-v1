// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package agents

// The written selection guidance every tool on this surface carries, and the
// four questions each entry answers.
//
// WHY IT IS PROSE AND NOT DERIVED. Everything a machine can answer about a tool
// is already on its spec — which scope it spends, which tier it runs at, whether
// it leaves the workspace, which contract operation it maps to. A model choosing
// between catch_me_up_on, prep_for_meeting, read_record and run_report is not
// short of any of that; it is short of what each one produces and which of the
// four its goal is asking for. Only a person who knows all four can write that
// down, so it is written down.
//
// WHY FOUR FIELDS AND ONE STRING. The four are the questions a wrong call
// usually failed to ask, kept apart so an author cannot answer three of them and
// believe the entry is finished. They render into the ONE description the wire
// carries: MCP has a single description member, so a seam field per question
// would be four fields whose only consumer joins them.
//
// WHAT DOES NOT BELONG HERE. The autonomy tier and the passport scope: each
// surface appends its own reading of those (dispatch.toolList states them for an
// MCP client; the runner's system prompt states the staging rule once for the
// whole surface), and a copy that also spelled them would contradict one of them
// the first time a tier moved. The crm.yaml operation family: it is developer
// documentation, and a model has no use for the name of an endpoint it cannot
// call.
//
// EACH ENTRY IS REFERENCED EXACTLY ONCE, by the Spec() of the tool it belongs
// to. That is not style — an entry nothing references is an unused package-level
// identifier, which the unused check fails, so a tool renamed or withdrawn
// cannot leave its copy behind as documentation of a surface that is gone.

import "strings"

// toolCopy is one tool's written selection guidance.
//
// Purpose is required — a tool with nothing to say about its own outcome has no
// description worth the name. The other three are written when they are true:
// a tool with no confusable neighbour, no prerequisite, or no identifier worth
// keeping should say so by omission rather than by filler.
type toolCopy struct {
	// Purpose is the outcome and the intent that reaches for it, in the
	// caller's words rather than the schema's.
	Purpose string
	// Limits is what the tool does NOT do and what has to be true before it
	// can — the sentence that stops a caller reading Purpose more widely than
	// it is meant.
	Limits string
	// Instead names the neighbour that answers a nearby goal better, and the
	// goal it answers. The pairs that need it are the ones A1 measured: two
	// tools whose names read alike and whose results do not.
	Instead string
	// Retain is what the caller has to carry out of the result for its next
	// call — an id, a version, a cursor. A follow-up that guesses one of these
	// is the failure this line prevents.
	Retain string
}

// render joins the entry into the one description a client is served. The parts
// are already sentences, so they are joined by a space in a fixed order: the
// outcome first, because a model reading only the first line of a thirty-tool
// listing should still be reading the answer to "what is this for".
func (c toolCopy) render() string {
	parts := make([]string, 0, 4)
	for _, part := range []string{c.Purpose, c.Limits, c.Instead, c.Retain} {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			parts = append(parts, trimmed)
		}
	}
	return strings.Join(parts, " ")
}
