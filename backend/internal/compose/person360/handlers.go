// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package person360

// The HTTP transport for the person record page. Wire concerns only: bind
// the path id, refuse the modes this read cannot honestly serve, and hand
// the result to the sentinel error mapping. The service owns the
// transaction and every gate.

import (
	"context"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// entityTypePerson is this baseline's record type; migration 0184 widened
// the table's CHECK to admit it beside organization.
const entityTypePerson = "person"

// OverlayMode answers whether the calling workspace reads from an incumbent
// mirror instead of this system of record. The composition layer injects
// the one Dispatcher every other overlay-aware read uses, so a mode flip is
// observed here at the same moment it is observed there.
type OverlayMode func(ctx context.Context) (bool, error)

// Handlers shadows the generated person-360 stubs.
type Handlers struct {
	svc     *Service
	overlay OverlayMode
}

// NewHandlers binds the transport to a ready service.
func NewHandlers(svc *Service, overlay OverlayMode) Handlers {
	return Handlers{svc: svc, overlay: overlay}
}

// GetPerson360 implements GET /people/{id}/360.
func (h Handlers) GetPerson360(w http.ResponseWriter, r *http.Request, id crmcontracts.Id) {
	if !h.nativeOnly(w, r) {
		return
	}
	view, err := h.svc.Assemble(r.Context(), ids.From[ids.PersonKind](ids.UUID(id)))
	if err != nil {
		httperr.Write(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, view)
}

// AcknowledgePersonView implements POST /people/{id}/view-ack.
func (h Handlers) AcknowledgePersonView(w http.ResponseWriter, r *http.Request, id crmcontracts.Id) {
	ack, err := h.svc.Acknowledge(r.Context(), ids.From[ids.PersonKind](ids.UUID(id)))
	if err != nil {
		httperr.Write(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, ack)
}

// GetPersonProfileFields implements GET /people/{id}/profile-fields.
func (h Handlers) GetPersonProfileFields(w http.ResponseWriter, r *http.Request, id crmcontracts.Id) {
	fields, err := h.svc.ProfileFields(r.Context(), ids.From[ids.PersonKind](ids.UUID(id)))
	if err != nil {
		httperr.Write(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, struct {
		Data []crmcontracts.PersonProfileField `json:"data"`
	}{Data: fields})
}

// nativeOnly refuses the read in overlay mode. A mirror holds none of these
// relationships, so answering from it would describe a record this
// installation does not own.
func (h Handlers) nativeOnly(w http.ResponseWriter, r *http.Request) bool {
	if h.overlay == nil {
		return true
	}
	overlay, err := h.overlay(r.Context())
	if err != nil {
		httperr.Write(w, r, err)
		return false
	}
	if overlay {
		httperr.Write(w, r, httperr.Validation("id", "unsupported_in_overlay_mode",
			"the person view is assembled from this system of record; while the workspace reads from the incumbent mirror, open the contact in the incumbent's own UI"))
		return false
	}
	return true
}

// Acknowledge records that the calling human has now seen this person.
//
// The upsert takes GREATEST(stored, now), so a slow tab's late-arriving ack
// can never rewind a newer one.
//
// The human gate is load-bearing, not defense in depth: an agent principal
// carries the granting human's id as its UserID, so resolving "the acting
// user" would happily mark a record as SEEN by a human who never opened it,
// consuming their unread marker on their behalf.
func (s *Service) Acknowledge(ctx context.Context, personID ids.PersonID) (crmcontracts.RecordViewAck, error) {
	if err := auth.RequireHuman(ctx); err != nil {
		return crmcontracts.RecordViewAck{}, err
	}
	if err := auth.Require(ctx, "person", principal.ActionRead); err != nil {
		return crmcontracts.RecordViewAck{}, err
	}
	userID, err := actingUser(ctx)
	if err != nil {
		return crmcontracts.RecordViewAck{}, err
	}
	now := s.now().UTC()
	var stored time.Time
	err = database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		// Anything that names a record is gated: acknowledging a person the
		// caller cannot read would confirm they exist.
		if err := auth.EnsureVisible(ctx, tx, "person", personID.UUID); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `
			INSERT INTO user_record_view (workspace_id, user_id, entity_type, entity_id, last_viewed_at)
			VALUES ($1, $2, $3, $4, $5)
			ON CONFLICT (workspace_id, user_id, entity_type, entity_id)
			DO UPDATE SET last_viewed_at = GREATEST(user_record_view.last_viewed_at, EXCLUDED.last_viewed_at)
			RETURNING last_viewed_at`,
			storekit.MustWorkspace(ctx), userID, entityTypePerson, personID, now).Scan(&stored)
	})
	if err != nil {
		return crmcontracts.RecordViewAck{}, err
	}
	return crmcontracts.RecordViewAck{
		EntityType:   crmcontracts.RecordViewAckEntityTypePerson,
		EntityId:     openapi_types.UUID(personID.UUID),
		LastViewedAt: stored,
	}, nil
}

// ProfileFields serves the enrichment evidence sidecar on its own. The
// person read is the gate: evidence about a contact the caller cannot see
// is not disclosed.
func (s *Service) ProfileFields(ctx context.Context, personID ids.PersonID) ([]crmcontracts.PersonProfileField, error) {
	if err := auth.Require(ctx, "person", principal.ActionRead); err != nil {
		return nil, err
	}
	var out []crmcontracts.PersonProfileField
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		if err := auth.EnsureVisible(ctx, tx, "person", personID.UUID); err != nil {
			return err
		}
		fields, err := readProfileFields(ctx, tx, personID)
		if err != nil {
			return err
		}
		out = fields
		return nil
	})
	return out, err
}
