// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package gmail

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"mime"
	"slices"
	"strings"

	"github.com/gradionhq/margince/backend/internal/shared/ports/connector"
)

// gmailSendScope permits transmission only — it cannot read, modify, or delete,
// which is why it rides alongside the read-only capture scope rather than
// replacing it.
const gmailSendScope = "https://www.googleapis.com/auth/gmail.send"

// ErrSendScopeMissing marks a connection whose Google grant does not include the
// send scope: the user connected for capture and declined (or predates) sending.
// Reconnecting is the fix, so the caller parks rather than retries.
var ErrSendScopeMissing = fmt.Errorf("gmail: this connection was not granted the send scope: %w", connector.ErrAuthRejected)

var _ connector.Sender = (*Connector)(nil)

// Send transmits one message as the connected mailbox owner.
//
// On a retry it first asks Gmail whether this message identity already exists.
// Job delivery is at-least-once and Gmail does not deduplicate on Message-ID, so
// without that lookup a crash between a successful transmission and its recorded
// outcome mails the recipient twice.
func (c *Connector) Send(ctx context.Context, auth connector.Auth, msg connector.OutboundMessage) (connector.SendReceipt, error) {
	var st authState
	if err := json.Unmarshal(auth, &st); err != nil {
		return connector.SendReceipt{}, fmt.Errorf("gmail: malformed auth bundle: %w", err)
	}
	if !slices.Contains(st.Granted, gmailSendScope) {
		return connector.SendReceipt{}, ErrSendScopeMissing
	}
	access, err := c.oauth.AccessToken(ctx, st.RefreshToken)
	if err != nil {
		return connector.SendReceipt{}, err
	}
	if msg.Attempt > 0 {
		id, thread, found, findErr := c.api.FindByMessageID(ctx, access, msg.MessageID)
		if findErr != nil {
			return connector.SendReceipt{}, findErr
		}
		if found {
			return connector.SendReceipt{ProviderMessageID: id, ThreadKey: thread}, nil
		}
	}
	raw := base64.URLEncoding.EncodeToString([]byte(buildRFC822(st.Owner, msg)))
	id, thread, err := c.api.Send(ctx, access, raw, "")
	if err != nil {
		return connector.SendReceipt{}, err
	}
	return connector.SendReceipt{ProviderMessageID: id, ThreadKey: thread}, nil
}

// bracket renders a message identity as RFC 5322 requires it on the wire. The
// identity travels unbracketed everywhere else in this system, because that is
// the form mail parsing yields and therefore the form the captured copy of this
// message will be keyed on.
func bracket(id string) string {
	if id == "" {
		return ""
	}
	return "<" + strings.Trim(id, "<>") + ">"
}

// buildRFC822 renders the provider-neutral message as the wire format Gmail
// accepts: header order follows RFC 5322's origination-first sequence, body is
// text/plain UTF-8.
func buildRFC822(from string, msg connector.OutboundMessage) string {
	var b strings.Builder
	writeHeader(&b, "From", from)
	writeHeader(&b, "To", strings.Join(msg.To, ", "))
	if len(msg.Cc) > 0 {
		writeHeader(&b, "Cc", strings.Join(msg.Cc, ", "))
	}
	// Encoded-word per RFC 2047 so a non-ASCII subject survives the wire.
	writeHeader(&b, "Subject", mime.QEncoding.Encode("utf-8", msg.Subject))
	writeHeader(&b, "Message-ID", bracket(msg.MessageID))
	if msg.InReplyTo != "" {
		writeHeader(&b, "In-Reply-To", bracket(msg.InReplyTo))
	}
	if len(msg.References) > 0 {
		refs := make([]string, 0, len(msg.References))
		for _, r := range msg.References {
			refs = append(refs, bracket(r))
		}
		writeHeader(&b, "References", strings.Join(refs, " "))
	}
	if msg.ListUnsubscribe != "" {
		writeHeader(&b, "List-Unsubscribe", msg.ListUnsubscribe)
		writeHeader(&b, "List-Unsubscribe-Post", msg.ListUnsubscribePost)
	}
	writeHeader(&b, "MIME-Version", "1.0")
	writeHeader(&b, "Content-Type", `text/plain; charset="utf-8"`)
	b.WriteString("\r\n")
	b.WriteString(msg.Body)
	return b.String()
}

// writeHeader emits one header line with CR and LF removed from the value: a
// bare CR or LF inside a header is the classic injection vector, and the only
// safe rendering of one is not to emit it.
func writeHeader(b *strings.Builder, name, value string) {
	clean := strings.NewReplacer("\r", "", "\n", "").Replace(value)
	b.WriteString(name)
	b.WriteString(": ")
	b.WriteString(clean)
	b.WriteString("\r\n")
}
