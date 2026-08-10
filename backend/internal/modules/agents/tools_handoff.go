// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package agents

// prepare_handoff: everything the delivery side of a project would otherwise
// have to go and ask the seller for, plus a named list of what is still
// missing.
//
// WHAT A HANDOFF OWES, and why this shape. A handoff fails on the things
// nobody wrote down — who owns the work now, who to call at the client, what
// was actually sold, by when, and what was already promised. So the answer is
// those five facts and a GAP for each one that is absent. The gaps are the
// product: a brief that silently omits the owner reads exactly like a brief
// for a project that has one.
//
// EVERY GAP NAMES THE FIELD IT WAS READ OFF, the same discipline
// whats_slipping_this_week applies to a risk claim. A gap nobody can point at
// a field for would be advice, and this tool does not give advice — it
// reports what the records do and do not say.
//
// IT WRITES NOTHING. Handing work over is a decision with its own verb
// (advance_project_phase); a tool that moved the phase while claiming to
// prepare a briefing would be performing the handoff, not preparing it.

import (
	"context"
	"encoding/json"
	"strconv"
	"time"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/modules/agents/apps"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
	"github.com/gradionhq/margince/backend/internal/shared/ports/mcp"
)

// HandoffFacts is one project's handover material as the seam read it: the
// record itself, what is rolled up to it, and the promises outstanding on it.
// The judging — which of these is a gap — happens in the tool.
type HandoffFacts struct {
	// AsOf is the instant the commitments below were judged against, stamped
	// by the seam for the reason CommitmentSweep gives.
	AsOf         time.Time
	Project      HandoffProject
	Deals        []HandoffDeal
	Stakeholders []HandoffStakeholder
	// DealsTruncated and StakeholdersTruncated report a list read that
	// stopped at its bound. They are not cosmetic: two of the gaps below are
	// claims that something is ABSENT, and absence cannot be read off a
	// truncated list. A bounded read that raised them anyway would tell the
	// receiving side no deal was ever won on a project with fifty-one of them.
	DealsTruncated        bool
	StakeholdersTruncated bool
	OpenCommitments       []OpenCommitment
	CommitmentsTruncated  bool
}

// HandoffProject is the project row itself, carried across the seam with the
// fields a handover is judged on.
type HandoffProject struct {
	ProjectID      ids.UUID
	Name           string
	Key            string
	Phase          string
	Description    string
	OrganizationID ids.UUID
	OwnerID        *ids.UUID
	StartedAt      *time.Time
	TargetEndDate  *time.Time
}

// HandoffReader serves one project's handover material under the caller's row
// scope. Compose implements it over the modules' own gated reads.
type HandoffReader func(ctx context.Context, projectID ids.UUID) (HandoffFacts, error)

// RegisterHandoffTool wires the sales-to-delivery briefing.
func RegisterHandoffTool(r *Registry, read HandoffReader) {
	if read == nil {
		return
	}
	r.Register(prepareHandoff{read: read})
}

// The gap vocabulary, and the field each one is read off. Both halves are
// stated here so a gap cannot be added without saying what evidences it.
const (
	gapNoDeliveryOwner      = "no_delivery_owner"
	gapNoStakeholder        = "no_stakeholder"
	gapStakeholderRoleUnset = "stakeholder_role_unset"
	gapNoTargetEndDate      = "no_target_end_date"
	gapNoWonDeal            = "no_won_deal"
	gapUnpricedWonDeal      = "unpriced_won_deal"
	gapOverdueCommitment    = "overdue_commitment"
)

// dealStatusWon is the one status that means something was sold, read from
// the contract's own enum rather than spelled here, so a vocabulary change
// reaches this judgement rather than leaving it quietly true of nothing.
var dealStatusWon = string(crmcontracts.DealStatusWon)

type prepareHandoff struct{ read HandoffReader }

