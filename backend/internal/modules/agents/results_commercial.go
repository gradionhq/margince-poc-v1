// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package agents

// The wire shapes the commercial reads answer with.
//
// They sit here rather than in results.go because that file is at the size
// this tree caps a file at, and a shape is easier to find beside the tools
// that answer with it than at the end of a list of forty. Every rule
// results.go states still applies: a list member is never null, an absent
// value is absent rather than zero, and a field a reader must not guess at
// carries the evidence it was read from.

import (
	"time"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// CommitmentItem is one open promise as review_commitments reports it.
type CommitmentItem struct {
	TaskID  ids.UUID `json:"task_id"`
	Subject string   `json:"subject"`
	// DueAt is absent for a promise nobody dated, which is a different state
	// from one that is late — see State.
	DueAt *time.Time `json:"due_at,omitempty"`
	// State is undated | overdue | upcoming, judged against the answer's own
	// as_of instant. There is deliberately no "due today": see the note on the
	// vocabulary in tools_commitments.go.
	State string `json:"state"`
	// DaysOverdue is present only for an overdue promise, and zero is a real
	// value there — hours late is late by no whole days.
	DaysOverdue *int `json:"days_overdue,omitempty"`
	// AssigneeID and AssigneeName are absent together for a promise nobody
	// owns, which is the state this answer exists to make visible.
	AssigneeID   *ids.UUID         `json:"assignee_id,omitempty"`
	AssigneeName string            `json:"assignee_name,omitempty"`
	About        []CommitmentAbout `json:"about"`
}

// ReviewCommitmentsResult is what review_commitments answers: the open
// promises, oldest first, and the instant every state on them was judged
// against.
type ReviewCommitmentsResult struct {
	AsOf        time.Time        `json:"as_of"`
	Commitments []CommitmentItem `json:"commitments"`
}

// HandoffGap is one thing the receiving side would have to go and ask for,
// named by the field it was read off — the same evidence discipline
// whats_slipping_this_week applies to a risk claim. A gap nobody can point at
// a field for is not reported.
type HandoffGap struct {
	Code    string `json:"code"`
	Source  string `json:"source"`
	Message string `json:"message"`
}

// HandoffDeal is one deal rolled up to the project being handed over.
type HandoffDeal struct {
	DealID ids.UUID `json:"deal_id"`
	Name   string   `json:"name"`
	Status string   `json:"status"`
	// AmountMinor and Currency are absent for a deal carrying no amount,
	// which is a real state: a deal can be worked, and won, before it is
	// priced.
	AmountMinor *int64  `json:"amount_minor,omitempty"`
	Currency    *string `json:"currency,omitempty"`
}

// HandoffStakeholder is one seat on the account side of the work: who holds
// it, what they are called, and what their part in it is.
//
// The NAME is here because the tool's whole promise on this list is "who to
// call at the client", and a UUID restates that question rather than answering
// it. Every other nested list in this answer names its records; the seat was
// the one that did not.
//
// It is absent for a person the caller may not read — the seat itself survived
// the edge's own visibility rule, so this is the narrow case of a name read
// that came back empty, and a reader shows the id rather than a blank.
type HandoffStakeholder struct {
	PersonID ids.UUID `json:"person_id"`
	Name     string   `json:"name,omitempty"`
	// Role is absent for a seat nobody titled. It is the field the receiving
	// side reads first, so an empty one is a gap rather than a blank.
	Role string `json:"role,omitempty"`
}

// PreparedHandoff is what prepare_handoff answers: what the delivery side is
// being given, and what it is not.
type PreparedHandoff struct {
	ProjectID   ids.UUID `json:"project_id"`
	Name        string   `json:"name"`
	Key         string   `json:"key,omitempty"`
	Phase       string   `json:"phase"`
	Description string   `json:"description,omitempty"`
	// OrganizationID is the account the work is for — always present, since a
	// project cannot exist without one.
	OrganizationID ids.UUID `json:"organization_id"`
	// OwnerID and OwnerName are absent together for a project nobody owns,
	// which is reported as a gap rather than left to be noticed. The name is
	// here for the reason HandoffStakeholder's is: "who is receiving this
	// work" answered as a UUID restates the question.
	OwnerID       *ids.UUID            `json:"owner_id,omitempty"`
	OwnerName     string               `json:"owner_name,omitempty"`
	StartedAt     *time.Time           `json:"started_at,omitempty"`
	TargetEndDate *time.Time           `json:"target_end_date,omitempty"`
	Deals         []HandoffDeal        `json:"deals"`
	Stakeholders  []HandoffStakeholder `json:"stakeholders"`
	// OpenCommitments are the promises already outstanding on this work, in
	// the same shape and judged the same way review_commitments judges them —
	// one derivation, so a promise cannot read as late in one answer and
	// upcoming in the other.
	AsOf            time.Time        `json:"as_of"`
	OpenCommitments []CommitmentItem `json:"open_commitments"`
	// Gaps is what the receiving side would otherwise discover by asking. An
	// empty list means nothing was found missing, not that nothing was
	// checked.
	Gaps []HandoffGap `json:"gaps"`
}
