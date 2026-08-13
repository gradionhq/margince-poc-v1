// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package agents

// The four record-lifecycle commands (gradionhq/margince-poc-v1#928 task 7):
// graduating a lead, retiring one, stepping a project along its phase ladder,
// and moving a deal between stages. Each names ONE routed record and each
// question below rests on that one row — Guards on whether an approval for it
// could ever be released, Subject on the version it pins and the name a human
// reads — so all four ride anchoredRecord (command.go) for a single read.
//
// The vocabulary checks come FIRST in every Guards here, before the read. A
// trigger that is not genuine engagement, a phase that is not a phase, a close
// with no reason: none of them names a record worth a round trip to refuse, and
// each is a refusal the executor makes anyway — reaching it from there instead
// would spend the human's one-shot approval on the way past.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
	"github.com/gradionhq/margince/backend/internal/shared/ports/mcp"
)

// PromoteLeadCommand is one lead promotion, whichever door asked for it.
//
// Trigger travels because both questions read it: Guards refuses one that is
// not genuine engagement, and Subject names it in the line a human approves —
// "promote this lead" and "promote this lead because they replied" are not the
// same decision. The evidence note does not: nothing here reads it.
type PromoteLeadCommand struct {
	LeadID  ids.UUID
	Trigger string
}

// NewPromoteLeadCall binds one promotion to the resolver that answers for it,
// reading the lead through the record seam.
//
//nolint:ireturn // the call IS the product: a resolver named concretely here is exactly the thing that must not leave this package
func NewPromoteLeadCall(records datasource.SystemOfRecordProvider, cmd PromoteLeadCommand) GovernedCall {
	return bind[PromoteLeadCommand](&promoteLeadResolver{
		lead: anchoredRecord{records: records, entityType: datasource.EntityLead},
	}, cmd)
}

type promoteLeadResolver struct {
	lead anchoredRecord
}

// Subject names the LEAD the approval binds to, pins the version the human's
// judgment was formed against, and says WHICH engagement justifies the
// promotion.
func (r *promoteLeadResolver) Subject(ctx context.Context, cmd PromoteLeadCommand) (StageInfo, error) {
	rec, err := r.lead.row(ctx, cmd.LeadID)
	if err != nil {
		return StageInfo{}, err
	}
	return StageInfo{
		TargetType:    string(datasource.EntityLead),
		TargetID:      cmd.LeadID,
		TargetVersion: &rec.Version,
		Summary:       fmt.Sprintf("Promote lead %s to a contact (%s)", recordLabel(rec), cmd.Trigger),
	}, nil
}

// Guards refuses a trigger that is not genuine engagement — cold outbound with
// no reply must never even reach the inbox — and then the lead itself, the
// same two ways patchResolver.Guards refuses its own target.
func (r *promoteLeadResolver) Guards(ctx context.Context, cmd PromoteLeadCommand) error {
	if err := requireGenuineTrigger(cmd.Trigger); err != nil {
		return err
	}
	return r.lead.refuse(ctx, cmd.LeadID)
}

// requireGenuineTrigger admits the engagement a promotion rests on.
//
// One function for both doors: the staging path asks it through Guards above
// and the execution path through promoteLead.Handle, which the approved retry
// re-enters without passing Guards. Two copies of the predicate AND its
// sentence is how the two doors come to disagree about what counts as
// engagement, or to say it differently for the same refusal.
func requireGenuineTrigger(trigger string) error {
	if validTriggers[trigger] {
		return nil
	}
	return &BadArgsError{Cause: fmt.Errorf("trigger %q is not genuine engagement", trigger)}
}

// DisqualifyLeadCommand is one lead retirement, whichever door asked for it.
// The routed lead is the whole of it: DELETE /v1/leads/{id} carries no body,
// and the tool's own arguments carry nothing else either.
type DisqualifyLeadCommand struct {
	LeadID ids.UUID
}

