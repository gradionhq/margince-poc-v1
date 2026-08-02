// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package activities

// The request-time send pre-flight: one seam asking "can a send through this
// provider leave at all?", and the two refusals that answer no.
//
// It lives apart from either send path because it belongs to BOTH, and because
// the question is one question: authority over transmission is the delivery
// path's, and this only moves an already-knowable no forward to where the person
// who can fix it is still looking at the screen. The two refusals differ only in
// whose problem it is — the rep's own mailbox, or the workspace's bot.

import "context"

// SendAuthority answers whether this installation can actually transmit through
// a provider for the acting user today. Authority over transmission stays with
// the delivery path, which re-checks the credential at the moment it sends; this
// answers the same question earlier, so the common already-knowable failure
// becomes a refusal the user can act on instead of a 202 followed by a silently
// parked delivery.
//
// It is asked PER PROVIDER because the two transports it serves hold their
// credentials in different places: a mailbox is one human's OAuth grant, while a
// messaging channel is a bot an admin bound for the whole workspace. One seam
// with the provider named is what keeps "may this send leave?" a single question
// — two seams could answer it differently, and the one that wrongly said yes
// would park every message it accepted.
type SendAuthority interface {
	// SendCapable reports whether a send through provider, by the principal on
	// ctx, has a credential behind it that can transmit — not merely read.
	SendCapable(ctx context.Context, provider string) (bool, error)
}

// MailboxNotSendCapableError refuses a MAIL send this installation already knows
// cannot leave. It maps to 422 with an actionable message: the fix is the
// user's to make, and naming it is the whole point of checking early.
type MailboxNotSendCapableError struct{}

func (e *MailboxNotSendCapableError) Error() string {
	return "reconnect your mailbox to enable sending"
}

// MessageFault names the condition and no field: no send contract takes a
// `from` argument, so naming one would hand the caller an input it cannot
// change. Reconnecting the mailbox is the remedy, and a person has to do it.
func (e *MailboxNotSendCapableError) MessageFault() (code, message string) {
	return "mailbox_not_send_capable", e.Error()
}

// NoRecipientsError refuses a mail send that names nobody. It maps to 422 on
// both surfaces, and unlike its neighbours here the remedy is an argument the
// caller wrote — so it is a FieldFault, naming the one it has to change.
type NoRecipientsError struct{}

func (e *NoRecipientsError) Error() string {
	return "a send needs at least one recipient in `to`"
}

// FieldFault names `to` rather than the merged recipient list the store works
// in: `to` is what the caller actually sent, on both transports, and an error
// naming a field no request body has is an error nobody can act on.
func (e *NoRecipientsError) FieldFault() (field, code, message string) {
	return "to", "required", e.Error()
}

// ChannelNotSendCapableError refuses a channel send this installation already
// knows cannot leave: no live bot is bound for the provider. It maps to 422, and
// unlike its mail twin the fix usually belongs to an ADMIN rather than to the
// rep — so the message says who has to do what.
type ChannelNotSendCapableError struct{ Provider string }

func (e *ChannelNotSendCapableError) Error() string {
	return "this workspace has no connected " + e.Provider +
		" bot to send through — an admin connects one in the connector settings"
}

// FieldFault refuses a send into a channel with no bot authority to post.
func (e *ChannelNotSendCapableError) FieldFault() (field, code, message string) {
	return "id", "channel_not_send_capable", e.Error()
}

// WithSendAuthority returns a store whose send paths pre-flight the credential
// behind the transport they are about to use. The invariant: the pre-flight is
// advisory, and the delivery path's authority check at transmission is what
// refuses. A store composed without a send authority therefore runs no
// pre-flight and accepts the send, which is the correct reading of an advisory
// check that is absent.
func (s *Store) WithSendAuthority(authority SendAuthority) *Store {
	clone := *s
	clone.sendAuthority = authority
	return &clone
}

// canSend runs the pre-flight described on SendAuthority. It lives on the send
// path rather than in a transport because the MCP tool surface reaches these
// store methods directly: a check in one handler is a check half the callers
// skip.
//
// It is advisory by contract, unlike the send paths' composition guards, which
// fail closed: authority over transmission belongs to the delivery path, which re-checks the
// credential at the moment it sends. This answers earlier and in words the user
// can act on; it never decides whether a message may go. An unwired authority
// therefore reports capable — the honest reading of an advisory check that is
// absent.
//
// It returns the ANSWER rather than a refusal because what the user must do
// about a no differs by transport — reconnect their own mailbox, or have an
// admin bind the workspace's bot — and only the caller knows which question it
// asked.
func (s *Store) canSend(ctx context.Context, provider string) (bool, error) {
	if s.sendAuthority == nil {
		return true, nil
	}
	return s.sendAuthority.SendCapable(ctx, provider)
}
