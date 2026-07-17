// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The one-shot IMAP pull (connectImap): dial a user's mailbox with the
// posted credentials, capture the most recent messages as email activities,
// and return a summary. The credentials are transient — used for this call
// only, never persisted (no capture_connection row) and never logged. The
// write lands through the capture Sink under the caller's LIVE authority
// swapped to the connector principal (Registry.RunTransient), so audit +
// outbox hold. This file owns only the transport: decode, build the
// connector, map provider failures onto clean RFC 7807 responses without
// leaking host/IMAP internals.

import (
	"errors"
	"log/slog"
	"net/http"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/modules/capture"
	"github.com/gradionhq/margince/backend/internal/modules/capture/imap"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
)

// imapConnectHandlers shadows the generated ConnectImap stub.
type imapConnectHandlers struct {
	registry *capture.Registry
}

func (h imapConnectHandlers) ConnectImap(w http.ResponseWriter, r *http.Request) {
	if h.registry == nil {
		httperr.NotImplemented(w, r, "connectImap")
		return
	}
	var req crmcontracts.ImapConnectRequest
	if !httperr.Decode(w, r, &req) {
		return
	}

	creds := imap.Credentials{
		Host:     req.Host,
		Email:    string(req.Email),
		Password: req.Password,
	}
	if req.Port != nil {
		creds.Port = *req.Port
	}
	if req.Mailbox != nil {
		creds.Mailbox = *req.Mailbox
	}
	if req.MaxMessages != nil {
		creds.MaxMessages = *req.MaxMessages
	}

	authReq, err := imap.AuthRequestFrom(creds)
	if err != nil {
		httperr.Write(w, r, err)
		return
	}

	ctx := r.Context()
	conn := imap.New()
	// Authenticate dials + logs in; a rejected login or an unreachable host
	// must degrade to an actionable status, never echo the provider's text.
	auth, err := conn.Authenticate(ctx, authReq)
	if err != nil {
		writeImapError(w, r, err)
		return
	}
	// Authenticate opened a live session (fd + a background reader goroutine);
	// own its teardown here so every exit — including a RunTransient failure
	// before the pull reaches its own cleanup — releases it.
	//craft:ignore swallowed-errors best-effort teardown of the read-only session; the response is already written by this point
	defer func() { _ = conn.Close() }()
	if err := h.registry.RunTransient(ctx, conn, auth); err != nil {
		writeImapError(w, r, err)
		return
	}

	// The connector is the single source of truth for which mailbox was read
	// (it resolves the default and trims), so report its resolved value rather
	// than re-deriving it here.
	stats := conn.Stats()
	httperr.WriteJSON(w, http.StatusOK, crmcontracts.ImapConnectResult{
		Connected: true,
		Mailbox:   stats.Mailbox,
		Captured:  stats.Captured,
		Skipped:   stats.Skipped,
		Contacts:  stats.Contacts,
	})
}

// writeImapError maps the connector's sentinels onto clean problem+json.
// The raw provider/network cause is logged server-side and NEVER written to
// the client — a rejected login is a 422, an unreachable server a 502, and
// anything else flows through the standard sentinel path.
func writeImapError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, imap.ErrLoginRejected):
		slog.WarnContext(r.Context(), "imap connect rejected", "err", err)
		httperr.Write(w, r, &httperr.DetailedError{
			Status: http.StatusUnprocessableEntity,
			Code:   "imap_login_rejected",
			Detail: "The mail server rejected these credentials. Check the host, email and password.",
		})
	case errors.Is(err, imap.ErrUnreachable):
		slog.ErrorContext(r.Context(), "imap connect unreachable", "err", err)
		httperr.Write(w, r, &httperr.DetailedError{
			Status: http.StatusBadGateway,
			Code:   "imap_unreachable",
			Detail: "Couldn't reach that mail server. Check the host and port, then retry.",
		})
	default:
		httperr.Write(w, r, err)
	}
}
