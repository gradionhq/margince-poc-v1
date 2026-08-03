// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package connector

import (
	"errors"
	"fmt"
)

// Recipient is ONE addressee of an outbound message, in whichever vocabulary
// its transport uses: a mail address, as EmailMessage's To and Cc lists carry
// them, or a channel identity, as ChannelMessage carries its recipient.
//
// It lives beside those two seams because it is the union of their addressees
// and nothing else. The outbound suppression gate answers about it, so one
// default-deny check covers mail and a messaging channel rather than each
// transport growing a gate of its own — two gates would be two chances for one
// of them to stop applying.
//
// EXACTLY ONE arm is set; Validate is the enforcement. Both arms, or neither,
// is a caller defect rather than a recipient nobody granted anything to: a
// half-populated recipient handed to a default-deny gate is asked about nobody
// and answered yes, which is how a consent check silently stops applying to a
// whole channel.
type Recipient struct {
	// Email is the addr-spec of a mail recipient.
	Email string
	// Channel is the messaging-channel identity of a channel recipient.
	Channel *ChannelIdentity
}

// ErrRecipientShape marks a recipient that names no subject, or two, or half of
// a channel identity. It is a FAULT and not a suppression answer: the question
// could not be asked, so nothing has been learned about anybody's consent.
var ErrRecipientShape = errors.New("connector: a recipient carries exactly one of an email address or a channel identity")

// Validate refuses a recipient no gate could answer about. The third case is
// the one a caller reaches by accident: Provider and ChannelUserID TOGETHER are
// a channel identity's resolution key (ChannelIdentity), so half of one
// resolves to nobody while still reading as a channel recipient.
func (r Recipient) Validate() error {
	switch {
	case r.Email != "" && r.Channel != nil:
		return fmt.Errorf("recipient %q also names a channel identity: %w", r.Email, ErrRecipientShape)
	case r.Email == "" && r.Channel == nil:
		return fmt.Errorf("recipient names neither: %w", ErrRecipientShape)
	case r.Channel != nil && (r.Channel.Provider == "" || r.Channel.ChannelUserID == ""):
		return fmt.Errorf("channel recipient is missing its provider or its account id: %w", ErrRecipientShape)
	}
	return nil
}

// EmailRecipients lifts an address list into recipients. Spelled once because
// both sides of the mail seam do it — the gate's own address-shaped entry point
// and the dispatcher assembling a delivery's addressees — and a second loop
// would be a second place a blank address could slip through as a recipient
// naming nobody.
func EmailRecipients(addresses []string) []Recipient {
	out := make([]Recipient, 0, len(addresses))
	for _, addr := range addresses {
		out = append(out, Recipient{Email: addr})
	}
	return out
}