// NewDisqualifyLeadCall binds one retirement to the resolver that answers for
// it.
//
//nolint:ireturn // the call IS the product: a resolver named concretely here is exactly the thing that must not leave this package
func NewDisqualifyLeadCall(records datasource.SystemOfRecordProvider, cmd DisqualifyLeadCommand) GovernedCall {
	return bind[DisqualifyLeadCommand](&disqualifyLeadResolver{
		lead: anchoredRecord{records: records, entityType: datasource.EntityLead},
	}, cmd)
}

type disqualifyLeadResolver struct {
	lead anchoredRecord
}

func (r *disqualifyLeadResolver) Subject(ctx context.Context, cmd DisqualifyLeadCommand) (StageInfo, error) {
	rec, err := r.lead.row(ctx, cmd.LeadID)
	if err != nil {
		return StageInfo{}, err
	}
	return StageInfo{
		TargetType:    string(datasource.EntityLead),
		TargetID:      cmd.LeadID,
		TargetVersion: &rec.Version,
		Summary:       fmt.Sprintf("Disqualify lead %s", recordLabel(rec)),
	}, nil
}

func (r *disqualifyLeadResolver) Guards(ctx context.Context, cmd DisqualifyLeadCommand) error {
	return r.lead.refuse(ctx, cmd.LeadID)
}

// AdvanceProjectPhaseCommand is one project phase transition, whichever door
// asked for it.
//
// Reason travels even though Subject never renders it, because Guards reads
// it: closing without one is refused by the contract, and the rule is a
// property of the PAIR (phase, reason) rather than of either alone. It carries
// no if_version — the pin an approval binds to is taken server-side inside the
// staging transaction, so a caller-supplied version has no reader here, the
// same call PatchCommand's own doc makes.
type AdvanceProjectPhaseCommand struct {
	ProjectID ids.UUID
	ToPhase   string
	Reason    *string
}

// NewAdvanceProjectPhaseCall binds one phase transition to the resolver that
// answers for it.
//
//nolint:ireturn // the call IS the product: a resolver named concretely here is exactly the thing that must not leave this package
func NewAdvanceProjectPhaseCall(records datasource.SystemOfRecordProvider, cmd AdvanceProjectPhaseCommand) GovernedCall {
	return bind[AdvanceProjectPhaseCommand](&advanceProjectPhaseResolver{
		project: anchoredRecord{records: records, entityType: datasource.EntityProject},
	}, cmd)
}

type advanceProjectPhaseResolver struct {
	project anchoredRecord
}

func (r *advanceProjectPhaseResolver) Subject(ctx context.Context, cmd AdvanceProjectPhaseCommand) (StageInfo, error) {
	rec, err := r.project.row(ctx, cmd.ProjectID)
	if err != nil {
		return StageInfo{}, err
	}
	return StageInfo{
		TargetType:    string(datasource.EntityProject),
		TargetID:      cmd.ProjectID,
		TargetVersion: &rec.Version,
		Summary:       fmt.Sprintf("Move project %s to %s", recordLabel(rec), cmd.ToPhase),
	}, nil
}

// Guards refuses a phase the ladder does not have and a close with no reason,
// then the project itself.
func (r *advanceProjectPhaseResolver) Guards(ctx context.Context, cmd AdvanceProjectPhaseCommand) error {
	if err := requireProjectPhase(cmd.ToPhase, cmd.Reason); err != nil {
		return err
	}
	return r.project.refuse(ctx, cmd.ProjectID)
}

// requireProjectPhase admits a transition's destination and the reason the
// contract attaches to one of them.
//
// One function for both doors: the staging path asks it through Guards above
// and the execution path asks it through advanceProjectPhase.readArgs, so a
// phase refused before a human is asked cannot be a phase admitted by the
// approved retry — and neither can the reverse, which is the direction that
// costs a human's yes.
func requireProjectPhase(toPhase string, reason *string) error {
	if !projectPhases[toPhase] {
		return &BadArgsError{Cause: fmt.Errorf("to_phase %q is not a project phase", toPhase)}
	}
	if toPhase == projectPhaseClosed && (reason == nil || strings.TrimSpace(*reason) == "") {
		return &BadArgsError{Cause: errors.New("reason is required when to_phase is closed")}
	}
	return nil
}

