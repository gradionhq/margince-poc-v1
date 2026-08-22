// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package dealrooms

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/provenance"
)

// createInput maps the create request onto the store's input, refusing what the
// store should never have to re-check.
func createInput(req crmcontracts.CreateDealRoomRequest) (CreateRoomInput, error) {
	if req.Title == "" {
		return CreateRoomInput{}, &fieldError{field: columnTitle, code: "required", msg: "title is required"}
	}
	if err := provenance.Refuse("source", req.Source); err != nil {
		return CreateRoomInput{}, err
	}
	if err := httperr.RequireBodyID("deal_id", ids.UUID(req.DealId)); err != nil {
		return CreateRoomInput{}, err
	}
	in := CreateRoomInput{
		DealID:         ids.From[ids.DealKind](ids.UUID(req.DealId)),
		Title:          req.Title,
		WelcomeMessage: req.WelcomeMessage,
		ExpiresAt:      req.ExpiresAt,
		Source:         req.Source,
	}
	if req.StewardUserId != nil {
		u := ids.From[ids.UserKind](ids.UUID(*req.StewardUserId))
		in.StewardUserID = &u
	}
	return in, nil
}

// updateInput maps the patch request. The double pointers carry the difference
// between "leave this alone" (outer nil) and "clear it" (inner nil), which a
// single pointer cannot express and which matters for every nullable column
// here — a steward and an expiry are both legitimately set back to nothing.
func updateInput(req crmcontracts.UpdateDealRoomRequest, ifVersion *int64) UpdateRoomInput {
	in := UpdateRoomInput{Title: req.Title, IfVersion: ifVersion}
	if req.WelcomeMessage != nil {
		in.WelcomeMessage = &req.WelcomeMessage
	}
	if req.StewardUserId != nil {
		u := ids.From[ids.UserKind](ids.UUID(*req.StewardUserId))
		p := &u
		in.StewardUserID = &p
	}
	return in
}

// listInput maps the list query parameters.
func listInput(params crmcontracts.ListDealRoomsParams) ListRoomsInput {
	in := ListRoomsInput{
		Limit:  limitArg(params.Limit),
		Cursor: cursorArg(params.Cursor),
	}
	if params.IncludeArchived != nil {
		in.IncludeArchived = bool(*params.IncludeArchived)
	}
	if params.DealId != nil {
		d := ids.From[ids.DealKind](ids.UUID(*params.DealId))
		in.DealID = &d
	}
	if params.State != nil {
		s := string(*params.State)
		in.State = &s
	}
	return in
}

func limitArg(l *crmcontracts.Limit) *int {
	if l == nil {
		return nil
	}
	v := int(*l)
	return &v
}

func cursorArg(c *crmcontracts.Cursor) *string {
	if c == nil {
		return nil
	}
	v := string(*c)
	return &v
}

func pageInfo(p storekit.Page) crmcontracts.PageInfo {
	out := crmcontracts.PageInfo{HasMore: p.HasMore}
	if p.NextCursor != "" {
		out.NextCursor = &p.NextCursor
	}
	return out
}

// publishNote reads the optional release note. The body itself is optional, so
// an absent one is not an error — but a malformed one is, because silently
// discarding a note the caller wrote would lose their words without saying so.
func publishNote(w http.ResponseWriter, r *http.Request) (*string, bool) {
	var req crmcontracts.PublishDealRoomRequest
	err := json.NewDecoder(r.Body).Decode(&req)
	if errors.Is(err, io.EOF) {
		return nil, true
	}
	if err != nil {
		httperr.Write(w, r, &fieldError{
			field: "release_note",
			code:  "malformed_body",
			msg:   "the publish body could not be read as JSON; send no body to publish without a note",
		})
		return nil, false
	}
	return req.ReleaseNote, true
}