func (t prepareHandoff) Spec() mcp.ToolSpec {
	return mcp.ToolSpec{
		Name: "prepare_handoff", Title: "Prepare a delivery handoff", Version: toolVersionV1,
		Description:   prepareHandoffCopy.render(),
		RequiredScope: principal.ScopeRead, Tier: mcp.TierAutoExecute,
		OpenAPIOp: "getProject + listDeals + listProjectStakeholders + listActivities",
		InputSchema: schema(`{"type":"object","required":["project_id"],"properties":{
			"project_id":{"type":"string","format":"uuid","description":"The project being handed to delivery"}},
			"additionalProperties":false}`),
		OutputSchema: schemaFor[PreparedHandoff](),
		// The view renders the same answer with the gaps beside the facts they
		// are about, which is the comparison a person makes when deciding
		// whether the work is ready to hand over.
		UI: &mcp.ToolUI{ResourceURI: apps.HandoffURI},
	}
}

func (t prepareHandoff) Handle(ctx context.Context, in json.RawMessage) (json.RawMessage, error) {
	var args struct {
		ProjectID ids.UUID `json:"project_id"`
	}
	if err := decodeArgs(in, &args); err != nil {
		return nil, err
	}
	facts, err := t.read(ctx, args.ProjectID)
	if err != nil {
		return nil, err
	}
	// The brief carries names, descriptions and task subjects read off rows
	// this call does not hand over, so the answer is tainted with their
	// content.
	noteDerivedContent(ctx)
	noteEvidence(ctx, datasource.EntityProject, facts.Project.ProjectID)
	for _, d := range facts.Deals {
		noteEvidence(ctx, datasource.EntityDeal, d.DealID)
	}
	for _, s := range facts.Stakeholders {
		noteEvidence(ctx, datasource.EntityPerson, s.PersonID)
	}
	commitments := make([]CommitmentItem, 0, len(facts.OpenCommitments))
	for _, c := range facts.OpenCommitments {
		noteEvidence(ctx, datasource.EntityActivity, c.TaskID)
		commitments = append(commitments, c.wire(facts.AsOf))
	}
	if facts.CommitmentsTruncated || facts.DealsTruncated || facts.StakeholdersTruncated {
		noteWarning(ctx, warningSweepTruncated, handoffTruncatedMessage)
	}
	return json.Marshal(assembleHandoff(facts, commitments))
}

// assembleHandoff shapes the answer and judges the gaps over the same facts
// it reports, so a gap can never disagree with the field beside it.
func assembleHandoff(facts HandoffFacts, commitments []CommitmentItem) PreparedHandoff {
	p := facts.Project
	out := PreparedHandoff{
		ProjectID: p.ProjectID, Name: p.Name, Key: p.Key, Phase: p.Phase,
		Description: p.Description, OrganizationID: p.OrganizationID,
		OwnerID: p.OwnerID, StartedAt: p.StartedAt, TargetEndDate: p.TargetEndDate,
		// Never null, for the reason every list-shaped answer on this surface
		// gives: null reads as "unknown" where an empty array says "none".
		Deals:           orEmpty(facts.Deals),
		Stakeholders:    orEmpty(facts.Stakeholders),
		AsOf:            facts.AsOf,
		OpenCommitments: orEmpty(commitments),
	}
	out.Gaps = handoffGaps(out, facts)
	return out
}

// handoffTruncatedMessage is the rule every bounded read on this surface
// states, sharpened for the one answer where a bound changes what the answer
// MEANS: two of the gaps are claims that something is absent, and a bounded
// read cannot make one. They are withheld rather than guessed, so this says
// what is missing from the answer as well as from the project.
const handoffTruncatedMessage = "This project has more deals, contacts or open commitments than " +
	"one briefing lists. Report the lists as partial, and do not report an absent " +
	"won deal or an absent contact as a finding — those checks were withheld."