// AdvanceDealCommand is one deal stage move, whichever door asked for it.
//
// ToStageID travels because the summary turns on it: the destination's
// configured SEMANTIC is what makes a move a close, a reopen or a routine step,
// and those are opposite decisions wearing one shape. It carries neither
// lost_reason nor if_version — nothing here reads either.
//
// It is the command BOTH deal-move tools stage through: progress_deal composes
// advance_deal with a note (interfaces.md §2.2) and stages the identical act,
// so it stages the identical shape rather than a second copy of it that could
// come to describe the same move differently.
type AdvanceDealCommand struct {
	DealID    ids.UUID
	ToStageID ids.UUID
}

// NewAdvanceDealCall binds one deal move to the resolver that answers for it.
// stages resolves the destination's configured semantic — the property the
// summary is written from, never the request's own labels.
//
//nolint:ireturn // the call IS the product: a resolver named concretely here is exactly the thing that must not leave this package
func NewAdvanceDealCall(records datasource.SystemOfRecordProvider, stages StageResolver, cmd AdvanceDealCommand) GovernedCall {
	return advanceDealCall{
		GovernedCall: bind[AdvanceDealCommand](&advanceDealResolver{
			deal:   anchoredRecord{records: records, entityType: datasource.EntityDeal},
			stages: stages,
		}, cmd),
		records: records,
		stages:  stages,
		cmd:     cmd,
	}
}

// advanceDealCall is the bound deal move plus the one question this operation's
// tier turns on: 🟢/🟡 is decided per invocation, from BOTH endpoints of the
// move, so the gate cannot be told what to decide from without being told which
// move it is.
//
// It is its own type rather than a method every bound command carries, because
// DynamicTierInput's refusal (command.go) has to be a real refusal — a call that
// answers no tier must be turned away, not handed an empty input the resolver
// would read as "not open" for a reason nobody chose.
type advanceDealCall struct {
	GovernedCall
	records datasource.SystemOfRecordProvider
	stages  StageResolver
	cmd     AdvanceDealCommand
}

// tierInput shows the gate both endpoints of the move, through the builder every
// door shares (DealMoveTierInput). args travels because the resolver input
// carries the call's own arguments alongside the semantics resolved from them.
func (c advanceDealCall) tierInput(ctx context.Context, args json.RawMessage) (mcp.TierResolverInput, error) {
	return DealMoveTierInput(ctx, c.records, c.stages, c.cmd.DealID, c.cmd.ToStageID, args)
}

type advanceDealResolver struct {
	deal   anchoredRecord
	stages StageResolver
}

// Subject pins the staged move to the deal's CURRENT version, so an approval
// given for "close this deal as it stands" cannot execute against a deal that
// changed in between, and names the move by BOTH of its endpoints
// (dealMoveSummary) — reading the source out of the record this staging
// already read rather than fetching the deal a second time.
func (r *advanceDealResolver) Subject(ctx context.Context, cmd AdvanceDealCommand) (StageInfo, error) {
	rec, err := r.deal.row(ctx, cmd.DealID)
	if err != nil {
		return StageInfo{}, err
	}
	semantic, _, err := r.stages.StageSemantic(ctx, cmd.ToStageID)
	if err != nil {
		return StageInfo{}, err
	}
	return StageInfo{
		TargetType:    string(datasource.EntityDeal),
		TargetID:      cmd.DealID,
		TargetVersion: &rec.Version,
		Summary:       dealMoveSummary(ctx, r.stages, rec, semantic),
	}, nil
}

// Guards refuses the deal the same two ways patchResolver.Guards refuses its
// own target. It does NOT re-derive the move's tier: whether this move needs an
// approval at all is the gate's question, answered before staging is reached
// (advanceDealTier, dealmove.go), and asking it again here would be a second
// opinion about a decision already taken.
func (r *advanceDealResolver) Guards(ctx context.Context, cmd AdvanceDealCommand) error {
	return r.deal.refuse(ctx, cmd.DealID)
}
