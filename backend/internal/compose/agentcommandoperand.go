// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The REST door's half of the eight commands whose operand is a SECOND path
// parameter or a second path segment, not the route's own {id}
// (gradionhq/margince-poc-v1#928 task 5): an organization fact or profile
// field, a custom field's retire/options actions, and a project stakeholder.
// The decoding shape is the same one archiveCommand/patchCommand set in
// agentcommand.go — parse {id} as the existence-hiding 404 (routedID, shared
// with them), decode the rest, hand the typed command to its resolver
// (modules/agents/commandsidecar.go, commandaction.go) — split out here
// because the family carries a SECOND path operand those two never had to.

import (
	"net/http"

	chi "github.com/go-chi/chi/v5"

	"github.com/gradionhq/margince/backend/internal/modules/agents"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// pathOperand reads a required path parameter beyond the route's own {id}.
// chi has already matched the route by the time a handler runs, so an empty
// value means the parameter was never bound — a request built by hand
// (tests) or a future route missing the segment — and answers 422 naming the
// parameter, not a panic on an empty FactKey/Field later.
func pathOperand(r *http.Request, name string) (string, error) {
	v := chi.URLParam(r, name)
	if v == "" {
		return "", httperr.Validation(name, "missing", name+" is required")
	}
	return v, nil
}

// routedID parses the route's own {id}, the existence-hiding answer every
// restCommands decoder gives a malformed one: "that is not a uuid" and
// "there is no such row" must read alike, or the shape of a caller's id
// tells them which rows exist.
func routedID(r *http.Request) (ids.UUID, error) {
	id, err := ids.Parse(chi.URLParam(r, "id"))
	if err != nil {
		return ids.UUID{}, apperrors.ErrNotFound
	}
	return id, nil
}

//nolint:ireturn // a decoder's whole product is the erased command-and-resolver pair restCommands is typed by
func confirmFactCommand(_ agentPolicy, deps restCommandDeps, r *http.Request, _ []byte) (agents.GovernedCall, error) {
	id, err := routedID(r)
	if err != nil {
		return nil, err
	}
	factKey, err := pathOperand(r, "factKey")
	if err != nil {
		return nil, err
	}
	return agents.NewConfirmFactCall(deps.records, agents.ConfirmFactCommand{ID: id, FactKey: factKey}), nil
}

//nolint:ireturn // a decoder's whole product is the erased command-and-resolver pair restCommands is typed by
func updateFactCommand(_ agentPolicy, deps restCommandDeps, r *http.Request, _ []byte) (agents.GovernedCall, error) {
	id, err := routedID(r)
	if err != nil {
		return nil, err
	}
	factKey, err := pathOperand(r, "factKey")
	if err != nil {
		return nil, err
	}
	return agents.NewUpdateFactCall(deps.records, agents.UpdateFactCommand{ID: id, FactKey: factKey}), nil
}

//nolint:ireturn // a decoder's whole product is the erased command-and-resolver pair restCommands is typed by
func confirmProfileFieldCommand(_ agentPolicy, deps restCommandDeps, r *http.Request, _ []byte) (agents.GovernedCall, error) {
	id, err := routedID(r)
	if err != nil {
		return nil, err
	}
	field, err := pathOperand(r, "field")
	if err != nil {
		return nil, err
	}
	return agents.NewConfirmProfileFieldCall(deps.records, agents.ConfirmProfileFieldCommand{ID: id, Field: field}), nil
}

//nolint:ireturn // a decoder's whole product is the erased command-and-resolver pair restCommands is typed by
func updateProfileFieldCommand(_ agentPolicy, deps restCommandDeps, r *http.Request, _ []byte) (agents.GovernedCall, error) {
	id, err := routedID(r)
	if err != nil {
		return nil, err
	}
	field, err := pathOperand(r, "field")
	if err != nil {
		return nil, err
	}
	return agents.NewUpdateProfileFieldCall(deps.records, agents.UpdateProfileFieldCommand{ID: id, Field: field}), nil
}

//nolint:ireturn // a decoder's whole product is the erased command-and-resolver pair restCommands is typed by
func retireCustomFieldCommand(_ agentPolicy, deps restCommandDeps, r *http.Request, _ []byte) (agents.GovernedCall, error) {
	id, err := routedID(r)
	if err != nil {
		return nil, err
	}
	return agents.NewRetireCustomFieldCall(deps.records, agents.RetireCustomFieldCommand{ID: id}), nil
}

//nolint:ireturn // a decoder's whole product is the erased command-and-resolver pair restCommands is typed by
func updateCustomFieldOptionsCommand(_ agentPolicy, deps restCommandDeps, r *http.Request, _ []byte) (agents.GovernedCall, error) {
	id, err := routedID(r)
	if err != nil {
		return nil, err
	}
	return agents.NewUpdateCustomFieldOptionsCall(deps.records, agents.UpdateCustomFieldOptionsCommand{ID: id}), nil
}

//nolint:ireturn // a decoder's whole product is the erased command-and-resolver pair restCommands is typed by
func setStakeholderCommand(_ agentPolicy, deps restCommandDeps, r *http.Request, _ []byte) (agents.GovernedCall, error) {
	id, err := routedID(r)
	if err != nil {
		return nil, err
	}
	return agents.NewSetStakeholderCall(deps.records, agents.SetStakeholderCommand{ID: id}), nil
}

// removeStakeholderCommand decodes DELETE /v1/projects/{id}/stakeholders/{person_id}.
// person_id fails as 422, not 404: unlike the routed {id}, a malformed or
// missing person_id names no row this door hides the existence of — it
// names which edge the caller meant, a shape the caller simply got wrong.
// Composed from the same pathOperand every other second-operand decoder
// uses (a missing segment answers "missing") plus ids.Parse for the shape
// check the others don't need (a non-empty but malformed one answers
// "invalid") — one spelling of "required", not a second copy of it.
//
//nolint:ireturn // a decoder's whole product is the erased command-and-resolver pair restCommands is typed by
func removeStakeholderCommand(_ agentPolicy, deps restCommandDeps, r *http.Request, _ []byte) (agents.GovernedCall, error) {
	id, err := routedID(r)
	if err != nil {
		return nil, err
	}
	raw, err := pathOperand(r, "person_id")
	if err != nil {
		return nil, err
	}
	personID, perr := ids.Parse(raw)
	if perr != nil {
		return nil, httperr.Validation("person_id", "invalid", "person_id must be a uuid")
	}
	return agents.NewRemoveStakeholderCall(deps.records, agents.RemoveStakeholderCommand{ID: id, PersonID: personID}), nil
}