// handoffGaps judges the assembled brief. It reads the ANSWER rather than the
// facts it came from, so every gap is a statement about something the reader
// can see for themselves in the same document.
func handoffGaps(h PreparedHandoff, facts HandoffFacts) []HandoffGap {
	gaps := make([]HandoffGap, 0)
	if h.OwnerID == nil {
		gaps = append(gaps, HandoffGap{
			gapNoDeliveryOwner, "project.owner_id",
			"Nobody owns this project, so the handover has no receiving side.",
		})
	}
	if h.TargetEndDate == nil {
		gaps = append(gaps, HandoffGap{
			gapNoTargetEndDate, "project.target_end_date",
			"No target end date, so there is nothing to deliver against.",
		})
	}
	gaps = append(gaps, stakeholderGaps(h.Stakeholders, facts.StakeholdersTruncated)...)
	gaps = append(gaps, dealGaps(h.Deals, facts.DealsTruncated)...)
	if overdue := countOverdue(h.OpenCommitments); overdue > 0 {
		gaps = append(gaps, HandoffGap{
			gapOverdueCommitment, "activity.due_at",
			countPhrase(overdue, "commitment is", "commitments are") +
				" already past due at handover.",
		})
	}
	return gaps
}

// stakeholderGaps judges the seats. `bounded` reports a read that stopped at
// its cap: an empty page of a truncated read says nothing about whether a
// contact exists, so the absence claim is withheld rather than asserted.
func stakeholderGaps(stakeholders []HandoffStakeholder, bounded bool) []HandoffGap {
	if len(stakeholders) == 0 {
		if bounded {
			return nil
		}
		return []HandoffGap{{
			gapNoStakeholder, "relationship.project_stakeholder",
			"No contact this caller can see is named on the client side of this work.",
		}}
	}
	untitled := 0
	for _, s := range stakeholders {
		if s.Role == "" {
			untitled++
		}
	}
	if untitled == 0 {
		return nil
	}
	return []HandoffGap{{
		gapStakeholderRoleUnset, "relationship.role",
		countPhrase(untitled, "named contact has", "named contacts have") +
			" no recorded part in the work.",
	}}
}

// dealGaps judges what was sold. `bounded` reports a read that stopped at its
// cap, and it withholds exactly one claim: "no won deal" is a statement about
// every deal on the project, which a truncated list cannot support. The
// unpriced count is a statement about the deals that WERE read, so it stands.
func dealGaps(deals []HandoffDeal, bounded bool) []HandoffGap {
	won, unpriced := 0, 0
	for _, d := range deals {
		if d.Status != dealStatusWon {
			continue
		}
		won++
		if d.AmountMinor == nil {
			unpriced++
		}
	}
	if won == 0 {
		if bounded {
			return nil
		}
		return []HandoffGap{{
			gapNoWonDeal, "deal.status",
			"No won deal this caller can see is rolled up to this project, " +
				"so what was sold is not recorded here.",
		}}
	}
	if unpriced == 0 {
		return nil
	}
	return []HandoffGap{{
		gapUnpricedWonDeal, "deal.amount_minor",
		countPhrase(unpriced, "won deal carries", "won deals carry") +
			" no amount, so delivery cannot see what was sold.",
	}}
}

func countOverdue(commitments []CommitmentItem) int {
	overdue := 0
	for _, c := range commitments {
		if c.State == commitmentOverdue {
			overdue++
		}
	}
	return overdue
}

// countPhrase renders "1 thing is" / "3 things are", so a gap message reads
// as a sentence rather than as a count with a suffix bolted on. The verb form
// comes from plural, which this surface already has one of.
func countPhrase(n int, one, many string) string {
	return strconv.Itoa(n) + " " + plural(n, one, many)
}

// orEmpty answers an empty slice for a nil one, so a list-shaped member is
// never serialized as null.
func orEmpty[T any](in []T) []T {
	if in == nil {
		return []T{}
	}
	return in
}
