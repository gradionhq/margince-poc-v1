// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package dealrooms

import (
	"log/slog"
	"net/http"

	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

func participantPathID(id openapi_types.UUID) ids.DealRoomParticipantID {
	return ids.DealRoomParticipantID{UUID: ids.UUID(id)}
}

// ListDealRoomParticipants returns the room's roster.
func (h Handlers) ListDealRoomParticipants(w http.ResponseWriter, r *http.Request, id crmcontracts.Id, params crmcontracts.ListDealRoomParticipantsParams) {
	activeOnly := params.ActiveOnly != nil && *params.ActiveOnly
	people, page, err := h.store.ListParticipants(r.Context(), pathID(id), activeOnly)
	if err != nil {
		httperr.Write(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, crmcontracts.DealRoomParticipantListResponse{
		Data: people,
		Page: pageInfo(page),
	})
}

// InviteDealRoomParticipant admits a named person and returns their credential.
func (h Handlers) InviteDealRoomParticipant(w http.ResponseWriter, r *http.Request, id crmcontracts.Id) {
	var req crmcontracts.InviteDealRoomParticipantRequest
	if !httperr.Decode(w, r, &req) {
		return
	}
	in, err := inviteInput(req)
	if err != nil {
		httperr.Write(w, r, err)
		return
	}
	issued, err := h.store.InviteParticipant(r.Context(), pathID(id), in)
	if err != nil {
		httperr.Write(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusCreated, h.issuedBody(r, issued))
}

// ResendDealRoomInvitation issues a fresh credential, retiring the previous one.
func (h Handlers) ResendDealRoomInvitation(w http.ResponseWriter, r *http.Request, id crmcontracts.Id, participantID openapi_types.UUID) {
	issued, err := h.store.ResendInvitation(r.Context(), pathID(id), participantPathID(participantID))
	if err != nil {
		httperr.Write(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusCreated, h.issuedBody(r, issued))
}

// RevokeDealRoomParticipant takes a person's access away.
func (h Handlers) RevokeDealRoomParticipant(w http.ResponseWriter, r *http.Request, id crmcontracts.Id, participantID openapi_types.UUID) {
	participant, err := h.store.RevokeParticipant(r.Context(), pathID(id), participantPathID(participantID))
	if err != nil {
		httperr.Write(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, participant)
}

// UpdateDealRoomParticipant corrects a participant's details.
func (h Handlers) UpdateDealRoomParticipant(w http.ResponseWriter, r *http.Request, id crmcontracts.Id, participantID openapi_types.UUID) {
	var req crmcontracts.UpdateDealRoomParticipantRequest
	if !httperr.Decode(w, r, &req) {
		return
	}
	in, err := participantUpdateInput(req)
	if err != nil {
		httperr.Write(w, r, err)
		return
	}
	participant, err := h.store.UpdateParticipant(r.Context(), pathID(id),
		participantPathID(participantID), in)
	if err != nil {
		httperr.Write(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, participant)
}

// issuedBody renders a freshly minted credential and delivers it if the
// installation can send mail.
//
// Delivery is best-effort and never fails the write: the participant and the
// credential are recorded either way, so a relay outage leaves a seller with a
// link they can pass on by hand rather than a half-admitted person and an error.
// `delivered` says which happened, so the caller knows whether to send it.
func (h Handlers) issuedBody(r *http.Request, issued IssuedInvitation) crmcontracts.DealRoomInvitationIssued {
	delivered := false
	if h.canSendInvite() {
		sendErr := h.sendInvite(r, issued)
		if sendErr != nil {
			// Logged rather than returned: the caller's request succeeded, and
			// telling them it failed would invite a retry that mints a second
			// credential and silently retires the one already on its way.
			slog.ErrorContext(r.Context(), "deal room invitation email failed",
				"participant_id", issued.Participant.Id, "err", sendErr)
		} else {
			delivered = true
		}
		// Recorded as well as reported, so the roster can answer "did it
		// arrive?" tomorrow. Without this the delivery state a seller reads
		// would be `pending` forever, whatever actually happened to the mail.
		h.recordSendOutcome(r, issued, sendErr)
	}
	return crmcontracts.DealRoomInvitationIssued{
		Participant:         issued.Participant,
		Credential:          issued.Credential,
		CredentialExpiresAt: issued.ExpiresAt,
		Delivered:           delivered,
	}
}

// recordSendOutcome stamps what the relay did onto the invitation.
//
// It runs after the issuing transaction has committed, because the send happens
// there: rolling the invitation back because a mail server was slow would leave
// the seller with no participant at all, which is worse than a participant whose
// delivery state is unknown. A failure to record is therefore logged and
// swallowed — the credential is valid either way, and the only casualty is the
// chip on the roster.
func (h Handlers) recordSendOutcome(r *http.Request, issued IssuedInvitation, sendErr error) {
	if err := h.store.RecordInvitationSend(r.Context(),
		participantPathID(issued.Participant.Id), sendErr); err != nil {
		slog.ErrorContext(r.Context(), "recording deal room invitation delivery failed",
			"participant_id", issued.Participant.Id, "err", err)
	}
}

// PreviewDealRoom hands the seller a one-time credential for their own
// preview seat. The credential is in the body exactly once and nowhere else.
func (h Handlers) PreviewDealRoom(w http.ResponseWriter, r *http.Request, id crmcontracts.Id) {
	issued, err := h.store.PreviewRoom(r.Context(), pathID(id))
	if err != nil {
		httperr.Write(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusCreated, crmcontracts.DealRoomPreviewIssued{
		Credential:          issued.Credential,
		CredentialExpiresAt: issued.ExpiresAt,
	})
}
