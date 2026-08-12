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
	"encoding/json"
	"net/http"

	"github.com/gradionhq/margince/backend/internal/modules/agents"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
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

// advanceDealCommand decodes POST /v1/deals/{id}/advance — the ONE parse of this
// request. The tier gate reads its answer off the call this produces
// (agents.DynamicTierInput, reached from agentgate.go's tierInput), so the
// endpoints a move's 🟢/🟡 is judged from and the move a human is later asked
// about cannot be read differently.
//
// It does not share commandBody, because this is the one decoder whose faults
// the caller must be able to tell apart. THREE of them, three answers, and the
// order matters because each later check presumes the earlier one passed:
// json.Unmarshal alone cannot separate the first two — it fails identically for
// a body that is not JSON and for a body that is perfectly good JSON carrying a
// to_stage_id the UUID decoder refuses. Answering "not readable JSON" to the
// second sends the caller hunting a syntax error that is not there, while the
// real fault — a value they can see and fix — goes unnamed.
//
//nolint:ireturn // a decoder's whole product is the erased command-and-resolver pair restCommands is typed by
func advanceDealCommand(_ agentPolicy, deps restCommandDeps, r *http.Request, body []byte) (agents.GovernedCall, error) {
	// The deal is named by the ROUTE — /deals/{id}/advance — and a path segment
	// that is not an id gets the existence-hiding answer every other decoder here
	// gives, rather than a validation message about a record nobody proved exists.
	id, err := routedID(r)
	if err != nil {
		return nil, err
	}
	if !json.Valid(body) {
		// malformed_json, the code httperr.Decode answers on the session half of
		// this same route — one mistake must not carry two machine codes keyed on
		// which credential the caller presented.
		return nil, httperr.Validation("body", "malformed_json", "the request body is not readable JSON")
	}
	var in struct {
		ToStageID ids.UUID `json:"to_stage_id"`
	}
	if err := json.Unmarshal(body, &in); err != nil {
		// Valid JSON the shape refuses: a non-object, or a to_stage_id that is not
		// a UUID string. Naming the field is right for both — the fix is to send an
		// object carrying a canonical UUID there.
		return nil, httperr.Validation("to_stage_id", "invalid",
			"to_stage_id must be a canonical UUID string on a JSON object body")
	}
	// The omission goes through the one implementation, so a passport reaching
	// this gate and a session reaching advanceDealInput read the SAME sentence.
	// The gate resolves the tier before the handler runs, so without this the rule
	// had two spellings on the one field U3 unified.
	if err := httperr.RequireBodyID("to_stage_id", in.ToStageID); err != nil {
		return nil, err
	}
	return agents.NewAdvanceDealCall(deps.records, deps.stages, agents.AdvanceDealCommand{
		DealID:    id,
		ToStageID: in.ToStageID,
	}), nil
}
