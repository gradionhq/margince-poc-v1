// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package ai

// Who reports a task's work to the AI-activity projection.
//
// Every AI task this build can run must reach ai_task_run, because the rail's
// whole claim is that it says what the AI is doing for one person — and a task
// nothing reports is AI work the product performed and then denied. Seventeen
// of nineteen shipped tasks reported nothing before this registry existed, and
// no gate could see it: the parity checks in place compared two already-wired
// producers against each other, which is a question that cannot have an
// absentee for an answer.
//
// Two kinds of reporter, and the difference is what each can honestly say:
//
//   - The ROUTER reports by default. It is the one place every model call
//     passes through, so a task added next year is wired before its author has
//     thought about the rail. It learns of a call only once the call is over,
//     so a router-owned occurrence is settled the moment it appears.
//   - A CARRIER reports instead, for work that owns a durable row. Only a
//     carrier can say queued and running, and only a carrier declares the lease
//     that lets the read call a dead attempt stalled. Where one exists it is
//     the better reporter, and the router stays silent so the two never write
//     one occurrence between them.
//
// This is not a list kept beside the code. The router calls RailOwner to decide
// whether the call it just finished is its to announce, so an unanswered task
// is one the router refuses to guess about rather than one it quietly reports
// twice.
const (
	// SourceRouter is the ai_task_run.source of an occurrence the router
	// announces on a task's behalf.
	SourceRouter = "ai_router"

	// SourceNoOccurrence answers for work that is a STEP inside somebody
	// else's occurrence rather than an occurrence of its own. It is not an
	// exemption from reporting — the work is reported, under the unit of work
	// it serves — and every use of it owes a reason in railNoOccurrenceReasons.
	SourceNoOccurrence = "none"

	// The carrier sources. Spelled as literals rather than imported: a module
	// never imports a sibling, and these are wire values the projection stores,
	// not Go identity. A root fitness test holds each one to the constant the
	// carrier actually emits, which is the check that makes the literal safe.
	sourceAgentRunner          = "agent_runner"
	sourceAttachmentExtraction = "attachment_extraction"
)

// railOwners answers who reports each task. TOTAL over the contract's task
// table, held there by a root fitness test — a task the generator adds and
// nobody answers fails the build.
//
// A carrier entry is a deliberate silencing of the router: it says this task's
// occurrence is announced somewhere that knows more about it than the router
// does. Every other task is the router's, including the three still `planned`
// — those run no calls today, so the entry costs nothing and is already correct
// on the day somebody builds one.
var railOwners = map[Task]string{
	TaskAgentLoop:       sourceAgentRunner,
	TaskDocumentExtract: sourceAttachmentExtraction,

	TaskEmbeddings: SourceNoOccurrence,

	TaskBriefRanking:               SourceRouter,
	TaskCaptureClassify:            SourceRouter,
	TaskCaptureCounterpartyVerdict: SourceRouter,
	TaskCertJudge:                  SourceRouter,
	TaskColdStart:                  SourceRouter,
	TaskDealHealth:                 SourceRouter,
	TaskDraftReply:                 SourceRouter,
	TaskEnrich:                     SourceRouter,
	TaskGrowthFit:                  SourceRouter,
	TaskNlSearch:                   SourceRouter,
	TaskOfferDraft:                 SourceRouter,
	TaskRateExtract:                SourceRouter,
	TaskSignalExtract:              SourceRouter,
	TaskSiteExtract:                SourceRouter,
	TaskSiteFactExtract:            SourceRouter,
	TaskSiteTriage:                 SourceRouter,
	TaskSummarize:                  SourceRouter,
	TaskTranscript:                 SourceRouter,
	TaskTranscriptPropose:          SourceRouter,
	TaskVoiceBuild:                 SourceRouter,
}

// railNoOccurrenceReasons says why a task is a step rather than an occurrence.
//
// Required, and checked: "reported by nobody" is one keystroke from the silence
// this registry exists to end, so it costs a sentence somebody has to be able
// to defend. A reason that is really an editorial preference ("no rep wants to
// see it") belongs in the CLIENT, which decides what to draw — this map is only
// for work that has no unit of its own to be an occurrence of.
var railNoOccurrenceReasons = map[Task]string{
	TaskEmbeddings: "an embedding is a step inside another piece of work, never one of its own: " +
		"every call happens in service of a search, an enrich or a reindex, and that is the " +
		"occurrence. Reporting it separately would report one piece of work twice at two grains — " +
		"and the reindex pass, which mints no correlation id at all, would report one row per vector.",
}

// NoOccurrenceReason is why this task reports no occurrence of its own, or "".
func NoOccurrenceReason(t Task) string { return railNoOccurrenceReasons[t] }

// RailOwner returns the ai_task_run.source that reports this task, or "" for a
// task nobody has answered for.
//
// An empty answer is the router's instruction to stay silent. That is the safe
// direction: a task with no declared reporter is one whose grain and
// attribution nobody has thought about, and inventing an occurrence for it
// would put a row on somebody's rail that no gate has ever read.
func RailOwner(t Task) string { return railOwners[t] }

// RouterReports says whether the router is the one to announce this task.
func RouterReports(t Task) bool { return railOwners[t] == SourceRouter }

// RailOwners returns the registry as the gate reads it: task name to owning
// source. Copied, so a caller cannot edit the map the router routes on.
func RailOwners() map[string]string {
	out := make(map[string]string, len(railOwners))
	for task, source := range railOwners {
		out[string(task)] = source
	}
	return out
}
