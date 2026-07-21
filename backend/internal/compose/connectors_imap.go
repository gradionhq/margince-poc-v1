// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The standing IMAP connect transport (POST /v1/connectors/imap/connect): probe
// the supplied credentials, seal them to the vault via Registry.Connect, and let
// the background sweep take over — the OAuth-less sibling of the Google/graph
// connect flow in connectors.go. The transient one-shot pull remains a separate
// surface until its callers migrate.

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/modules/capture/imap"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/connector"
)

const providerIMAP = "imap"

const codeConnectorStoreFailed = "connector_store_failed"

// connectIMAP establishes a STANDING imap connection: the credentials are
// probed (dial + login, session closed), sealed to the vault by
// Registry.Connect, and the background sweep takes over — the same lifecycle
// as gmail, minus the OAuth ceremony. The transient one-shot pull
// (/connectors/imap/connect) remains a separate surface until its callers
// migrate.
func (h connectorHandlers) connectIMAP(w http.ResponseWriter, r *http.Request) {
	actor, ok := principal.Actor(r.Context())
	_, hasWS := principal.WorkspaceID(r.Context())
	if !ok || actor.Type != principal.PrincipalHuman || !hasWS {
		httperr.Write(w, r, &httperr.DetailedError{
			Status: http.StatusUnauthorized,
			Code:   codeUnauthorized,
			Detail: "Connecting a mailbox is a signed-in human action.",
		})
		return
	}
	// Scope preflight BEFORE any credential probe: the probe dials a
	// tenant-supplied host, so an under-scoped caller must be refused before
	// any egress happens (and before login-vs-unreachable becomes
	// distinguishable). Registry.Connect re-checks the same scopes as the
	// persistence invariant.
	for _, scope := range imap.NewStanding().Descriptor().Scopes {
		if !actor.Scopes.Has(scope) {
			httperr.Write(w, r, &httperr.DetailedError{
				Status: http.StatusForbidden,
				Code:   "scope_exceeded",
				Detail: "Connecting a mailbox needs the read scope your session does not hold.",
			})
			return
		}
	}
	// The shared decoder bounds the body (1 MiB), rejects trailing/noncanonical
	// input, and answers malformed JSON itself — so a decode failure is handled,
	// never conflated with the credential check below.
	var req crmcontracts.ConnectConnectorRequest
	if !httperr.Decode(w, r, &req) {
		return
	}
	if req.Imap == nil || req.Imap.Secret == nil || req.Imap.Host == "" || req.Imap.Username == "" {
		httperr.Write(w, r, &httperr.DetailedError{
			Status: http.StatusUnprocessableEntity,
			Code:   "imap_credentials_required",
			Detail: "The imap provider needs host, username and secret in the request body.",
		})
		return
	}
	port := 0
	if req.Imap.Port != nil {
		port = *req.Imap.Port
	}
	authReq, err := imap.AuthRequestFrom(imap.Credentials{
		Host:     req.Imap.Host,
		Port:     port,
		Email:    req.Imap.Username,
		Password: *req.Imap.Secret,
	})
	if err != nil {
		httperr.Write(w, r, &httperr.DetailedError{
			Status: http.StatusUnprocessableEntity,
			Code:   "imap_credentials_invalid",
			Detail: "These credentials could not be processed.",
		})
		return
	}
	authenticate := h.imapAuthenticate
	if authenticate == nil {
		authenticate = imap.NewStanding().Authenticate
	}
	auth, err := authenticate(r.Context(), authReq)
	if err != nil {
		writeIMAPConnectError(w, r, err)
		return
	}
	h.persistIMAPConnection(w, r, auth)
}

// persistIMAPConnection stores the sealed bundle and answers with the
// connected row — the connect's terminal half.
func (h connectorHandlers) persistIMAPConnection(w http.ResponseWriter, r *http.Request, auth connector.Auth) {
	if _, err := h.registry.Connect(r.Context(), providerIMAP, auth); err != nil {
		if errors.Is(err, apperrors.ErrScopeExceeded) {
			httperr.Write(w, r, &httperr.DetailedError{
				Status: http.StatusForbidden,
				Code:   "scope_exceeded",
				Detail: "Connecting a mailbox needs the read scope your session does not hold.",
			})
			return
		}
		slog.ErrorContext(r.Context(), "imap connector: persisting connection", "err", err)
		httperr.Write(w, r, &httperr.DetailedError{
			Status: http.StatusInternalServerError,
			Code:   codeConnectorStoreFailed,
			Detail: "The connection could not be stored. Nothing was captured; try again.",
		})
		return
	}
	views, err := h.registry.Connections(r.Context())
	if err != nil {
		slog.ErrorContext(r.Context(), "imap connector: reading back connection", "err", err)
		httperr.Write(w, r, &httperr.DetailedError{
			Status: http.StatusInternalServerError,
			Code:   codeConnectorStoreFailed,
			Detail: "The connection was stored but could not be read back.",
		})
		return
	}
	for _, v := range views {
		if v.Provider == providerIMAP {
			w.Header().Set("Content-Type", "application/json")
			conn := toContractConnection(v)
			if err := json.NewEncoder(w).Encode(crmcontracts.ConnectConnectorResponse{
				Connection: &conn,
			}); err != nil {
				// The status line is already gone; the log is the only place
				// a truncated success can still be seen.
				slog.ErrorContext(r.Context(), "imap connector: encoding connect response", "err", err)
			}
			return
		}
	}
	httperr.Write(w, r, &httperr.DetailedError{
		Status: http.StatusInternalServerError,
		Code:   codeConnectorStoreFailed,
		Detail: "The connection was stored but did not appear in the read-back.",
	})
}

// writeIMAPConnectError maps the connector sentinels onto the transport
// without leaking the provider's raw error.
func writeIMAPConnectError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, imap.ErrLoginRejected):
		httperr.Write(w, r, &httperr.DetailedError{
			Status: http.StatusUnprocessableEntity,
			Code:   "imap_login_rejected",
			Detail: "The mailbox rejected these credentials. Check host, email and app password.",
		})
	case errors.Is(err, imap.ErrUnreachable):
		httperr.Write(w, r, &httperr.DetailedError{
			Status: http.StatusBadGateway,
			Code:   "imap_unreachable",
			Detail: "The mail server could not be reached.",
		})
	default:
		slog.ErrorContext(r.Context(), "imap connector: authenticate", "err", err)
		httperr.Write(w, r, &httperr.DetailedError{
			Status: http.StatusInternalServerError,
			Code:   "imap_connect_failed",
			Detail: "The connection could not be established.",
		})
	}
}
