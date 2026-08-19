// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package agents

// The written guidance for the confirm-first queue.
//
// Four entries, written together because the mistake they prevent is a mistake
// about the SET: a caller that re-proposes what is already waiting, answers an
// item without reading what it holds, or approves its own staged call and stops
// there — believing the approval performed it. Each names the next step.
//
// They are also the tightest entries on this surface, deliberately. The tool
// listing is spent out of every run's window and never elided, so a queue that
// is read once costs a paragraph less than the verbs a run leans on.

// list_approvals — the queue itself.
var listApprovalsCopy = toolCopy{
	Purpose: "The staged actions waiting for a person's decision: what was proposed and what each " +
		"would do. It is where a proposal that is already waiting turns up — a message staged and " +
		"unsent is not one that needs writing again.",
	Limits: "It lists what the person you act for could decide themselves; anything else is absent " +
		"rather than refused. A proposal past its expiry reads as expired and can no longer be " +
		"answered. Each item carries its one-line summary, not the change itself.",
	Instead: "read_approval opens one and shows what it holds; decide_approval answers it.",
	Retain:  "Keep the staged_action_id you mean to act on, the bundle_id when one act staged several, and next_cursor.",
}

// read_approval — one item, in full.
var readApprovalCopy = toolCopy{
	Purpose: "Read one staged action in full: the exact change proposed, the record it acts on, and " +
		"the evidence it was formed on — enough to answer it without opening the app.",
	Limits: "Reading performs nothing. An id the person you act for could not decide answers as not " +
		"found, exactly as an id naming nothing does.",
	Instead: "list_approvals yields the id; decide_approval answers it.",
	Retain:  "Keep the staged_action_id, and the bundle_id if the item names one.",
}

// decide_approval — the answer.
var decideApprovalCopy = toolCopy{
	Purpose: "Answer one staged action for the person asking you: approve it, which lets it happen, " +
		"or reject it, which discards it.",
	Limits: "The verdict is theirs — take an explicit approve or reject rather than deciding what " +
		"they would have wanted. Approving is what makes the change real, including sending a " +
		"message that was only drafted; a rejection cannot be taken back. An item already answered, " +
		"or lapsed, is reported as such and nothing is written.",
	Instead: "read_approval when they have not seen what it holds; decide_approval_bundle for every " +
		"proposal one act staged.",
	Retain: "If the proposal is your OWN refused call, approving does not perform it — re-issue that " +
		"same call with approval_id set.",
}

// decide_approval_bundle — one act's proposals, answered together.
var decideBundleCopy = toolCopy{
	Purpose: "Answer every still-waiting proposal that one act staged together — the overnight run " +
		"that proposed six corrections is six proposals under one bundle_id.",
	Limits: "Each member is answered on its own terms and reported on its own; one already decided, " +
		"or lapsed, is left as it is. Members the person could not decide alone are not decided " +
		"here, and a bundle holding none of theirs reads as not found.",
	Instead: "decide_approval answers a single item; list_approvals is where a bundle_id comes from.",
	Retain:  "Each member carries its own outcome — decided here, already decided, or expired.",
}
