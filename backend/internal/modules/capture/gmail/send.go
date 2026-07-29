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
	"log/slog"
	"mime"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/gradionhq/margince/backend/internal/modules/capture/mailmap"
	"github.com/gradionhq/margince/backend/internal/shared/ports/connector"
)

// SendScope permits transmission only — it cannot read, modify, or delete,
// which is why it rides alongside the read-only capture scope rather than
// replacing it.
//
// Exported so the OAuth consent that REQUESTS it names the same string this
// connector RE-CHECKS. A send whose consent asked for a scope the connector
// then does not find parks every message with "not granted the send scope",
// and nothing at compile time would say why.
const SendScope = "https://www.googleapis.com/auth/gmail.send"

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
//
// The lookup narrows that window; it does not close it. FindByMessageID reads
// Gmail's search index, which is eventually consistent with a just-completed
// send — a retry landing before the index catches up gets a false negative and
// still mails twice. Nor does anything here serialize concurrent attempts at
// the same delivery: that guarantee lives entirely outside this package, in
// River delivering one job per delivery. The delivery store deliberately
// carries no in-flight status and no claim, so two concurrent attempts on the
// same delivery would both observe it pending and both call Send here — the
// one-job-per-delivery assumption, not this lookup, is what keeps that from
// happening in practice.
func (c *Connector) Send(ctx context.Context, auth connector.Auth, msg connector.OutboundMessage) (connector.SendReceipt, error) {
	// Before anything reaches Google: a message with no usable identity cannot
	// be found again by the prior-send lookup above, so transmitting it would
	// mail the recipient once per retry with nothing able to tell that it had
	// already gone.
	if err := msg.Validate(); err != nil {
		return connector.SendReceipt{}, err
	}
	var st authState
	if err := json.Unmarshal(auth, &st); err != nil {
		return connector.SendReceipt{}, fmt.Errorf("gmail: malformed auth bundle: %w", err)
	}
	if !slices.Contains(st.Granted, SendScope) {
		return connector.SendReceipt{}, ErrSendScopeMissing
	}
	access, err := c.oauth.AccessToken(ctx, st.RefreshToken)
	if err != nil {
		return connector.SendReceipt{}, err
	}
	if msg.Attempt > 0 {
		id, found, findErr := c.api.FindByMessageID(ctx, access, msg.MessageID)
		if findErr != nil {
			return connector.SendReceipt{}, findErr
		}
		if found {
			return connector.SendReceipt{ProviderMessageID: id}, nil
		}
	}
	raw := base64.URLEncoding.EncodeToString([]byte(buildRFC822(st.Owner, msg)))
	id, err := c.api.Send(ctx, access, raw)
	if err != nil {
		return connector.SendReceipt{}, err
	}
	return connector.SendReceipt{
		ProviderMessageID: id,
		RFC822MessageID:   c.stampedIdentity(ctx, access, st.Owner, id),
	}, nil
}

// stampedIdentity reads back the RFC822 identity the provider actually put on
// the message. Gmail discards a client-supplied Message-ID and mints its own,
// so the identity this system records — the natural key its captured echo will
// carry, and the key a reply will root at — has to come from the sent copy
// rather than from what was asked for.
//
// It parses with mailmap, the same function capture parses the echo with, so
// the identity recorded here and the identity derived there are one function of
// one set of bytes and cannot drift apart.
//
// Unlike FindByMessageID, this is a get-by-id, not a search: messages.get on
// the id messages.send just returned reads the resource directly, not the
// eventually-consistent search index the retransmission guard is warned about
// above. The ordering is already right; it does not need "fixing".
//
// EVERY failure returns "". The message has already been transmitted when this
// runs, and returning an error would hand the delivery back to a retry whose
// prior-send lookup cannot find a rewritten identity — mailing the recipient a
// second time to fix a bookkeeping problem. An unread identity costs one
// duplicate timeline row; a re-mail costs the recipient's trust. This is the
// one place in this package where an error is deliberately not propagated, and
// TestSendSucceedsWithNoIdentityWhenTheReadBackFails is what keeps it that way.
//
// A connection granted send-but-not-read makes this structurally dead: Send
// verifies only SendScope, so the read-back 403s and degrades as above.
func (c *Connector) stampedIdentity(ctx context.Context, access, owner, providerMessageID string) string {
	sent, err := c.api.GetRaw(ctx, access, providerMessageID)
	if err != nil {
		slog.WarnContext(ctx, "gmail: reading back the sent message identity",
			"err", err, "provider_message_id", providerMessageID)
		return ""
	}
	parsed, err := mailmap.Parse(sent.RFC822, owner)
	if err != nil {
		slog.WarnContext(ctx, "gmail: parsing the sent message identity", "err", err)
		return ""
	}
	return parsed.ID()
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
	writeHeader(&b, "To", addressList(msg.To))
	if cc := addressList(msg.Cc); cc != "" {
		writeHeader(&b, "Cc", cc)
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

// addressList renders one address header value: each address trimmed, empties
// dropped, comma-separated. The trim is the same normalization the send path's
// consent gate matches on, so the address a recipient is asked about and the
// address the header carries are one string — a stored " a@x " must not become
// folding whitespace around an addr-spec that some clients then display raw.
func addressList(addrs []string) string {
	out := make([]string, 0, len(addrs))
	for _, addr := range addrs {
		if trimmed := strings.TrimSpace(addr); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return strings.Join(out, ", ")
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

// Send transmits one already-encoded RFC822 message via messages.send. No
// threadId is sent: the message carries its own In-Reply-To/References chain,
// which is the threading this system reads, and Gmail files a reply under the
// right conversation from those headers.
func (a *httpAPI) Send(ctx context.Context, accessToken, rawBase64URL string) (string, error) {
	payload := struct {
		Raw string `json:"raw"`
	}{Raw: rawBase64URL}
	var out struct {
		ID string `json:"id"`
	}
	if err := a.postJSON(ctx, accessToken, "/messages/send", payload, &out); err != nil {
		return "", err
	}
	return out.ID, nil
}

// FindByMessageID looks a message up by its RFC822 identity via Gmail's
// rfc822msgid: search operator — the retransmission guard's only tool for
// telling "already sent" from "never sent" on a retry. The operator reads a
// search index, not a strongly consistent store: it is eventually consistent
// with a just-completed send, so a lookup landing before the index catches up
// returns a false negative — "never sent" — rather than proof the message was
// never sent.
func (a *httpAPI) FindByMessageID(ctx context.Context, accessToken, id string) (string, bool, error) {
	var out struct {
		Messages []struct {
			ID string `json:"id"`
		} `json:"messages"`
	}
	q := url.Values{"q": {"rfc822msgid:" + id}}
	if _, err := a.get(ctx, accessToken, "/messages", q, &out, maxJSONResponseBytes); err != nil {
		return "", false, err
	}
	if len(out.Messages) == 0 {
		return "", false, nil
	}
	return out.Messages[0].ID, true, nil
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
