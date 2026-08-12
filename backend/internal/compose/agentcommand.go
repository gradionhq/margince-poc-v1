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
var restCommands = map[string]func(pol agentPolicy, deps restCommandDeps, r *http.Request) (agents.GovernedCall, error){
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
func archiveCommand(pol agentPolicy, deps restCommandDeps, r *http.Request) (agents.GovernedCall, error) {
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
