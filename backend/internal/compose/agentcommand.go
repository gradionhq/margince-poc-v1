// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The REST door's half of the governance seam (modules/agents/command.go):
// what turns an HTTP request into the operation's typed command.
//
// The door decodes; it does not interpret. What the approval binds to, and what
// would be refused anyway, come back from the resolver the command is bound to
// — the same resolver the tool door reaches for the same operation. The rest of
// the gate's questions are still answered elsewhere: the tier by the generated
// policy and dynamicTierInputs, the inbox line by restSummary.

import (
	"encoding/json"
	"net/http"

	"github.com/gradionhq/margince/backend/internal/modules/agents"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
)

// restCommandDeps is what a decoder needs to build its command's resolver.
//
// A struct rather than a positional argument because the dependency is per
// FAMILY: the archive reads the record seam, an update_record command will want
// the field-ownership probe, a send the comms seam. Passed positionally, each
// family added would re-sign this map type and every entry already in it — the
// churn a table of twelve identical signatures is least able to absorb.
//
// It is not tierDeps, which answers a different question (what tier is this
// call) and is consulted before admission rather than at staging; the two
// happen to share a provider today and have no reason to share a shape.
type restCommandDeps struct {
	records datasource.SystemOfRecordProvider
}

// restCommands maps a crm.yaml operationId to the decoder that turns an HTTP
// request into the operation's typed command, bound to the resolver that
// speaks it.
//
// The table covers one operation family, not the surface: an operation with no
// entry resolves its staged target by walking the route
// (stagedTargetByRoute) instead, which is the guess this seam replaces family
// by family (gradionhq/margince-poc-v1#928).
//
// Every create and every whole-record patch route is registered, all
// twenty-five. Six of the thirteen create record types (custom_field, list,
// offer_template, product, saved_view, tag) create through their own
// module's handler, never through create_record's own datasource-provider
// write path — but that asymmetry is not this table's to answer for.
// createResolver.Guards (command.go) deliberately asks nothing about whether
// create_record itself "serves" a record type: that question has a
// door-dependent answer (create_record's own Handle cannot express these six
// types; the REST operation that creates one performs it fine through its
// own handler), so it is asked once, at createRecord.StageInfo (tools.go),
// on the one door where it is a fact about the executor rather than about
// the operation. Every whole-record patch route is registered for the same
// reason patch never had that question at all — see patchResolver.Guards'
// own comment.
var restCommands = map[string]func(pol agentPolicy, deps restCommandDeps, r *http.Request, body []byte) (agents.GovernedCall, error){
	"archiveActivity":      archiveCommand,
	"archiveDeal":          archiveCommand,
	"archiveList":          archiveCommand,
	"archiveOffer":         archiveCommand,
	"archiveOfferTemplate": archiveCommand,
	"archiveOrganization":  archiveCommand,
	"archivePerson":        archiveCommand,
	"archiveProduct":       archiveCommand,
	"archiveProject":       archiveCommand,
	"archiveRelationship":  archiveCommand,
	"archiveSavedView":     archiveCommand,
	"archiveTag":           archiveCommand,

	"createCustomField":         createCommand,
	"createDeal":                createCommand,
	"createLead":                createCommand,
	"createList":                createCommand,
	"createOfferTemplate":       createCommand,
	"createOrganization":        createCommand,
	"createPerson":              createCommand,
	"createProduct":             createCommand,
	"createProject":             createCommand,
	"createRelationship":        createCommand,
	"createSavedView":           createCommand,
	"createTag":                 createCommand,
	"createWebhookSubscription": createCommand,

	opRenameCustomField:         patchCommand,
	"updateActivity":            patchCommand,
	"updateDeal":                patchCommand,
	"updateLead":                patchCommand,
	"updateOffer":               patchCommand,
	"updateOrganization":        patchCommand,
	"updatePerson":              patchCommand,
	"updateProduct":             patchCommand,
	"updateProject":             patchCommand,
	"updateRelationship":        patchCommand,
	"updateSavedView":           patchCommand,
	"updateWebhookSubscription": patchCommand,

	// The eight bespoke confirm-first commands (agentcommandoperand.go): none
	// of them is a whole-record patch, so none belongs above — each targets
	// the routed record but carries a SECOND operand (a path segment or, for
	// removeProjectStakeholder, a second path parameter) that a projection
	// onto update_record's own {record_type, id, fields} arguments cannot
	// express (gradionhq/margince-poc-v1#928 task 5).
	"confirmOrganizationFact":         confirmFactCommand,
	"updateOrganizationFact":          updateFactCommand,
	"confirmOrganizationProfileField": confirmProfileFieldCommand,
	"updateOrganizationProfileField":  updateProfileFieldCommand,
	"retireCustomField":               retireCustomFieldCommand,
	"updateCustomFieldOptions":        updateCustomFieldOptionsCommand,
	"setProjectStakeholder":           setStakeholderCommand,
	"removeProjectStakeholder":        removeStakeholderCommand,

	// The seven bespoke auto-execute commands (agentcommandnested.go). Six of
	// the seven — every one but upsertPartner — are nested creates or
	// child/membership actions that are 🟢 today and have NEVER staged:
	// registered anyway, because the route walk's guess (stagedTargetByRoute)
	// is the one this table replaces family by family, and for createOffer
	// that guess is provably wrong today (gradionhq/margince-poc-v1#1046).
	// upsertPartner is the one exception: it stages TODAY, whenever
	// splitOrRedeemUpdate's per-field probe finds a human-owned conflict
	// (agentsplit.go's actionShapedUpdateOps — upsertPartner is deliberately
	// NOT a member — explains why), so this entry is already load-bearing,
	// not merely future-proofing. Five of the seven share their operationId
	// constant with agentsplit.go's own (opAddListMember's own comment says
	// why); upsertPartner shares the CONSTANT with agentsplit.go too (its
	// restCommands entry and its exclusion from actionShapedUpdateOps must
	// name the identical operationId), though it is not a MEMBER of that
	// map; createOffer has no such twin at all, since create_record never
	// reaches the split.
	opAddListMember:       addListMemberCommand,
	opApplyTag:            applyTagCommand,
	opAddOfferLineItem:    addOfferLineItemCommand,
	opUpdateOfferLineItem: updateOfferLineItemCommand,
	opRemoveOfferLineItem: removeOfferLineItemCommand,
	"createOffer":         createOfferCommand,
	opUpsertPartner:       upsertPartnerCommand,
}

