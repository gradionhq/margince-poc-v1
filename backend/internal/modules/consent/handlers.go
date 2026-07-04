package consent

import (
	"context"
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"
	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// Handlers is the module's transport slice; compose embeds it so the
// generated consent stubs are shadowed by real code.
type Handlers struct {
	store  *Store
	eraser Eraser
}

// Eraser is the erase-path seam (compose injects the real one): DSR
// fulfillment of an erasure request EXECUTES the erasure, it never just
// marks a row done.
type Eraser interface {
	ErasePerson(ctx context.Context, personID ids.UUID, reason string) error
}

func NewHandlers(pool *pgxpool.Pool) Handlers {
	return Handlers{store: NewStore(pool)}
}

// WithEraser returns a copy wired to the erase path.
func (h Handlers) WithEraser(e Eraser) Handlers {
	h.eraser = e
	return h
}

func (h Handlers) ListConsentPurposes(w http.ResponseWriter, r *http.Request, _ crmcontracts.ListConsentPurposesParams) {
	purposes, err := h.store.ListPurposes(r.Context())
	if err != nil {
		writeConsentErr(w, r, err)
		return
	}
	data := make([]crmcontracts.ConsentPurpose, 0, len(purposes))
	for _, p := range purposes {
		data = append(data, wirePurpose(p))
	}
	httperr.WriteJSON(w, http.StatusOK, map[string]any{
		"data": data,
		"page": crmcontracts.PageInfo{HasMore: false},
	})
}

func (h Handlers) CreateConsentPurpose(w http.ResponseWriter, r *http.Request) {
	var req crmcontracts.CreateConsentPurposeRequest
	if !httperr.Decode(w, r, &req) {
		return
	}
	requiresDOI := req.RequiresDoubleOptIn != nil && *req.RequiresDoubleOptIn
	purpose, err := h.store.CreatePurpose(r.Context(), req.Key, req.Label, requiresDOI)
	if err != nil {
		writeConsentErr(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusCreated, wirePurpose(purpose))
}

func (h Handlers) GetPersonConsent(w http.ResponseWriter, r *http.Request, id crmcontracts.Id) {
	states, events, err := h.store.PersonConsent(r.Context(), ids.UUID(id))
	if err != nil {
		writeConsentErr(w, r, err)
		return
	}
	wireStates := make([]crmcontracts.PersonConsentState, 0, len(states))
	for _, st := range states {
		wireStates = append(wireStates, wireState(st))
	}
	wireEvents := make([]crmcontracts.ConsentEvent, 0, len(events))
	for _, ev := range events {
		wireEvents = append(wireEvents, wireEvent(ev))
	}
	httperr.WriteJSON(w, http.StatusOK, map[string]any{"state": wireStates, "events": wireEvents})
}

func (h Handlers) RecordConsent(w http.ResponseWriter, r *http.Request, id crmcontracts.Id, _ crmcontracts.RecordConsentParams) {
	var req crmcontracts.RecordConsentRequest
	if !httperr.Decode(w, r, &req) {
		return
	}
	state, err := h.store.Record(r.Context(), RecordInput{
		PersonID:         ids.UUID(id),
		PurposeID:        ids.UUID(req.PurposeId),
		NewState:         string(req.NewState),
		LawfulBasis:      req.LawfulBasis,
		Source:           req.Source,
		DoubleOptInToken: req.DoubleOptInToken,
	})
	if err != nil {
		writeConsentErr(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, wireState(state))
}

func writeConsentErr(w http.ResponseWriter, r *http.Request, err error) {
	var invalid *ValidationError
	if errors.As(err, &invalid) {
		httperr.Write(w, r, httperr.Validation(invalid.Field, "invalid", invalid.Reason))
		return
	}
	httperr.Write(w, r, err)
}

func wirePurpose(p Purpose) crmcontracts.ConsentPurpose {
	return crmcontracts.ConsentPurpose{
		Id:                  openapi_types.UUID(p.ID),
		WorkspaceId:         openapi_types.UUID(p.WorkspaceID),
		Key:                 p.Key,
		Label:               p.Label,
		RequiresDoubleOptIn: &p.RequiresDoubleOptIn,
		CreatedAt:           p.CreatedAt,
	}
}

func wireState(st State) crmcontracts.PersonConsentState {
	out := crmcontracts.PersonConsentState{
		PurposeId:              openapi_types.UUID(st.PurposeID),
		State:                  crmcontracts.PersonConsentStateState(st.State),
		LawfulBasis:            st.LawfulBasis,
		DoubleOptInConfirmedAt: st.DoubleOptInConfirmedAt,
		UpdatedAt:              st.UpdatedAt,
	}
	if st.PurposeKey != "" {
		out.PurposeKey = &st.PurposeKey
	}
	return out
}

func wireEvent(ev ProofEvent) crmcontracts.ConsentEvent {
	actorType, actorID := splitActor(ev.CapturedBy)
	wireActorType := crmcontracts.ConsentEventActorType(actorType)
	return crmcontracts.ConsentEvent{
		Id:          openapi_types.UUID(ev.ID),
		PurposeId:   openapi_types.UUID(ev.PurposeID),
		NewState:    crmcontracts.ConsentEventNewState(ev.NewState),
		LawfulBasis: ev.LawfulBasis,
		Source:      ev.Source,
		ActorType:   &wireActorType,
		ActorId:     &actorID,
		OccurredAt:  ev.OccurredAt,
	}
}

// splitActor maps the stored captured_by ("human:<id>" | "agent:<id>" |
// "connector:<name>" | "system") onto the contract's actor pair.
func splitActor(capturedBy string) (actorType, actorID string) {
	for _, prefix := range []string{"human", "agent", "connector", "system"} {
		if capturedBy == prefix {
			return prefix, ""
		}
		if len(capturedBy) > len(prefix)+1 && capturedBy[:len(prefix)+1] == prefix+":" {
			return prefix, capturedBy[len(prefix)+1:]
		}
	}
	return "system", capturedBy
}
