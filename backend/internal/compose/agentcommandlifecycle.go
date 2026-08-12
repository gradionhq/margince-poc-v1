// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The REST door's half of the four record-lifecycle commands
// (gradionhq/margince-poc-v1#928 task 7): graduating a lead, retiring one,
// stepping a project along its phase ladder, and moving a deal between stages.
// Each names its record in the route and its operands in the body, and each
// has a tool-door twin resolving the identical command
// (modules/agents/commandlifecycle.go).

import (
	"net/http"

	"github.com/gradionhq/margince/backend/internal/modules/agents"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// promoteLeadCommand decodes POST /v1/leads/{id}/promote. The trigger is read
// off crm.yaml's PromoteLeadRequest; the evidence sub-object is not, because
// nothing the resolver answers reads it.
//
//nolint:ireturn // a decoder's whole product is the erased command-and-resolver pair restCommands is typed by
func promoteLeadCommand(_ agentPolicy, deps restCommandDeps, r *http.Request, body []byte) (agents.GovernedCall, error) {
	id, err := routedID(r)
	if err != nil {
		return nil, err
	}
	in, err := commandBody[struct {
		Trigger string `json:"trigger"`
	}](body)
	if err != nil {
		return nil, err
	}
	return agents.NewPromoteLeadCall(deps.records, agents.PromoteLeadCommand{
		LeadID:  id,
		Trigger: in.Trigger,
	}), nil
}

// disqualifyLeadCommand decodes DELETE /v1/leads/{id}, which carries no body
// at all — the routed lead is the whole of the call.
//
//nolint:ireturn // a decoder's whole product is the erased command-and-resolver pair restCommands is typed by
func disqualifyLeadCommand(_ agentPolicy, deps restCommandDeps, r *http.Request, _ []byte) (agents.GovernedCall, error) {
	id, err := routedID(r)
	if err != nil {
		return nil, err
	}
	return agents.NewDisqualifyLeadCall(deps.records, agents.DisqualifyLeadCommand{LeadID: id}), nil
}

// advanceProjectPhaseCommand decodes POST /v1/projects/{id}/advance. Both body
// fields travel: the resolver refuses a phase the ladder does not have, and a
// close with no reason, before a human is asked about either.
//
//nolint:ireturn // a decoder's whole product is the erased command-and-resolver pair restCommands is typed by
func advanceProjectPhaseCommand(_ agentPolicy, deps restCommandDeps, r *http.Request, body []byte) (agents.GovernedCall, error) {
	id, err := routedID(r)
	if err != nil {
		return nil, err
	}
	in, err := commandBody[struct {
		ToPhase string  `json:"to_phase"`
		Reason  *string `json:"reason"`
	}](body)
	if err != nil {
		return nil, err
	}
	return agents.NewAdvanceProjectPhaseCall(deps.records, agents.AdvanceProjectPhaseCommand{
		ProjectID: id,
		ToPhase:   in.ToPhase,
		Reason:    in.Reason,
	}), nil
}

// advanceDealCommand decodes POST /v1/deals/{id}/advance.
//
// It reads the same two ids advanceDealTierInput (agentgate.go) reads, and
// that overlap is deliberate for now: the tier question is answered BEFORE
// admission and the staged subject only on the refusal path, so the two run at
// different moments and each takes the reading its own moment needs. Folding
// them into one reading is task 9's, and doing it here would mean this decoder
// answering a tier question it is never asked.
//
//nolint:ireturn // a decoder's whole product is the erased command-and-resolver pair restCommands is typed by
func advanceDealCommand(_ agentPolicy, deps restCommandDeps, r *http.Request, body []byte) (agents.GovernedCall, error) {
	id, err := routedID(r)
	if err != nil {
		return nil, err
	}
	in, err := commandBody[struct {
		ToStageID ids.UUID `json:"to_stage_id"`
	}](body)
	if err != nil {
		return nil, err
	}
	return agents.NewAdvanceDealCall(deps.records, deps.stages, agents.AdvanceDealCommand{
		DealID:    id,
		ToStageID: in.ToStageID,
	}), nil
}
