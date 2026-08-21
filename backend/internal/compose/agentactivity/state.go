// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package agentactivity

// State is the wire vocabulary. It is deliberately NOT the column vocabulary:
// runner_job says "done" where agent_run says "completed", and a reader needs one
// word for one idea.
type State string

const (
	StateQueued           State = "queued"
	StateRunning          State = "running"
	StateAwaitingApproval State = "awaiting_approval"
	StateDone             State = "done"
	StateDegraded         State = "degraded"
	StateFailed           State = "failed"
)

// runStates maps agent_run.status. Written out rather than derived: the CHECK
// constraint's vocabulary and the reader's are different vocabularies, and a
// mapping that fell back to the raw column would put a database word on screen.
var runStates = map[string]State{
	"running":           StateRunning,
	"awaiting_approval": StateAwaitingApproval,
	"completed":         StateDone,
	"degraded":          StateDegraded,
	"failed":            StateFailed,
}

// jobStates maps runner_job.status, and only "queued" is here. Once a job is
// claimed it has an agent_run row, and that row is the authority; surfacing both
// would report one trigger occurrence twice.
var jobStates = map[string]State{"queued": StateQueued}

// RunState is the wire state for one agent_run row. The second return is false
// for a status this surface does not report, and the caller drops the row rather
// than inventing a word for it.
func RunState(status string) (State, bool) {
	s, ok := runStates[status]
	return s, ok
}

// JobState is the wire state for one runner_job row, on the same contract.
func JobState(status string) (State, bool) {
	s, ok := jobStates[status]
	return s, ok
}
