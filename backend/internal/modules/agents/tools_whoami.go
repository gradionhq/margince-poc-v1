// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package agents

// whoami (🟢 read): the human this passport acts for.
//
// The surface knew and did not say. Every write carries on_behalf_of, and the
// audit log records it, but nothing published it — so an assistant could not
// say "I'll assign this to you", could not filter "my deals" without asking,
// and could not set owner_id on a record it was creating even though the field
// is accepted. It also could not know which language to write stored prose in,
// which is how a German sentence ended up in an English workspace's company
// description.
//
// A tool rather than a field on the capabilities resource: that document is
// shape-versioned and cached, while identity is per-call and must never be
// served from a cache belonging to a different passport.

import (
	"context"
	"encoding/json"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/mcp"
)

// ActingIdentity is the human a call is made on behalf of.
type ActingIdentity struct {
	UserID      ids.UUID
	DisplayName string
	Email       string
	// Locale is the language this person chose, empty when they never did.
	// The caller decides what to do with empty rather than being handed a
	// default that cannot be told apart from a choice.
	Locale   string
	Timezone string
}

// IdentityReader answers who the call acts for. Declared here and implemented
// in compose, so this module never imports identity.
type IdentityReader func(ctx context.Context) (ActingIdentity, error)

// RegisterWhoamiTool joins whoami to the surface. A nil reader registers
// nothing — the same conditional registration the other injected-seam tools
// take.
func RegisterWhoamiTool(r *Registry, read IdentityReader) {
	if read == nil {
		return
	}
	r.Register(whoami{read: read})
}

type whoami struct{ read IdentityReader }

func (t whoami) Spec() mcp.ToolSpec {
	return mcp.ToolSpec{
		Name: "whoami", Title: "Who this passport acts for", Version: toolVersionV1,
		Description:   whoamiCopy.render(),
		RequiredScope: principal.ScopeRead, Tier: mcp.TierAutoExecute,
		InputSchema:  schema(`{"type":"object","properties":{},"additionalProperties":false}`),
		OutputSchema: schemaFor[WhoamiResult](),
	}
}

func (t whoami) Handle(ctx context.Context, _ json.RawMessage) (json.RawMessage, error) {
	who, err := t.read(ctx)
	if err != nil {
		return nil, err
	}
	// No noteEvidence: the acting user is not a record this answer rests on,
	// it is who is asking. Stamping it would put the caller's own seat in the
	// evidence list of every call that begins by asking who they are.
	return json.Marshal(WhoamiResult{
		ActingUserID: who.UserID,
		DisplayName:  who.DisplayName,
		Email:        who.Email,
		Locale:       who.Locale,
		Timezone:     who.Timezone,
	})
}
