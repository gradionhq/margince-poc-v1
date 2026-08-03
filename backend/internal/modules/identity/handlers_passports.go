// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package identity

// The three /passports operations, split out of handlers.go when it outgrew the
// file cap: mint, list, revoke — one credential's whole HTTP lifecycle, and the
// only handlers in this module that speak about passports rather than sessions.

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// IssuePassport implements (POST /passports): the session user mints an
// agent bearer token bound to their OWN identity — on_behalf_of is never
// a request field, so a passport cannot outreach its issuer by
// construction.
func (h Handlers) IssuePassport(w http.ResponseWriter, r *http.Request) {
	id, ok := identityFrom(r.Context())
	if !ok {
		httperr.Unauthorized(w, r, "passports are minted by a signed-in human, not an agent")
		return
	}
	var req crmcontracts.IssuePassportRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httperr.Write(w, r, httperr.Validation("body", "malformed_json", err.Error()))
		return
	}

	in := IssuePassportInput{Label: req.Label}
	for _, sc := range req.Scopes {
		in.Scopes = append(in.Scopes, string(sc))
	}
	if req.TtlHours != nil {
		ttl := time.Duration(*req.TtlHours) * time.Hour
		in.TTL = &ttl
	}

	issued, err := h.svc.IssuePassport(r.Context(), id, in)
	if err != nil {
		var badScope *InvalidScopeError
		if errors.As(err, &badScope) {
			httperr.Write(w, r, httperr.Validation("scopes", "invalid_scope", badScope.Error()))
			return
		}
		httperr.Write(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusCreated, crmcontracts.IssuePassportResponse{
		PassportId: openapi_types.UUID(issued.ID.UUID),
		Token:      issued.Token,
		Scopes:     issued.Scopes,
		OnBehalfOf: openapi_types.UUID(id.UserID.UUID),
		ExpiresAt:  issued.ExpiresAt,
	})
}

// ListPassports implements (GET /passports): passport metadata for the
// Settings list. Tokens are never re-disclosed. Two contract fields answer as
// absent because nothing stores them: agent_id has no storage at all (the
// A1/local path has no agent-connection table), and last_used_at has a column
// that nothing writes yet — its debounced stamp on the authenticated /mcp path
// arrives with the per-workspace admin surface.
//
// `connection` is the field that tells the two kinds of row apart — a passport
// the human minted from the credential a client received after they lent one —
// and it is absent, not empty, on the former. Settings reads its presence
// rather than the `oauth:` label prefix, which is display text a human could
// equally type.
func (h Handlers) ListPassports(w http.ResponseWriter, r *http.Request) {
	identity, ok := identityFrom(r.Context())
	if !ok {
		httperr.Unauthorized(w, r, "passports are listed by a signed-in human")
		return
	}
	rows, err := h.svc.ListPassports(r.Context(), identity)
	if err != nil {
		httperr.Write(w, r, err)
		return
	}
	data := make([]crmcontracts.PassportSummary, 0, len(rows))
	for _, p := range rows {
		summary := crmcontracts.PassportSummary{
			Id:        openapi_types.UUID(p.ID.UUID),
			Scopes:    p.Scopes,
			CreatedAt: p.CreatedAt,
			ExpiresAt: &p.ExpiresAt,
			RevokedAt: p.RevokedAt,
		}
		if p.Label != nil {
			summary.Label = *p.Label
		}
		if c := p.Connection; c != nil {
			summary.Connection = &crmcontracts.PassportConnection{
				ClientId:    c.ClientID,
				ClientName:  c.ClientName,
				ConnectedAt: c.ConnectedAt,
			}
			if c.LentPassportID != nil {
				lent := openapi_types.UUID(c.LentPassportID.UUID)
				summary.Connection.LentPassportId = &lent
			}
			summary.Connection.LentPassportLabel = c.LentPassportLabel
		}
		data = append(data, summary)
	}
	httperr.WriteJSON(w, http.StatusOK, struct {
		Data []crmcontracts.PassportSummary `json:"data"`
	}{Data: data})
}

// RevokePassport implements (DELETE /passports/{id}): the kill switch.
func (h Handlers) RevokePassport(w http.ResponseWriter, r *http.Request, id crmcontracts.Id) {
	identity, ok := identityFrom(r.Context())
	if !ok {
		httperr.Unauthorized(w, r, "passports are revoked by a signed-in human")
		return
	}
	if err := h.svc.RevokePassport(r.Context(), identity, ids.From[ids.PassportKind](ids.UUID(id))); err != nil {
		httperr.Write(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
