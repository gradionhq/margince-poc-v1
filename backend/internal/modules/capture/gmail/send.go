// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package gmail

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
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

// Send transmits one already-encoded RFC822 message via messages.send.
// threadID files it under an existing Gmail conversation; empty starts a new
// one — omitempty on the wire because Gmail treats an empty threadId
// differently from an absent one.
func (a *httpAPI) Send(ctx context.Context, accessToken, rawBase64URL, threadID string) (string, string, error) {
	payload := struct {
		Raw      string `json:"raw"`
		ThreadID string `json:"threadId,omitempty"` //nolint:tagliatelle // Google's wire format
	}{Raw: rawBase64URL, ThreadID: threadID}
	var out struct {
		ID       string `json:"id"`
		ThreadID string `json:"threadId"` //nolint:tagliatelle // Google's wire format
	}
	if err := a.postJSON(ctx, accessToken, "/messages/send", payload, &out); err != nil {
		return "", "", err
	}
	return out.ID, out.ThreadID, nil
}

// FindByMessageID looks a message up by its RFC822 identity via Gmail's
// rfc822msgid: search operator — the retransmission guard's only tool for
// telling "already sent" from "never sent" on a retry.
func (a *httpAPI) FindByMessageID(ctx context.Context, accessToken, id string) (string, string, bool, error) {
	var out struct {
		Messages []struct {
			ID       string `json:"id"`
			ThreadID string `json:"threadId"` //nolint:tagliatelle // Google's wire format
		} `json:"messages"`
	}
	q := url.Values{"q": {"rfc822msgid:" + id}}
	if _, err := a.get(ctx, accessToken, "/messages", q, &out, maxJSONResponseBytes); err != nil {
		return "", "", false, err
	}
	if len(out.Messages) == 0 {
		return "", "", false, nil
	}
	return out.Messages[0].ID, out.Messages[0].ThreadID, true, nil
}

// postJSON performs an authorized POST with a JSON body and JSON-decodes the
// response into out — the same bounded client, bearer header, status
// classification, and bounded read as get, so a POST call is diagnosable
// exactly like a GET call rather than forking a second error-mapping path.
//
//craft:ignore naked-any payload/out are the caller-supplied JSON encode/decode values — the concrete type varies per endpoint
func (a *httpAPI) postJSON(ctx context.Context, accessToken, path string, payload, out any) error {
	reqBody, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("gmail: encoding %s request: %w", path, err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.base+path, bytes.NewReader(reqBody))
	if err != nil {
		return fmt.Errorf("gmail: building request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err := a.client.Do(req)
	if err != nil {
		return fmt.Errorf("gmail: %s: %w", path, ErrUnreachable)
	}
	//craft:ignore swallowed-errors best-effort close of the response body — the decoded result/status is what matters
	defer func() { _ = resp.Body.Close() }()
	// A read fault mid-body is a real reachability failure, distinct from the
	// size cap (LimitReader signals the cap with EOF, not an error). Surface it
	// as such rather than letting a truncated body fail the decode with a
	// misleading "decoding" error.
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxJSONResponseBytes))
	if err != nil {
		return fmt.Errorf("gmail: reading %s: %w", path, ErrUnreachable)
	}
	if err := classifyStatus(resp, path, body); err != nil {
		return err
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("gmail: decoding %s: %w", path, ErrUnreachable)
	}
	return nil
}
