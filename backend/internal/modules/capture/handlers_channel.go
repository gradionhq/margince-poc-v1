// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package capture

// The /channel-connections transport. compose embeds ChannelHandlers so these
// methods shadow the generated 501 stubs; a role that composes no channel store
// leaves the field zero and every operation answers an honest 503 rather than
// nil-dereferencing.
//
// The mapping in writeChannelErr is the reason this file exists rather than the
// handlers calling httperr.Write directly: the provider's sentinels each have a
// different actionable answer, and none of them may carry Telegram's own
// response text onto the wire.

import (
	"errors"
	"net/http"

	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/modules/capture/telegram"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
	"github.com/gradionhq/margince/backend/internal/platform/keyvault"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// ChannelHandlers is the channel-connection transport slice. The store owns the
// RBAC gate, the connect ordering, and the write shape; this layer only decodes,
// maps and writes.
type ChannelHandlers struct {
	store *ChannelStore
}

// NewChannelHandlers wires the transport over a channel store.
func NewChannelHandlers(store *ChannelStore) ChannelHandlers {
	return ChannelHandlers{store: store}
}

// WithVault hands the credential custodian to an already-composed transport,
// returning a copy: the composition root learns the installation's public origin
// and its vault from two different options, and the origin is what decides
// whether this surface exists at all. A transport that was never composed stays
// uncomposed — a vault alone cannot tell the provider where to deliver.
func (h ChannelHandlers) WithVault(vault keyvault.Vault) ChannelHandlers {
	if h.store == nil {
		return h
	}
	h.store = h.store.withVault(vault)
	return h
}

// ListChannelConnections returns the workspace's live channel connections.
func (h ChannelHandlers) ListChannelConnections(w http.ResponseWriter, r *http.Request) {
	if !h.composed(w, r) {
		return
	}
	conns, err := h.store.List(r.Context())
	if err != nil {
		writeChannelErr(w, r, err)
		return
	}
	data := make([]crmcontracts.ChannelConnection, 0, len(conns))
	for _, c := range conns {
		data = append(data, wireChannelConnection(c))
	}
	httperr.WriteJSON(w, http.StatusOK, crmcontracts.ChannelConnectionListResponse{Data: data})
}

// ConnectChannel binds a bot to the workspace and returns the live connection.
func (h ChannelHandlers) ConnectChannel(w http.ResponseWriter, r *http.Request) {
	if !h.composed(w, r) {
		return
	}
	var req crmcontracts.ConnectChannelRequest
	if !httperr.Decode(w, r, &req) {
		return
	}
	conn, err := h.store.Connect(r.Context(), ConnectRequest{
		Provider: string(req.Provider),
		BotToken: req.BotToken,
	})
	if err != nil {
		writeChannelErr(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusCreated, wireChannelConnection(conn))
}

// ReplaceChannelToken rotates a connection's bot token in place and returns the
// reconnected row — re-read rather than assembled from the request, so the
// response shows the bot the provider actually confirmed.
func (h ChannelHandlers) ReplaceChannelToken(w http.ResponseWriter, r *http.Request, id crmcontracts.Id) {
	if !h.composed(w, r) {
		return
	}
	var req crmcontracts.ReplaceChannelTokenRequest
	if !httperr.Decode(w, r, &req) {
		return
	}
	if err := h.store.ReplaceToken(r.Context(), ids.UUID(id), req.BotToken); err != nil {
		writeChannelErr(w, r, err)
		return
	}
	conn, err := h.store.Get(r.Context(), ids.UUID(id))
	if err != nil {
		writeChannelErr(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, wireChannelConnection(conn))
}

// DisconnectChannel withdraws the binding.
func (h ChannelHandlers) DisconnectChannel(w http.ResponseWriter, r *http.Request, id crmcontracts.Id) {
	if !h.composed(w, r) {
		return
	}
	if err := h.store.Disconnect(r.Context(), ids.UUID(id)); err != nil {
		writeChannelErr(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// composed reports whether this role wired a channel store, answering an
// honest 503 when it did not — the surface is declared in the contract, and a
// role that serves no channel connect must say so rather than 500.
func (h ChannelHandlers) composed(w http.ResponseWriter, r *http.Request) bool {
	if h.store != nil {
		return true
	}
	httperr.Write(w, r, &httperr.DetailedError{
		Status: http.StatusServiceUnavailable,
		Code:   "channel_connections_not_configured",
		Detail: "This deployment serves no messaging-channel connections.",
	})
	return false
}

func wireChannelConnection(c ChannelConnection) crmcontracts.ChannelConnection {
	return crmcontracts.ChannelConnection{
		Id:           openapi_types.UUID(c.ID),
		Provider:     crmcontracts.ChannelConnectionProvider(c.Provider),
		ChannelId:    c.ChannelID,
		ChannelLabel: c.ChannelLabel,
		Status:       crmcontracts.ChannelConnectionStatus(c.Status),
		Version:      c.Version,
		CreatedAt:    &c.CreatedAt,
		UpdatedAt:    &c.UpdatedAt,
	}
}

// writeChannelErr maps this surface's typed faults onto the wire. The provider
// sentinels are spelled out because each one tells the operator to do something
// different, and because none of them may forward Telegram's own text: the
// detail below is fixed, and the provider's description stays in the wrapped
// error the platform mapper logs server-side.
func writeChannelErr(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, telegram.ErrTokenRejected):
		httperr.Write(w, r, &httperr.DetailedError{
			Status: http.StatusBadRequest,
			Code:   "channel_token_rejected",
			Detail: "The bot token was rejected. Check the token BotFather issued, and that it has not been revoked.",
		})
	case errors.Is(err, ErrChannelWebhookOwnedElsewhere):
		httperr.Write(w, r, &httperr.DetailedError{
			Status: http.StatusConflict,
			Code:   "channel_webhook_owned_elsewhere",
			Detail: "This bot already delivers its updates to another installation. Use a different bot, or disconnect it there first.",
		})
	case errors.Is(err, ErrChannelWebhookBaseUnset):
		httperr.Write(w, r, &httperr.DetailedError{
			Status: http.StatusServiceUnavailable,
			Code:   "channel_public_base_url_unset",
			Detail: "This installation does not know its own public address, so the provider cannot be told where to deliver. Set the public base URL and restart.",
		})
	case errors.Is(err, ErrChannelWiringIncomplete):
		httperr.Write(w, r, &httperr.DetailedError{
			Status: http.StatusServiceUnavailable,
			Code:   "channel_credentials_not_configured",
			Detail: "This installation has no credential store configured, so a bot's token cannot be sealed or removed. Configure one and restart.",
		})
	case errors.Is(err, telegram.ErrUnreachable):
		httperr.Write(w, r, &httperr.DetailedError{
			Status: http.StatusBadGateway,
			Code:   "channel_provider_unreachable",
			Detail: "The messaging provider could not be reached. The connection is kept as pending — retry it once the provider is back.",
		})
	case errors.Is(err, telegram.ErrRequestRejected):
		httperr.Write(w, r, &httperr.DetailedError{
			Status: http.StatusBadGateway,
			Code:   "channel_provider_rejected",
			Detail: "The messaging provider refused this request. The connection is kept as pending — check the installation's public address is reachable from the internet.",
		})
	default:
		httperr.Write(w, r, err)
	}
}