// archiveCommand decodes one DELETE /v1/<collection>/{id} into the archive
// command.
//
// The record type is read off the route's own policy entry rather than written
// here a second time: the entry is generated from the contract's x-mcp-tool
// annotation, so a type spelled again in this file could disagree with the one
// the gate admitted against.
//
//nolint:ireturn // a decoder's whole product is the erased command-and-resolver pair the table above is typed by
func archiveCommand(pol agentPolicy, deps restCommandDeps, r *http.Request, _ []byte) (agents.GovernedCall, error) {
	id, err := routedID(r)
	if err != nil {
		return nil, err
	}
	return agents.NewArchiveCall(deps.records, agents.ArchiveCommand{
		RecordType: string(pol.RecordType),
		ID:         id,
	}), nil
}

// createCommand decodes one POST /v1/<collection> into the create command.
// The REST body IS the record's fields — unlike create_record's own tool
// arguments, there is no {record_type, fields} envelope to unwrap, because
// the route already names the type.
//
// body is the buffered copy stageRefusal already hashed into
// canonicalRESTCall (agentgatestaging.go), not a second read of r.Body: a
// stream has one honest reading, and the gate already took it.
//
//nolint:ireturn,unparam // ireturn: a decoder's whole product is the erased command-and-resolver pair the table above is typed by. unparam: the error is always nil TODAY (a create has no id to fail parsing), but every restCommands entry shares this signature, and archiveCommand/patchCommand both use theirs
func createCommand(pol agentPolicy, _ restCommandDeps, _ *http.Request, body []byte) (agents.GovernedCall, error) {
	return agents.NewCreateCall(agents.CreateCommand{
		RecordType: string(pol.RecordType),
		Fields:     json.RawMessage(body),
	}), nil
}

// patchCommand decodes one PATCH /v1/<collection>/{id} into the patch
// command, the same existence-hiding answer to a malformed id as
// archiveCommand, and the same buffered body as createCommand.
//
//nolint:ireturn // a decoder's whole product is the erased command-and-resolver pair the table above is typed by
func patchCommand(pol agentPolicy, deps restCommandDeps, r *http.Request, body []byte) (agents.GovernedCall, error) {
	id, err := routedID(r)
	if err != nil {
		return nil, err
	}
	return agents.NewPatchCall(deps.records, agents.PatchCommand{
		RecordType: string(pol.RecordType),
		ID:         id,
		Fields:     json.RawMessage(body),
	}), nil
}
