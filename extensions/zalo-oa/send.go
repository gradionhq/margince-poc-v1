// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package zalooa

// The transport half: what it takes to answer a conversation the poll captured
// (ADR-0107/A158, DESIGN-SP5 §9).
//
// THIS UNIT NEVER SENDS ON ITS OWN INITIATIVE. A rep stages a reply in the
// ordinary timeline reply box, the core checks their seat and their permissions,
// the dispatcher stages the delivery, and only then is Send called. The unit
// gets outbound without ever holding outbound authority.
//
// WHOSE CREDENTIAL TRANSMITS is where this differs from the sibling connector,
// and the difference is the provider's rather than a preference. Dispact holds
// one token per member and a message leaves as the person who wrote it. An
// Official Account has ONE credential and one identity on the wire: every rep's
// reply goes out as the OA, because that is the only thing Zalo will send it as.
// So the member the core hands in is deliberately NOT used to pick a credential —
// it stays load-bearing for the audit row, the seat gate and `captured_by`, and
// the credential is the authorizing admin's, resolved from the connection.
//
// DESIGN-SP5 §8 permits exactly this: `Live` may ignore the member it is handed.
// Its reasoning — reply from the account that received the message — is
// SATISFIED here rather than contradicted, because the OA is the account that
// received it.

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/gradionhq/margince/backend/pkg/extension"
)

// maxSendRunes bounds what leaves. Zalo caps a consultation message at 2000
// characters and answers a longer one with an argument refusal, which the core
// would read as a permanent failure of a message a rep can see in their own
// timeline. Refusing here says what to do about it instead.
const maxSendRunes = 2000

// send transmits one message as the Official Account.
func send(ctx context.Context, rt extension.Runtime, msg extension.OutboundMessage) (extension.Receipt, error) {
	return sendVia(ctx, rt, msg, newClient, newOAuthClient(), time.Now)
}

func sendVia(ctx context.Context, rt extension.Runtime, msg extension.OutboundMessage,
	dial clientFactory, grants grantExchanger, now clock,
) (extension.Receipt, error) {
	body := strings.TrimSpace(msg.Body)
	switch {
	case body == "":
		return extension.Receipt{}, fmt.Errorf("%w: an empty message has nothing to deliver", extension.ErrInvalid)
	case len([]rune(body)) > maxSendRunes:
		return extension.Receipt{}, fmt.Errorf("%w: Zalo accepts at most %d characters in a consultation message and this one is %d — shorten it and send again", extension.ErrInvalid, maxSendRunes, len([]rune(body)))
	}
	conn, err := liveConnection(ctx, rt)
	if err != nil {
		return extension.Receipt{}, err
	}
	// The recipient's account id is UNWRAPPED FROM THIS OA's namespace, and a
	// recipient belonging to another one is refused rather than trimmed. A
	// binding that survived a repointed connection carries a well-formed id that
	// names a DIFFERENT person at the account now connected, and delivering to
	// them is a mistake nothing downstream could detect.
	account, err := accountWithinOA(conn.OAID, msg.Recipient.ChannelUserID)
	if err != nil {
		return extension.Receipt{}, fmt.Errorf("%w: %s", extension.ErrInvalid, err.Error())
	}
	token, _, err := usableToken(ctx, rt, grants, conn, now())
	if err != nil {
		// Through the classifier rather than raw: a credential that cannot be
		// renewed is the one PERMANENT failure this unit has, and reporting it as
		// an ordinary transport error would have the core retry a token that will
		// never be accepted again.
		return extension.Receipt{}, sendRefusal(err)
	}
	sentID, err := dial(token.AccessToken).sendConsultation(ctx, account, body)
	if err != nil {
		return extension.Receipt{}, transmissionRefusal(err)
	}
	// REMEMBERED, so the poll does not read this back as a second copy of a
	// message the core has already written. See sentmessage.go for why the walk
	// makes that necessary.
	//
	// A failure here is NOT the send's failure: the message is at the recipient
	// and the receipt is owed to the core, which would otherwise retry a delivery
	// that already arrived. What is lost is the marker, and what that costs is one
	// duplicate on a timeline — visible, and survivable, which the alternative is
	// not.
	//craft:ignore swallowed-errors the message is already at the recipient, so reporting this would have the core retry a delivery that arrived — a customer messaged twice, against the one duplicate timeline row a lost marker costs
	_ = rememberSent(ctx, rt, conn.OAID, sentID)
	return extension.Receipt{ProviderMessageID: sentID}, nil
}

// live answers whether this installation can still send, WITHOUT spending the
// credential.
//
// That constraint is what decides the implementation. The obvious dry run — call
// `getoa` and see whether the token is accepted — spends the credential on every
// pre-flight, and this unit's pre-flight runs three times per delivery. So
// liveness is answered from state this side already holds: is there a connection,
// is it connected rather than parked, and is its credential still within its
// life. All three are facts the poll keeps current, and none of them requires
// reaching the provider.
//
// The member is ignored, deliberately: there is one Official Account and one
// credential, and it is live or not live for everybody at once.
//
// A confirmed "no" answers FALSE and the delivery parks where a human can see
// it; an inability to tell returns an ERROR and the delivery is retried.
// Collapsing the two would either strand a deliverable message or re-send a
// refused one.
func live(ctx context.Context, rt extension.Runtime, _ extension.UserID) (bool, error) {
	return liveVia(ctx, rt, time.Now)
}

