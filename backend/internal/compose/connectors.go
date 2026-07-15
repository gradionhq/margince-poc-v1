// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The per-provider OAuth capture surface (RC-8; capture.md CAP-WIRE-1):
// listConnectors / connectConnector / connectorOAuthCallback /
// disconnectConnector. The contract and the read-only Gmail connector
// (internal/modules/capture/gmail) are landed; this HTTP transport — the
// signed-state OAuth handshake, the callback that reconstructs the granting
// human's authority to persist the connection, and the background SyncOnce
// poller — is the next slice, tracked in
// docs/superpowers/plans/2026-07-15-gmail-capture-connector.md.
//
// Until then these operations answer with the repo's standard "declared but
// not implemented" 501 (the same honest posture as customfields'
// create/setOptions), never a silent 404: the surface exists, the wiring is
// pending. connectorHandlers has no dependencies, so Server embeds its zero
// value; the real dependencies (gmail.OAuth/API, the state signer, the
// connection registry) are injected when this is implemented.

import (
	"net/http"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
)

type connectorHandlers struct{}

func (connectorHandlers) ListConnectors(w http.ResponseWriter, r *http.Request) {
	httperr.NotImplemented(w, r, "ListConnectors")
}

func (connectorHandlers) ConnectConnector(w http.ResponseWriter, r *http.Request, _ crmcontracts.CaptureProvider) {
	httperr.NotImplemented(w, r, "ConnectConnector")
}

func (connectorHandlers) ConnectorOAuthCallback(w http.ResponseWriter, r *http.Request, _ crmcontracts.CaptureProvider, _ crmcontracts.ConnectorOAuthCallbackParams) {
	httperr.NotImplemented(w, r, "ConnectorOAuthCallback")
}

func (connectorHandlers) DisconnectConnector(w http.ResponseWriter, r *http.Request, _ crmcontracts.CaptureProvider) {
	httperr.NotImplemented(w, r, "DisconnectConnector")
}
