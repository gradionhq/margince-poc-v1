// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The licensed-data-provider surface (ADR-0101, PI-WIRE-1..6): read the
// connections, connect or rotate a key, patch the saved policy, disconnect,
// delete retained data, and queue or read a person's enrichment run.
//
// Thin transport. The integrations store owns every RBAC gate and every write;
// what happens here is decoding, mapping and the human-only check the contract
// declares.

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/modules/integrations"
	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/ports/provider"
)

type integrationsHandlers struct {
	store *integrations.Store
	runs  provider.RunService
}

func (h integrationsHandlers) ListProviderConnections(w http.ResponseWriter, r *http.Request) {
	if h.store == nil {
		httperr.NotImplemented(w, r, "ListProviderConnections")
		return
	}
	conns, err := h.store.List(r.Context())
	if err != nil {
		httperr.Write(w, r, err)
		return
	}
	out := make([]crmcontracts.ProviderConnection, 0, len(conns))
	for _, c := range conns {
		out = append(out, toProviderConnection(c))
	}
	httperr.WriteJSON(w, http.StatusOK, struct {
		Data []crmcontracts.ProviderConnection `json:"data"`
	}{Data: out})
}

func (h integrationsHandlers) ConnectProvider(w http.ResponseWriter, r *http.Request, name crmcontracts.Provider, _ crmcontracts.ConnectProviderParams) {
	if h.store == nil {
		httperr.NotImplemented(w, r, "ConnectProvider")
		return
	}
	// Human-only (x-agent-access): an agent never binds a paid credential.
	if err := auth.RequireHuman(r.Context()); err != nil {
		httperr.Write(w, r, err)
		return
	}
	var body crmcontracts.ConnectProviderRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httperr.Write(w, r, httperr.Validation("body", "invalid_json", "request body is not valid JSON"))
		return
	}
	conn, err := h.store.Connect(r.Context(), integrations.ConnectInput{
		Provider: string(name),
		APIKey:   derefString(body.ApiKey),
		Config:   fromProviderConfig(body.Configuration),
	})
	if err != nil {
		httperr.Write(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, toProviderConnection(conn))
}

func (h integrationsHandlers) UpdateProviderConnection(w http.ResponseWriter, r *http.Request, name crmcontracts.Provider, params crmcontracts.UpdateProviderConnectionParams) {
	if h.store == nil {
		httperr.NotImplemented(w, r, "UpdateProviderConnection")
		return
	}
	if err := auth.RequireHuman(r.Context()); err != nil {
		httperr.Write(w, r, err)
		return
	}
	var body crmcontracts.UpdateProviderConnectionRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httperr.Write(w, r, httperr.Validation("body", "invalid_json", "request body is not valid JSON"))
		return
	}
	version, err := ifMatchVersion(params.IfMatch)
	if err != nil {
		httperr.Write(w, r, err)
		return
	}
	patch := fromProviderConfigPatch(body.Configuration)
	conn, err := h.store.UpdateConfig(r.Context(), string(name), patch, version)
	if err != nil {
		httperr.Write(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, toProviderConnection(conn))
}

func (h integrationsHandlers) DisconnectProvider(w http.ResponseWriter, r *http.Request, name crmcontracts.Provider) {
	if h.store == nil {
		httperr.NotImplemented(w, r, "DisconnectProvider")
		return
	}
	if err := auth.RequireHuman(r.Context()); err != nil {
		httperr.Write(w, r, err)
		return
	}
	if err := h.store.Disconnect(r.Context(), string(name)); err != nil {
		httperr.Write(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h integrationsHandlers) DeleteProviderData(w http.ResponseWriter, r *http.Request, name crmcontracts.Provider) {
	if h.store == nil {
		httperr.NotImplemented(w, r, "DeleteProviderData")
		return
	}
	if err := auth.RequireHuman(r.Context()); err != nil {
		httperr.Write(w, r, err)
		return
	}
	if err := h.store.DeleteProviderData(r.Context(), string(name)); err != nil {
		httperr.Write(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h integrationsHandlers) CreatePersonEnrichmentRun(w http.ResponseWriter, r *http.Request, id crmcontracts.Id, _ crmcontracts.CreatePersonEnrichmentRunParams) {
	if h.runs == nil {
		httperr.NotImplemented(w, r, "CreatePersonEnrichmentRun")
		return
	}
	if err := auth.RequireHuman(r.Context()); err != nil {
		httperr.Write(w, r, err)
		return
	}
	var body crmcontracts.CreatePersonEnrichmentRunRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httperr.Write(w, r, httperr.Validation("body", "invalid_json", "request body is not valid JSON"))
		return
	}
	run, err := h.runs.QueueRun(r.Context(), provider.QueueInput{
		PersonID: id.String(),
		Provider: string(body.Provider),
		// A person asking explicitly. Never fenced by the duplicate or
		// freshness checks — they know something the timestamps do not.
		Trigger: provider.TriggerManual,
	})
	if err != nil {
		writeRunError(w, r, err)
		return
	}
	// 202: the run is durable, and the provider has not been called yet.
	httperr.WriteJSON(w, http.StatusAccepted, toProviderRun(run))
}

func (h integrationsHandlers) GetPersonEnrichmentRun(w http.ResponseWriter, r *http.Request, id crmcontracts.Id, runID openapi_types.UUID) {
	if h.runs == nil {
		httperr.NotImplemented(w, r, "GetPersonEnrichmentRun")
		return
	}
	run, err := h.runs.GetRun(r.Context(), id.String(), runID.String())
	if err != nil {
		writeRunError(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, toProviderRun(run))
}

// writeRunError maps the port's not-connected state onto a 404. It is a
// supported configuration rather than a fault: asking for an enrichment when
// no provider is connected is answered honestly, not with a 500.
func writeRunError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, provider.ErrNotConnected) {
		httperr.Write(w, r, apperrors.ErrNotFound)
		return
	}
	httperr.Write(w, r, err)
}

// newIntegrationsHandlers builds the surface. A nil registry means no adapter
// is compiled in, which is a supported configuration rather than a broken
// one: the store still answers, every card renders "not connected", and no
// code path exists that could reach a provider (PI-AC-9).
func newIntegrationsHandlers(pool *pgxpool.Pool, reg *integrations.Registry) integrationsHandlers {
	if reg == nil {
		return integrationsHandlers{}
	}
	store, err := integrations.NewStore(InstallationDB(pool), nil, reg, time.Now)
	if err != nil {
		// Construction only fails on a missing dependency, which is a wiring
		// bug rather than a runtime condition — answer 501 rather than
		// serving a half-built store.
		return integrationsHandlers{}
	}
	return integrationsHandlers{store: store, runs: provider.NotConnected{}}
}
