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
	"encoding/json"
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

// setStakeholderCommand decodes PUT /v1/projects/{id}/stakeholders, whose
// person_id arrives in the BODY where its DELETE twin carries it in the path.
//
// The shape check is the same one removeStakeholderCommand makes for its own
// operand, and for the same reason: the request the approval stages IS the
// request its redemption replays, so a person_id that names no person is a call
// the handler refuses AFTER a human's one-shot approval has been consumed. 422
// rather than the routed id's 404 — like the path operand, person_id says WHICH
// edge, not whether the project exists.
//
// It is checked here rather than in the resolver because there is nothing for
// the command to carry it as: neither Guards nor Subject reads the attached
// person (setStakeholderResolver's own doc says why), and a field with no
// reader documents no obligation.
//
//nolint:ireturn // a decoder's whole product is the erased command-and-resolver pair restCommands is typed by
func setStakeholderCommand(_ agentPolicy, deps restCommandDeps, r *http.Request, body []byte) (agents.GovernedCall, error) {
	id, err := routedID(r)
	if err != nil {
		return nil, err
	}
	if err := requireStakeholderPerson(body); err != nil {
		return nil, err
	}
	return agents.NewSetStakeholderCall(deps.records, agents.SetStakeholderCommand{ID: id}), nil
}

// requireStakeholderPerson holds the body to the one member the attach cannot
// run without. crm.yaml's SetProjectStakeholderRequest requires person_id as a
// uuid; a body that is not even an object is answered as the same missing
// person_id, since neither carries one.
func requireStakeholderPerson(body []byte) error {
	var payload struct {
		PersonID string `json:"person_id"`
	}
	if err := json.Unmarshal(body, &payload); err != nil || payload.PersonID == "" {
		return httperr.Validation("person_id", "missing", "person_id is required")
	}
	if _, err := ids.Parse(payload.PersonID); err != nil {
		return httperr.Validation("person_id", "invalid", "person_id must be a uuid")
	}
	return nil
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

// createRoomItemCommand decodes POST /v1/deal-rooms/{id}/documents and the
// thread-opening and reply POSTs through the same code: the routed room is
// folded into the body so the staged approval names where the item lands, and
// the policy's record type says which kind of item it is.
//
// The routed {id} names the ROOM, and the record being created is the item, so
// this cannot be the plain createCommand: that one carries only the body, and an
// approval of it would say nothing about which room the item lands in. The room
// travels in the fields, where the staged proposed_change shows it beside the
// wording a human is being asked to release.
//
//nolint:ireturn // a decoder's whole product is the erased command-and-resolver pair restCommands is typed by
func createRoomItemCommand(pol agentPolicy, _ restCommandDeps, r *http.Request, body []byte) (agents.GovernedCall, error) {
	roomID, err := routedID(r)
	if err != nil {
		return nil, err
	}
	fields, err := withRoomID(body, roomID)
	if err != nil {
		return nil, err
	}
	return agents.NewCreateCall(agents.CreateCommand{
		RecordType: string(pol.RecordType),
		Fields:     fields,
	}), nil
}

// updateDealRoomDocumentCommand decodes PATCH
// /v1/deal-rooms/{id}/documents/{documentId}. It is a roomItemPatch, which
// binds to the ITEM rather than the routed {id}:
// patchCommand would bind to the room, so the approval a human released would
// name a different record than the one the call goes on to write — and the
// item id it did carry would never be checked at all.
var updateDealRoomDocumentCommand = roomItemPatch("documentId")

// roomItemPatch is the decoder for a PATCH on one item under a room, keyed by
// the path parameter that names the item.
func roomItemPatch(param string) func(agentPolicy, restCommandDeps, *http.Request, []byte) (agents.GovernedCall, error) {
	return func(pol agentPolicy, deps restCommandDeps, r *http.Request, body []byte) (agents.GovernedCall, error) {
		return roomItemPatchCommand(pol, deps, r, body, param)
	}
}

//nolint:ireturn // a decoder's whole product is the erased command-and-resolver pair restCommands is typed by
func roomItemPatchCommand(pol agentPolicy, deps restCommandDeps, r *http.Request, body []byte, param string) (agents.GovernedCall, error) {
	if _, err := routedID(r); err != nil {
		return nil, err
	}
	raw, err := pathOperand(r, param)
	if err != nil {
		return nil, err
	}
	// The existence-hiding 404 routedID gives, not the 422 person_id gives.
	// The item id names a ROW rather than an edge, so "that is not a uuid" and
	// "there is no such item" must read alike, or the shape of a caller's id
	// tells them which of a room's items exist. It is also not a contract field,
	// so it has no name a validation fault could legitimately publish.
	itemID, perr := ids.Parse(raw)
	if perr != nil {
		return nil, apperrors.ErrNotFound
	}
	return agents.NewPatchCall(deps.records, agents.PatchCommand{
		RecordType: string(pol.RecordType),
		ID:         itemID,
		Fields:     json.RawMessage(body),
	}), nil
}

// withRoomID folds the routed room into a create body so the staged approval
// names where the item lands. A body that is not an object is refused here
// rather than reaching the handler, because a create whose fields cannot carry
// the room would stage an item belonging to no room at all.
func withRoomID(body []byte, roomID ids.UUID) (json.RawMessage, error) {
	fields := map[string]json.RawMessage{}
	if len(body) > 0 {
		if err := json.Unmarshal(body, &fields); err != nil {
			return nil, httperr.Validation("body", "invalid", "the request body must be a JSON object")
		}
	}
	encoded, err := json.Marshal(roomID)
	if err != nil {
		return nil, httperr.Validation("id", "invalid", "the room in the path is not a uuid")
	}
	fields["room_id"] = encoded
	merged, err := json.Marshal(fields)
	if err != nil {
		return nil, httperr.Validation("body", "invalid", "the request body could not be read as fields")
	}
	return merged, nil
}
