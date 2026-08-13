// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The REST door's half of the seven bespoke auto-execute commands
// (gradionhq/margince-poc-v1#928 task 6): a list member, a tag apply, an
// offer line item add/update/remove, an offer created under a parent deal,
// and a partner upsert. Unlike agentcommandoperand.go's confirm-first
// family, none of these carries body-derived VALUES into its command — the
// body is the executor's own concern, never something Subject or Guards
// reads (modules/agents/commandnested.go's own doc says why per command) —
// so decoding is routedID, plus the offer line items' own {lineItemId},
// handed straight to the resolver.

import (
	"encoding/json"
	"net/http"

	"github.com/gradionhq/margince/backend/internal/modules/agents"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

//nolint:ireturn // a decoder's whole product is the erased command-and-resolver pair restCommands is typed by
func addListMemberCommand(_ agentPolicy, deps restCommandDeps, r *http.Request, _ []byte) (agents.GovernedCall, error) {
	id, err := routedID(r)
	if err != nil {
		return nil, err
	}
	return agents.NewAddListMemberCall(deps.records, agents.AddListMemberCommand{ID: id}), nil
}

//nolint:ireturn // a decoder's whole product is the erased command-and-resolver pair restCommands is typed by
func applyTagCommand(_ agentPolicy, deps restCommandDeps, r *http.Request, _ []byte) (agents.GovernedCall, error) {
	id, err := routedID(r)
	if err != nil {
		return nil, err
	}
	return agents.NewApplyTagCall(deps.records, agents.ApplyTagCommand{ID: id}), nil
}

//nolint:ireturn // a decoder's whole product is the erased command-and-resolver pair restCommands is typed by
func addOfferLineItemCommand(_ agentPolicy, deps restCommandDeps, r *http.Request, _ []byte) (agents.GovernedCall, error) {
	id, err := routedID(r)
	if err != nil {
		return nil, err
	}
	return agents.NewAddOfferLineItemCall(deps.records, agents.AddOfferLineItemCommand{ID: id}), nil
}

// lineItemID reads the offer line item's {lineItemId} path parameter — the
// route's SECOND parameter, beyond the offer's own {id} — through the same
// pathOperand + ids.Parse composition removeStakeholderCommand uses for
// person_id (agentcommandoperand.go): a missing segment answers 422
// "missing", a non-empty malformed one answers 422 "invalid". Neither is
// routedID's existence-hiding 404 — a line item's shape being wrong is the
// caller's mistake, not a fact about whether the offer exists.
func lineItemID(r *http.Request) (ids.UUID, error) {
	raw, err := pathOperand(r, "lineItemId")
	if err != nil {
		return ids.UUID{}, err
	}
	id, perr := ids.Parse(raw)
	if perr != nil {
		return ids.UUID{}, httperr.Validation("lineItemId", "invalid", "lineItemId must be a uuid")
	}
	return id, nil
}

//nolint:ireturn // a decoder's whole product is the erased command-and-resolver pair restCommands is typed by
func updateOfferLineItemCommand(_ agentPolicy, deps restCommandDeps, r *http.Request, _ []byte) (agents.GovernedCall, error) {
	id, err := routedID(r)
	if err != nil {
		return nil, err
	}
	itemID, err := lineItemID(r)
	if err != nil {
		return nil, err
	}
	return agents.NewUpdateOfferLineItemCall(deps.records, agents.UpdateOfferLineItemCommand{ID: id, LineItemID: itemID}), nil
}

//nolint:ireturn // a decoder's whole product is the erased command-and-resolver pair restCommands is typed by
func removeOfferLineItemCommand(_ agentPolicy, deps restCommandDeps, r *http.Request, _ []byte) (agents.GovernedCall, error) {
	id, err := routedID(r)
	if err != nil {
		return nil, err
	}
	itemID, err := lineItemID(r)
	if err != nil {
		return nil, err
	}
	return agents.NewRemoveOfferLineItemCall(deps.records, agents.RemoveOfferLineItemCommand{ID: id, LineItemID: itemID}), nil
}

// createOfferCommand decodes POST /v1/deals/{id}/offers. The route's own
// {id} is the DEAL the offer nests under (gradionhq/margince-poc-v1#1046),
// not an offer id — there is no offer id to decode, since the offer does
// not exist yet. body is the same buffered copy every other create/patch
// decoder reuses (createCommand's own comment says why).
//
//nolint:ireturn // a decoder's whole product is the erased command-and-resolver pair restCommands is typed by
func createOfferCommand(_ agentPolicy, deps restCommandDeps, r *http.Request, body []byte) (agents.GovernedCall, error) {
	dealID, err := routedID(r)
	if err != nil {
		return nil, err
	}
	return agents.NewCreateOfferCall(deps.records, agents.CreateOfferCommand{
		DealID: dealID,
		Fields: json.RawMessage(body),
	}), nil
}

//nolint:ireturn // a decoder's whole product is the erased command-and-resolver pair restCommands is typed by
func upsertPartnerCommand(_ agentPolicy, deps restCommandDeps, r *http.Request, _ []byte) (agents.GovernedCall, error) {
	id, err := routedID(r)
	if err != nil {
		return nil, err
	}
	return agents.NewUpsertPartnerCall(deps.records, agents.UpsertPartnerCommand{ID: id}), nil
}
