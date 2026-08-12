// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The REST door's half of the governance seam (modules/agents/command.go):
// what turns an HTTP request into the operation's typed command.
//
// The door decodes; it does not interpret. Everything the gate must know about
// the call it decoded — what the approval binds to, what would be refused
// anyway — comes back from the resolver the command is bound to, which is the
// same resolver the tool door reaches for the same operation.

import (
	"net/http"

	chi "github.com/go-chi/chi/v5"

	"github.com/gradionhq/margince/backend/internal/modules/agents"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
)

// restCommands maps a crm.yaml operationId to the decoder that turns an HTTP
// request into the operation's typed command, bound to the resolver that
// speaks it.
//
// The table is deliberately incomplete: an operation with no entry still
// resolves its staged target by walking the route (stagedTargetByRoute), which
// is the guess this seam replaces one operation family at a time. The walk is
// what goes away as the entries arrive, and the fail-closed refusal
// dynamicTierInputs already takes for the tier question is what replaces it.
var restCommands = map[string]func(pol agentPolicy, records datasource.SystemOfRecordProvider, r *http.Request) (agents.GovernedCall, error){
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
func archiveCommand(pol agentPolicy, records datasource.SystemOfRecordProvider, r *http.Request) (agents.GovernedCall, error) {
	id, err := ids.Parse(chi.URLParam(r, "id"))
	if err != nil {
		// Existence-hiding, and the answer this door already gave: "that is not
		// a uuid" and "there is no such row" must read alike, or the shape of a
		// caller's id tells them which rows exist.
		return nil, apperrors.ErrNotFound
	}
	return agents.Bind(agents.NewArchiveResolver(records), agents.ArchiveCommand{
		RecordType: string(pol.RecordType),
		ID:         id,
	}), nil
}
