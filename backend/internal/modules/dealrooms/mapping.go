// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package dealrooms

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/provenance"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/values"
)

// createInput maps the create request onto the store's input, refusing what the
// store should never have to re-check.
func createInput(req crmcontracts.CreateDealRoomRequest) (CreateRoomInput, error) {
	if req.Title == "" {
		return CreateRoomInput{}, &fieldError{field: columnTitle, code: codeRequired, msg: "title is required"}
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

// inviteInput maps the invite request, defaulting the capability to the least
// that lets somebody read the room.
func inviteInput(req crmcontracts.InviteDealRoomParticipantRequest) (InviteInput, error) {
	if err := provenance.Refuse("source", req.Source); err != nil {
		return InviteInput{}, err
	}
	// Validated HERE rather than trusted from the binding: openapi_types.Email
	// is a bare string alias with no unmarshal check, so a malformed address
	// would otherwise take the one live seat its room allows for that address,
	// fail every send, and — once somebody has signed in — become uncorrectable.
	// The identity module validates its own credential-bearing invite the same
	// way and for the same reason.
	email, err := values.ParseEmail(string(req.Email))
	if err != nil {
		return InviteInput{}, err
	}
	if err := refuseOverlongName(req.FullName); err != nil {
		return InviteInput{}, err
	}
	in := InviteInput{
		FullName:   req.FullName,
		Email:      email.String(),
		Capability: capabilityView,
		Source:     req.Source,
	}
	if req.Capability != nil {
		if err := refuseUnknownCapability(*req.Capability); err != nil {
			return InviteInput{}, err
		}
		in.Capability = *req.Capability
	}
	return in, nil
}

// participantUpdateInput maps the correction request. Validation of the
// capability happens in the store rather than here, because an omitted field and
// an invalid one are different answers and only the store sees which arrived.
func participantUpdateInput(req crmcontracts.UpdateDealRoomParticipantRequest) (UpdateParticipantInput, error) {
	in := UpdateParticipantInput{
		FullName:   req.FullName,
		Capability: req.Capability,
	}
	if req.FullName != nil {
		if err := refuseOverlongName(*req.FullName); err != nil {
			return UpdateParticipantInput{}, err
		}
	}
	if req.Email != nil {
		// A correction is exactly where a malformed address does the most harm,
		// so it takes the same validator the invite does.
		email, err := values.ParseEmail(string(*req.Email))
		if err != nil {
			return UpdateParticipantInput{}, err
		}
		normalized := email.String()
		in.Email = &normalized
	}
	return in, nil
}

// nameLimit bounds a participant's display name, matching what the member
// invite allows. Unbounded, it is a row somebody else has to read.
const nameLimit = 255

func refuseOverlongName(name string) error {
	if strings.TrimSpace(name) == "" {
		return &fieldError{field: "full_name", code: codeRequired, msg: "full_name is required"}
	}
	if len([]rune(name)) > nameLimit {
		return &fieldError{
			field: "full_name",
			code:  codeTooLong,
			msg:   "full_name is longer than 255 characters",
		}
	}
	return nil
}

// The capabilities a participant may hold, spelled here because the contract
// carries them as a plain string — an inline enum would generate package-scope
// Go constants named View, Comment and Reviewer in the shared contracts package
// and silently rename any other schema declaring the same values.
const (
	capabilityView     = "view"
	capabilityComment  = "comment"
	capabilityReviewer = "reviewer"
)

// refuseUnknownCapability names the closed set rather than letting the schema
// CHECK answer: a constraint violation surfaces as a 500 with a table name in
// it, and the caller learns nothing about which values are legal.
func refuseUnknownCapability(capability string) error {
	switch capability {
	case capabilityView, capabilityComment, capabilityReviewer:
		return nil
	}
	return &fieldError{
		field: fieldCapability,
		code:  "unknown_capability",
		msg:   "capability must be view, comment or reviewer",
	}
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
	if params.ParticipantEmail != nil {
		email := strings.ToLower(strings.TrimSpace(string(*params.ParticipantEmail)))
		in.ParticipantEmail = &email
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
