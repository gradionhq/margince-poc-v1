// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package activities

// The channel reply's transport: decode, call the one send path, answer. The
// 🟡 confirm-first admission this operation declares is enforced BEFORE the
// request reaches here (the composition root's autonomy gate reads the tier off
// the contract), so a human's own call arrives as the approval it is.

import (
	"errors"
	"net/http"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// WithChannelDelivery returns handlers whose channel reply is recorded for
// transmission. Compose calls this; without it a reply refuses rather than leave
// a timeline entry claiming a message went out.
func (h Handlers) WithChannelDelivery(stager ChannelDeliveryStager) Handlers {
	h.channelDelivery = stager
	return h
}

// WithChannelReachability returns handlers whose channel reply can resolve who
// the conversation is with. Compose calls this; without it the reply fails
// closed, because a recipient nobody resolved is a message to nobody.
func (h Handlers) WithChannelReachability(reach ChannelReachability) Handlers {
	h.store = h.store.WithChannelReachability(reach)
	return h
}

// SendMessage replies on a captured channel conversation: the anchor names the
// conversation, the server resolves who it is with, and a 202 means the outbound
// activity is committed and the delivery is queued behind it.
func (h Handlers) SendMessage(w http.ResponseWriter, r *http.Request, id crmcontracts.Id, _ crmcontracts.SendMessageParams) {
	var req crmcontracts.SendMessageRequest
	if !httperr.Decode(w, r, &req) {
		return
	}
	sent, err := h.store.SendMessage(r.Context(), pathID[ids.ActivityKind](id), SendMessageInput{
		Body:           req.Body,
		ConsentPurpose: req.ConsentPurpose,
	}, h.consent, h.channelDelivery)
	if err != nil {
		writeChannelSendErr(w, r, err)
		return
	}
	// 202: accepted for delivery, the activity is the durable fact — the row
	// that keeps the sent message on the timeline across a reload.
	httperr.WriteJSON(w, http.StatusAccepted, sent)
}

// writeChannelSendErr maps this path's own refusals, then hands everything else
// to the shared mapping. All four are 422 and all four name what to do: the
// request carried no message, the caller pointed at the wrong kind of
// conversation, this workspace has no bot bound, or the conversation does not
// reach exactly one person.
func writeChannelSendErr(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, errEmptyMessageBody) {
		httperr.Write(w, r, httperr.Validation("body", "empty_message_body", errEmptyMessageBody.Error()))
		return
	}
	var wrongKind *NotAChannelConversationError
	if errors.As(err, &wrongKind) {
		httperr.Write(w, r, httperr.Validation("id", "not_a_channel_conversation", wrongKind.Error()))
		return
	}
	var noBot *ChannelNotSendCapableError
	if errors.As(err, &noBot) {
		httperr.Write(w, r, httperr.Validation("id", "channel_not_send_capable", noBot.Error()))
		return
	}
	var recipient *ChannelRecipientError
	if errors.As(err, &recipient) {
		httperr.Write(w, r, httperr.Validation("recipient", recipient.Code(), recipient.Error()))
		return
	}
	writeStoreErr(w, r, err)
}
