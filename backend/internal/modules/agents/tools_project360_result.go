// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package agents

// The read_project_360 answer, and the carry from the page's contract shape.
// Every list member is a pointer that is ABSENT when the section was
// withheld — `sections_omitted` names it — and a list that is never null
// when present, for the reason every list-shaped answer on this surface
// gives: null reads as "unknown" where an empty array says "none".

import (
	"time"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// Project360Result is the assembled project page as the surface serves it.
type Project360Result struct {
	AsOf            time.Time               `json:"as_of"`
	Project         Project360Project       `json:"project"`
	SectionsOmitted []string                `json:"sections_omitted"`
	Organization    *Project360Organization `json:"organization,omitempty"`
	PhaseHistory    *Project360PhaseHistory `json:"phase_history,omitempty"`
	Deals           *Project360Deals        `json:"deals,omitempty"`
	Stakeholders    *Project360Stakeholders `json:"stakeholders,omitempty"`
	Contracts       *Project360Contracts    `json:"contracts,omitempty"`
	Documents       *Project360Documents    `json:"documents,omitempty"`
	Commitments     *Project360Commitments  `json:"commitments,omitempty"`
	Activities      *Project360Activities   `json:"activities,omitempty"`
	// Filing is the page's coverage section under a name of its own:
	// "coverage" is an envelope word this surface reserves for an enumerated
	// vocabulary (TestNoResultSchemaCarriesADeferredEnvelopeField), and these
	// two counts are not that.
	Filing  *Project360Filing  `json:"filing,omitempty"`
	Rollups *Project360Rollups `json:"rollups,omitempty"`
}

// Project360Project is the anchor row's fields an agent acts on.
type Project360Project struct {
	ProjectID      ids.UUID   `json:"project_id"`
	Name           string     `json:"name"`
	Key            string     `json:"key"`
	Phase          string     `json:"phase"`
	ClosedReason   string     `json:"closed_reason"`
	Description    string     `json:"description"`
	OrganizationID *ids.UUID  `json:"organization_id,omitempty"`
	OwnerID        *ids.UUID  `json:"owner_id,omitempty"`
	StartedAt      *time.Time `json:"started_at,omitempty"`
	TargetEndDate  *time.Time `json:"target_end_date,omitempty"`
	EndedAt        *time.Time `json:"ended_at,omitempty"`
}

// Project360Organization names the company the project is for.
type Project360Organization struct {
	OrganizationID ids.UUID `json:"organization_id"`
	Name           string   `json:"name"`
}

// Project360PhaseHistory is every transition the project made, oldest
// first, and the time spent in each phase so far.
type Project360PhaseHistory struct {
	Transitions []Project360Transition    `json:"transitions"`
	Durations   []Project360PhaseDuration `json:"phase_durations"`
}

// Project360Transition is one phase move; from_phase is empty on the birth
// row, and changed_by_name is empty for a principal the user table does not
// hold.
type Project360Transition struct {
	FromPhase     string    `json:"from_phase"`
	ToPhase       string    `json:"to_phase"`
	Reason        string    `json:"reason"`
	ChangedAt     time.Time `json:"changed_at"`
	ChangedBy     string    `json:"changed_by"`
	ChangedByName string    `json:"changed_by_name"`
}

// Project360PhaseDuration is the seconds a project has spent in one phase,
// summed over every visit; current marks the phase it is in at as_of.
type Project360PhaseDuration struct {
	Phase   string `json:"phase"`
	Seconds int64  `json:"seconds"`
	Current bool   `json:"current"`
}

// Project360Deals is the deals rolled up to the project, every status.
type Project360Deals struct {
	Items     []HandoffDeal `json:"items"`
	Truncated bool          `json:"truncated"`
}

// Project360Stakeholders is the people seated on the project.
type Project360Stakeholders struct {
	Items     []HandoffStakeholder `json:"items"`
	Truncated bool                 `json:"truncated"`
}

// Project360Contracts is the agreements attached to the project.
type Project360Contracts struct {
	Items     []Project360Contract `json:"items"`
	Truncated bool                 `json:"truncated"`
}

// Project360Contract is one agreement as the page carries it.
type Project360Contract struct {
	ContractID     ids.UUID   `json:"contract_id"`
	Title          string     `json:"title"`
	ContractNumber string     `json:"contract_number"`
	Status         string     `json:"status"`
	UnderContract  bool       `json:"under_contract"`
	ValueMinor     *int64     `json:"value_minor,omitempty"`
	Currency       string     `json:"currency"`
	StartsOn       *time.Time `json:"starts_on,omitempty"`
	EndsOn         *time.Time `json:"ends_on,omitempty"`
}

// Project360Documents is the files attached to the project itself.
type Project360Documents struct {
	Items     []Project360Document `json:"items"`
	Truncated bool                 `json:"truncated"`
}

// Project360Document is one attached file's metadata — never its bytes.
type Project360Document struct {
	AttachmentID ids.UUID  `json:"attachment_id"`
	Filename     string    `json:"filename"`
	Title        string    `json:"title"`
	Category     string    `json:"category"`
	DocState     string    `json:"doc_state"`
	CreatedAt    time.Time `json:"created_at"`
}

// Project360Commitments is the open tasks filed under the project, judged
// against as_of the way review_commitments judges them.
type Project360Commitments struct {
	Items     []CommitmentItem `json:"items"`
	Truncated bool             `json:"truncated"`
}

// Project360Activities is the first page of the project's timeline, newest
// first.
type Project360Activities struct {
	Items     []Project360Activity `json:"items"`
	Truncated bool                 `json:"truncated"`
}

// Project360Activity is one timeline row.
type Project360Activity struct {
	ActivityID ids.UUID  `json:"activity_id"`
	Kind       string    `json:"kind"`
	Subject    string    `json:"subject"`
	Direction  string    `json:"direction"`
	OccurredAt time.Time `json:"occurred_at"`
}

// Project360Filing is how well the project's correspondence is filed:
// attributed is every activity on the project, unattributed_nearby every
// activity on its deals or stakeholders that carries no project link at all.
type Project360Filing struct {
	Attributed         int `json:"attributed"`
	UnattributedNearby int `json:"unattributed_nearby"`
}

// Project360Rollups is the header figures: money in the installation's base
// currency over the caller's deal row scope, counts over their activity row
// scope.
type Project360Rollups struct {
	OpenDealValueMinor int64      `json:"open_deal_value_minor"`
	WonDealValueMinor  int64      `json:"won_deal_value_minor"`
	Currency           string     `json:"currency"`
	OpenCommitments    int        `json:"open_commitments"`
	LastActivityAt     *time.Time `json:"last_activity_at,omitempty"`
	ActivityCount      int        `json:"activity_count"`
}

// truncated reports whether any section was cut at its cap.
func (r Project360Result) truncated() bool {
	return (r.Deals != nil && r.Deals.Truncated) ||
		(r.Stakeholders != nil && r.Stakeholders.Truncated) ||
		(r.Contracts != nil && r.Contracts.Truncated) ||
		(r.Documents != nil && r.Documents.Truncated) ||
		(r.Commitments != nil && r.Commitments.Truncated) ||
		(r.Activities != nil && r.Activities.Truncated)
}

// project360Result carries the page onto the surface's shape. It is a
// rename and a flattening, never a judgement: every row the page holds is
// here, and nothing the page withheld is invented.
func project360Result(page crmcontracts.Project360) Project360Result {
	out := Project360Result{
		AsOf:            page.AsOf,
		Project:         project360Project(page.Project),
		SectionsOmitted: make([]string, 0, len(page.SectionsOmitted)),
	}
	for _, s := range page.SectionsOmitted {
		out.SectionsOmitted = append(out.SectionsOmitted, string(s))
	}
	if page.Organization != nil {
		out.Organization = &Project360Organization{
			OrganizationID: ids.UUID(page.Organization.Id), Name: page.Organization.Name,
		}
	}
	if page.PhaseHistory != nil {
		out.PhaseHistory = project360History(*page.PhaseHistory)
	}
	if page.Deals != nil {
		out.Deals = &Project360Deals{Items: project360Deals(page.Deals.Data), Truncated: page.Deals.Page.HasMore}
	}
	if page.Stakeholders != nil {
		out.Stakeholders = &Project360Stakeholders{
			Items: project360Stakeholders(page.Stakeholders.Data), Truncated: page.Stakeholders.Page.HasMore,
		}
	}
	if page.Contracts != nil {
		out.Contracts = &Project360Contracts{
			Items: project360Contracts(page.Contracts.Data), Truncated: page.Contracts.Page.HasMore,
		}
	}
	if page.Documents != nil {
		out.Documents = &Project360Documents{
			Items: project360Documents(page.Documents.Data), Truncated: page.Documents.Page.HasMore,
		}
	}
	if page.Commitments != nil {
		out.Commitments = &Project360Commitments{
			Items: project360Commitments(page.Commitments.Data, page.AsOf), Truncated: page.Commitments.Page.HasMore,
		}
	}
	if page.Activities != nil {
		out.Activities = &Project360Activities{
			Items: project360Activities(page.Activities.Data), Truncated: page.Activities.Page.HasMore,
		}
	}
	if page.Coverage != nil {
		out.Filing = &Project360Filing{
			Attributed: page.Coverage.Attributed, UnattributedNearby: page.Coverage.UnattributedNearby,
		}
	}
	if page.Rollups != nil {
		out.Rollups = project360Rollups(*page.Rollups)
	}
	return out
}
