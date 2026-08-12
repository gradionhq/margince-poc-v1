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

	chi "github.com/go-chi/chi/v5"

	"github.com/gradionhq/margince/backend/internal/modules/agents"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
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
// Six of the thirteen create routes are deliberately ABSENT even though
// create_record is their governing tool: custom_field, list, offer_template,
// product, saved_view and tag create through their own module's handler, never
// through create_record's own datasource-provider write path, so
// createResolver.Guards' "does this verb serve this record type" refusal
// (command.go) does not describe them — it would hard-refuse a create that
// works fine, where stagedTargetByRoute already stages the correct shape
// (their type, and no id, since none of these routes carries one). Patch has
// no equivalent gap: patchResolver.Guards has no such refusal, so every
// update_record patch is registered.
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

	"createDeal":         createCommand,
	"createLead":         createCommand,
	"createOrganization": createCommand,
	"createPerson":       createCommand,
	"createProject":      createCommand,
	"createRelationship": createCommand,

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
	id, err := ids.Parse(chi.URLParam(r, "id"))
	if err != nil {
		// Existence-hiding, and the answer this door already gave: "that is not
		// a uuid" and "there is no such row" must read alike, or the shape of a
		// caller's id tells them which rows exist.
		return nil, apperrors.ErrNotFound
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
	id, err := ids.Parse(chi.URLParam(r, "id"))
	if err != nil {
		return nil, apperrors.ErrNotFound
	}
	return agents.NewPatchCall(deps.records, agents.PatchCommand{
		RecordType: string(pol.RecordType),
		ID:         id,
		Fields:     json.RawMessage(body),
	}), nil
}
