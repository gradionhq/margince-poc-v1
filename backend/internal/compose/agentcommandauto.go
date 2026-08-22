// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The REST door's half of the four auto-execute commands
// (gradionhq/margince-poc-v1#928 task 7): logging an activity, drafting a
// reply, re-associating an activity, and running a report. All four are 🟢
// today and none of them stages, so these decoders are reached only if a tier
// floor (#982) tightens one — registered anyway, for the reason
// agentcommandnested.go's seven are: the route walk's guess is what this table
// replaces, and a guess is only ever as good as the route's shape.
//
// runReport is the clearest case. Its route is POST /v1/reports/{report}:
// there is no {id} for the walk to read and no record type on its policy
// entry, so the walk can only answer an empty target with no name attached to
// it, where the command at least says which report.

import (
	"encoding/json"
	"net/http"

	"github.com/gradionhq/margince/backend/internal/modules/agents"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// logActivityCommand decodes POST /v1/activities. The body IS the activity's
// fields — the route names no record, because the activity does not exist yet
// — so this is the same shape createCommand takes for every other create.
//
//nolint:ireturn,unparam // ireturn: a decoder's whole product is the erased command-and-resolver pair restCommands is typed by. unparam: the error is always nil TODAY (a create has no id to fail parsing), but every restCommands entry shares this signature
func logActivityCommand(_ agentPolicy, _ restCommandDeps, _ *http.Request, body []byte) (agents.GovernedCall, error) {
	return agents.NewLogActivityCall(agents.LogActivityCommand{Fields: json.RawMessage(body)}), nil
}

// createTaskCommand decodes POST /v1/tasks. A task is an activity of kind
// task, so it binds to the same resolver a logged activity does; the kind is
// stamped here so the staged command names what the door will write.
//
//nolint:ireturn // a decoder's whole product is the erased command-and-resolver pair restCommands is typed by
func createTaskCommand(_ agentPolicy, _ restCommandDeps, _ *http.Request, body []byte) (agents.GovernedCall, error) {
	fields, err := agents.TaskAsActivity(json.RawMessage(body))
	if err != nil {
		return nil, err
	}
	return agents.NewLogActivityCall(agents.LogActivityCommand{Fields: fields}), nil
}

// draftEmailCommand decodes POST /v1/activities/{id}/draft-email. The optional
// `intent` body is not read: nothing the resolver answers depends on it.
//
//nolint:ireturn // a decoder's whole product is the erased command-and-resolver pair restCommands is typed by
func draftEmailCommand(_ agentPolicy, deps restCommandDeps, r *http.Request, _ []byte) (agents.GovernedCall, error) {
	id, err := routedID(r)
	if err != nil {
		return nil, err
	}
	return agents.NewDraftEmailCall(deps.records, agents.DraftEmailCommand{ActivityID: id}), nil
}

// relinkActivityCommand decodes POST /v1/activities/{id}/relink. The
// destination travels because both of the resolver's questions read it: one
// refuses a type that is not a link target, and the other names where the
// activity is going.
//
//nolint:ireturn // a decoder's whole product is the erased command-and-resolver pair restCommands is typed by
func relinkActivityCommand(_ agentPolicy, deps restCommandDeps, r *http.Request, body []byte) (agents.GovernedCall, error) {
	id, err := routedID(r)
	if err != nil {
		return nil, err
	}
	in, err := commandBody[struct {
		EntityType string   `json:"entity_type"`
		EntityID   ids.UUID `json:"entity_id"`
	}](body)
	if err != nil {
		return nil, err
	}
	return agents.NewRelinkActivityCall(deps.records, agents.RelinkActivityCommand{
		ActivityID: id,
		EntityType: in.EntityType,
		EntityID:   in.EntityID,
	}), nil
}

// relinkThreadCommand decodes POST /v1/activities/relink-thread and
// relinkActivitiesCommand POST /v1/activities/relink-bulk: the batch forms of
// the relink, carrying the same destination for the same two questions.
//
//nolint:ireturn // a decoder's whole product is the erased command-and-resolver pair restCommands is typed by
func relinkThreadCommand(_ agentPolicy, deps restCommandDeps, _ *http.Request, body []byte) (agents.GovernedCall, error) {
	in, err := commandBody[struct {
		ThreadKey  string   `json:"thread_key"`
		EntityType string   `json:"entity_type"`
		EntityID   ids.UUID `json:"entity_id"`
	}](body)
	if err != nil {
		return nil, err
	}
	return agents.NewRelinkThreadCall(deps.records, agents.RelinkThreadCommand{
		ThreadKey: in.ThreadKey, EntityType: in.EntityType, EntityID: in.EntityID,
	}), nil
}

//nolint:ireturn // a decoder's whole product is the erased command-and-resolver pair restCommands is typed by
func relinkActivitiesCommand(_ agentPolicy, deps restCommandDeps, _ *http.Request, body []byte) (agents.GovernedCall, error) {
	in, err := commandBody[struct {
		ActivityIDs []ids.UUID `json:"activity_ids"`
		EntityType  string     `json:"entity_type"`
		EntityID    ids.UUID   `json:"entity_id"`
	}](body)
	if err != nil {
		return nil, err
	}
	return agents.NewRelinkActivitiesCall(deps.records, agents.RelinkActivitiesCommand{
		ActivityIDs: in.ActivityIDs, EntityType: in.EntityType, EntityID: in.EntityID,
	}), nil
}

// runReportCommand decodes POST /v1/reports/{report}. The report key is a PATH
// parameter, and not the route's own {id} — so it goes through pathOperand
// (agentcommandoperand.go), which answers 422 naming the parameter rather than
// routedID's existence-hiding 404: a report key names no row whose existence
// this door could be hiding.
//
//nolint:ireturn // a decoder's whole product is the erased command-and-resolver pair restCommands is typed by
func runReportCommand(_ agentPolicy, _ restCommandDeps, r *http.Request, _ []byte) (agents.GovernedCall, error) {
	report, err := pathOperand(r, "report")
	if err != nil {
		return nil, err
	}
	return agents.NewRunReportCall(agents.RunReportCommand{Report: report}), nil
}

// decideApprovalCommand decodes a decision on ONE staged proposal. The verdict
// is read off the route rather than the body: approve and reject are two
// operations over one decision, and the body carries only the reason.
//
//nolint:ireturn // a decoder's whole product is the erased command-and-resolver pair restCommands is typed by
func decideApprovalCommand(pol agentPolicy, _ restCommandDeps, r *http.Request, _ []byte) (agents.GovernedCall, error) {
	id, err := pathOperand(r, "id")
	if err != nil {
		return nil, err
	}
	approvalID, err := ids.Parse(id)
	if err != nil {
		return nil, err
	}
	return agents.NewDecideApprovalCall(agents.DecideApprovalCommand{
		ApprovalID: approvalID, Approve: approvesApproval(pol.Op),
	}), nil
}

// decideBundleCommand decodes the same decision given to a whole act.
//
//nolint:ireturn // a decoder's whole product is the erased command-and-resolver pair restCommands is typed by
func decideBundleCommand(pol agentPolicy, _ restCommandDeps, r *http.Request, _ []byte) (agents.GovernedCall, error) {
	id, err := pathOperand(r, "bundle_id")
	if err != nil {
		return nil, err
	}
	bundleID, err := ids.Parse(id)
	if err != nil {
		return nil, err
	}
	return agents.NewDecideBundleCall(agents.DecideBundleCommand{
		BundleID: bundleID, Approve: approvesApproval(pol.Op),
	}), nil
}

// approvesApproval reads the verdict out of the operation that carried it.
// Written as "which operations approve" rather than "which reject" so an
// operation this table has never heard of decides nothing: an unknown verb
// falls to reject, which discards a proposal instead of performing one.
func approvesApproval(op string) bool {
	return op == opApproveApproval || op == opApproveApprovalBundle
}
