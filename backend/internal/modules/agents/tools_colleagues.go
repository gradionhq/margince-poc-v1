// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package agents

// list_colleagues (🟢 read): who works here, as opposed to who we sell to.
//
// The surface had no way to name a colleague. app_user appeared in none of the
// tools, so an assistant asked to assign work searched `person` — the customer
// contacts — found two people with the right first name, and reported that
// neither was "listed under sales". The seat it wanted was the one the human
// was signed in as.
//
// That also blocked everything downstream: assignee_id and owner_id take an
// app_user id, and nothing could produce one.
//
// NOT a record type. A seat has no owner, no visibility rule and no custom
// fields, and datasource.EntityTypes is pinned to the schema's CHECK
// constraints — widening it for this would ripple through every polymorphic
// reference to say "a colleague is a kind of record", which is exactly the
// confusion this tool exists to end.

import (
	"context"
	"encoding/json"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/mcp"
)

// Colleague is one workspace seat.
type Colleague struct {
	UserID      ids.UUID `json:"user_id"`
	DisplayName string   `json:"display_name"`
	Email       string   `json:"email"`
	SeatType    string   `json:"seat_type"`
	IsAgent     bool     `json:"is_agent"`
}

// ColleagueLister answers the roster. Declared here, implemented in compose,
// so this module never imports identity.
type ColleagueLister func(ctx context.Context, q string) (colleagues []Colleague, truncated bool, err error)

// RegisterColleaguesTool joins list_colleagues to the surface; a nil lister
// registers nothing.
func RegisterColleaguesTool(r *Registry, list ColleagueLister) {
	if list == nil {
		return
	}
	r.Register(listColleagues{list: list})
}

type listColleagues struct{ list ColleagueLister }

func (t listColleagues) Spec() mcp.ToolSpec {
	return mcp.ToolSpec{
		Name: "list_colleagues", Title: "List colleagues", Version: toolVersionV1,
		Description:   listColleaguesCopy.render(),
		RequiredScope: principal.ScopeRead, Tier: mcp.TierAutoExecute,
		OpenAPIOp: "listUsers",
		InputSchema: schema(`{"type":"object","properties":{
			"q":{"type":"string","description":"Narrow by name or email; omit for the whole roster"}},
			"additionalProperties":false}`),
		OutputSchema: schemaFor[ListColleaguesResult](),
	}
}

func (t listColleagues) Handle(ctx context.Context, in json.RawMessage) (json.RawMessage, error) {
	var args struct {
		Q string `json:"q"`
	}
	if err := decodeArgs(in, &args); err != nil {
		return nil, err
	}
	colleagues, truncated, err := t.list(ctx, args.Q)
	if err != nil {
		return nil, err
	}
	// No noteEvidence: a seat is not a record this answer rests on. Stamping
	// one would put a colleague in the evidence list of every call that begins
	// by asking who could do the work.
	if colleagues == nil {
		colleagues = []Colleague{}
	}
	return json.Marshal(ListColleaguesResult{Colleagues: colleagues, Truncated: truncated})
}