func liveVia(ctx context.Context, rt extension.Runtime, now clock) (bool, error) {
	conn, err := liveConnection(ctx, rt)
	switch {
	case errors.Is(err, extension.ErrNotFound):
		// No connection, or one that is parked. Both are a confirmed no rather
		// than a fault: somebody disconnected, or somebody must re-authorize, and
		// there is nothing to retry into until they do.
		return false, nil
	case err != nil:
		// The row could not be read at all — that is this installation's
		// database, not an answer about the provider, and it is retried.
		return false, err
	}
	return credentialStillWithinItsLife(conn, now())
}

// credentialStillWithinItsLife reads the expiry the row mirrors out of the sealed
// credential.
//
// The THIRD answer is the point of the signature. A row that records no expiry,
// or one this side cannot read back, is a connection whose renewal state cannot
// be vouched for: answering true would stage a message against a credential
// nobody has confirmed, and answering false would park a delivery that is
// probably fine. Both are claims, and neither is one this function is in a
// position to make.
//
// The renewal margin is NOT applied here, and the difference from
// tokenPair.usable is deliberate: that one decides whether to RENEW early, this
// one decides whether a message may be staged at all. A credential inside its
// last hour is still a credential, and the send renews it on the way through.
func credentialStillWithinItsLife(conn connection, now time.Time) (bool, error) {
	if conn.AccessTokenExpiresAt == "" {
		return false, fmt.Errorf("zalo-oa: this connection records no credential expiry, so its usability cannot be confirmed")
	}
	expiresAt, err := time.Parse(time.RFC3339, conn.AccessTokenExpiresAt)
	if err != nil {
		return false, fmt.Errorf("zalo-oa: this connection's recorded expiry cannot be read, so its usability cannot be confirmed")
	}
	return expiresAt.After(now), nil
}

// liveConnection reads the connection a send may use, and answers ErrNotFound
// for every state that is not a working one.
//
// The two are folded together on purpose: a caller asking "can I send" gets one
// answer for "there is no connection" and "the connection is parked", because
// neither can carry a message and the remedy for both is a human on the setup
// screen.
func liveConnection(ctx context.Context, rt extension.Runtime) (connection, error) {
	var found *connection
	if err := rt.Tx(ctx, func(ctx context.Context, tx extension.Tx) error {
		var err error
		found, err = currentConnection(ctx, tx)
		return err
	}); err != nil {
		return connection{}, err
	}
	switch {
	case found == nil:
		return connection{}, fmt.Errorf("%w: this installation has no Zalo Official Account connected", extension.ErrNotFound)
	case found.Status != statusConnected:
		return connection{}, fmt.Errorf("%w: this installation's Zalo connection is not usable (%s) and an administrator has to repair it", extension.ErrNotFound, found.Status)
	}
	return *found, nil
}

// sendRefusal maps a failure that happened BEFORE anything was transmitted —
// reading the row, unsealing the credential, renewing it.
//
// Only a credential that cannot be renewed is PERMANENT here. Everything else —
// a timeout, a rate limit, another caller holding the renewal lease — is
// transient, which is the conservative posture for a channel: parking a message
// that would have sent is recoverable by a human, and this provider gives no way
// to recover the other mistake.
func sendRefusal(err error) error {
	if errors.Is(err, errUnauthorized) || errors.Is(err, errCredentialGone) || errors.Is(err, errRotationLost) {
		return fmt.Errorf("%w: %s", extension.ErrForbidden, err.Error())
	}
	return err
}

// transmissionRefusal maps a failure of the POST ITSELF, which carries one class
// the pre-flight cannot: the message may already be at the recipient.
//
// Zalo accepts no idempotency key on a send and offers no prior-send lookup, so a
// request whose answer never came back is a question no later attempt can settle.
// The core retries every refusal it is not told is unanswerable, so reporting one
// as an ordinary transport failure delivers the rep's message twice — the one
// failure a human cannot undo. ErrSendOutcomeUnknown stops the delivery instead
// and leaves the uncertainty on the record, which is worse for this unit and
// better for the person receiving the message.
//
// The line is narrow and worth stating twice: a failure of the call that
// TRANSMITS is this class. A refusal the provider actually sent is an ANSWER and
// is not.
func transmissionRefusal(err error) error {
	if errors.Is(err, errUnanswered) {
		return fmt.Errorf("%w: %s", extension.ErrSendOutcomeUnknown, err.Error())
	}
	if errors.Is(err, errProvider) {
		// A refusal the provider NAMED, which reaches here only after the branch
		// above has taken every answer this side could not read — so this really
		// is a decision, nothing was transmitted, and it is permanent for this
		// delivery: the same message to the same person produces the same answer.
		//
		// What it usually IS, stated no more confidently than it has been
		// measured: `-201` is what a recipient id that is malformed or names
		// nobody returns on a send. The window an Official Account may write into
		// unprompted is the other candidate and is NOT claimed here — the one
		// out-of-window send anybody has run against this API was accepted, so a
		// message telling a rep to wait for the customer would be guessing, and
		// guessing about the recipient is how the wrong person gets blamed for a
		// mistyped id.
		return fmt.Errorf("%w: Zalo refused this message for that recipient — check that the conversation is one this Official Account can still write into", extension.ErrInvalid)
	}
	return sendRefusal(err)
}
