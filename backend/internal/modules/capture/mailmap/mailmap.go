// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// Package mailmap is the pure RFC822 → activity mapping shared by every
// mail-capture connector (imap, gmail): no provider handle, no I/O beyond
// reading the in-memory message bytes. This is the test-guarded surface —
// a connector's Sync and Normalize compose these functions, so the
// classification (direction, skip rules) and the field mapping are proven
// by fixtures, not a live mailbox. ToRecord is parameterised by the
// connector name so the same mapping stamps whichever connector read the
// message onto the row's provenance.
package mailmap

import (
	"bytes"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/emersion/go-message/mail"

	"github.com/gradionhq/margince/backend/internal/modules/capture"
	"github.com/gradionhq/margince/backend/internal/shared/ports/connector"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
)

// maxBodyLen caps the stored email body — the timeline needs a legible
// excerpt, not the full multi-megabyte thread with quoted history.
const maxBodyLen = 8000

// Message is the pure result of reading one RFC822 message against the
// mailbox owner — everything the mapping needs, with no provider handle.
type Message struct {
	messageID        string
	subject          string
	body             string
	occurredAt       time.Time
	direction        string // inbound | outbound
	from             string
	to               string
	counterparty     string
	counterpartyName string   // display name from the counterparty's header — untrusted text
	threadKey        string   // conversation identity: References root / In-Reply-To / own Message-ID
	senderDomain     string   // lowercased domain of From — for the RC-2 gate
	recipientDomains []string // lowercased, de-duped domains of every To — for the RC-2 gate
	autoReply        bool     // generated in RESPONSE to us — nobody chose to write it
	listUnsubscribe  bool     // an RFC 2369 List-Unsubscribe header — transactional-gate corroboration
	sentByOwner      bool     // the PROVIDER attested the owner sent this — set by AttestSentByOwner, never parsed
}

// AttestSentByOwner returns a copy carrying the provider's own attestation
// that the authenticated mailbox owner sent this message — Gmail's SENT
// label, an IMAP \Sent special-use mailbox, Microsoft's SentItems folder.
// The signal cannot come from Parse: every header this package reads is
// attacker-controlled, and the T1 correspondence-positive gate (ADR-0072 §1)
// treats a sent message as affirmative intent toward its recipient. Only a
// connector holding an authenticated provider handle can vouch for it.
func (m Message) AttestSentByOwner(sent bool) Message {
	m.sentByOwner = sent
	return m
}

// Counterparty is the non-owner address on the message (the person this
// mail was with) — exported so a connector can tally distinct contacts.
func (m Message) Counterparty() string { return m.counterparty }

// ThreadKey is the conversation identity this message belongs to.
func (m Message) ThreadKey() string { return m.threadKey }

var htmlTag = regexp.MustCompile(`(?s)<[^>]*>`)

// Parse reads the headers and the text body of one message and classifies
// its direction relative to the mailbox owner.
func Parse(raw []byte, owner string) (Message, error) {
	reader, err := mail.CreateReader(bytes.NewReader(raw))
	if err != nil {
		return Message{}, fmt.Errorf("mailmap: parsing message: %w", err)
	}
	header := reader.Header

	messageID, _ := header.MessageID()
	subject, _ := header.Subject()
	occurredAt, _ := header.Date()

	fromList, _ := header.AddressList("From")
	toList, _ := header.AddressList("To")
	from := firstAddress(fromList)
	to := firstAddress(toList)

	body := extractText(reader)

	ownerLower := strings.ToLower(strings.TrimSpace(owner))
	direction := connector.DirectionInbound
	counterparty := from
	counterpartyName := displayName(fromList, counterparty)
	if strings.ToLower(from) == ownerLower && ownerLower != "" {
		direction = connector.DirectionOutbound
		counterparty = firstNonOwner(toList, ownerLower)
		counterpartyName = displayName(toList, counterparty)
	}

	autoReply := isAutoReply(header.Has("Auto-Submitted"), header.Get("Auto-Submitted"), header.Get("Precedence"))

	return Message{
		messageID:        strings.TrimSpace(messageID),
		subject:          strings.TrimSpace(subject),
		body:             body,
		occurredAt:       occurredAt,
		direction:        direction,
		from:             from,
		to:               to,
		counterparty:     counterparty,
		counterpartyName: counterpartyName,
		threadKey:        threadKey(header.Get("References"), header.Get("In-Reply-To"), messageID),
		senderDomain:     domainOf(from),
		recipientDomains: domainsOf(toList),
		autoReply:        autoReply,
		listUnsubscribe:  strings.TrimSpace(header.Get("List-Unsubscribe")) != "",
	}, nil
}

// threadKey derives the conversation identity from the standard reply
// headers: the References ROOT (its first id — stable across every reply in
// the thread), else In-Reply-To, else the message's own id (a fresh thread
// is rooted at its opener, so later replies referencing it join it). Never
// a subject heuristic — "Re: Invoice" joining unrelated threads is worse
// than no join (CAP-FORMULA-1's no-subject-fallback rule).
func threadKey(references, inReplyTo, messageID string) string {
	if refs := strings.Fields(references); len(refs) > 0 {
		return trimAngle(refs[0])
	}
	if irt := strings.TrimSpace(inReplyTo); irt != "" {
		return trimAngle(irt)
	}
	return trimAngle(strings.TrimSpace(messageID))
}

// trimAngle strips the RFC822 angle brackets off a message id.
func trimAngle(id string) string {
	return strings.TrimSuffix(strings.TrimPrefix(id, "<"), ">")
}

// displayName returns the header display name for addr from list, "" when
// the header carried none. The value is whatever the sender typed — hostile
// input until a consumer sanitizes it.
func displayName(list []*mail.Address, addr string) string {
	for _, a := range list {
		if strings.EqualFold(a.Address, addr) {
			return strings.TrimSpace(a.Name)
		}
	}
	return ""
}

// SkipReason names why a message is intentionally dropped, or reports that
// it should be captured. The rule set keeps automated/system noise off the
// timeline: no stable id, no sender, auto-submitted, or a machine sender.
func (m Message) SkipReason() (string, bool) {
	if m.messageID == "" {
		return "no Message-ID", true
	}
	if m.from == "" {
		return "no From address", true
	}
	if m.autoReply {
		return "auto-reply", true
	}
	if isDeliverySystemSender(m.from) {
		return "delivery-system sender", true
	}
	return "", false
}

// ID is the RFC822 Message-ID — the idempotency source id every mail
// connector keys on (data-model §7/§8).
func (m Message) ID() string { return m.messageID }

// ToRecord builds the provenance-stamped activity record for the connector
// named connectorName (e.g. "imap", "gmail"): NaturalKey.SourceSystem and
// the Source/CapturedBy prefixes all carry that name, so the same message
// read over a different transport is still deduped on (name, Message-ID).
// The counterparty (From/To) is folded into a compact header on the body —
// the activity schema has no dedicated participant column, and the timeline
// needs to show who the mail was with.
func (m Message) ToRecord(connectorName string, raw []byte) connector.NormalizedRecord {
	source := connectorName + ":" + m.messageID
	header := fmt.Sprintf("From: %s\nTo: %s", orDash(m.from), orDash(m.to))
	body := header
	if m.body != "" {
		body = header + "\n\n" + m.body
	}
	body = truncate(body, maxBodyLen)

	return connector.NormalizedRecord{
		EntityType: datasource.EntityActivity,
		NaturalKey: connector.NaturalKey{SourceSystem: connectorName, SourceID: m.messageID},
		Fields: capture.ActivityFields{
			Kind:       "email",
			Subject:    m.subject,
			Body:       body,
			OccurredAt: m.occurredAt,
			Direction:  m.direction,
		},
		Source:     source,
		CapturedBy: "connector:" + connectorName,
		Raw:        raw,
		// The RC-2 exclusion gate reads these in the ONE Sink before any
		// write; labels are left empty here (RFC822 carries none — a
		// provider label feed is a follow-up), so only domain rules bite
		// on mail read over imap/gmail-raw today.
		Match: connector.ExclusionAttrs{
			SenderDomain:     m.senderDomain,
			RecipientDomains: m.recipientDomains,
		},
		Counterparty: connector.Counterparty{
			Email:           strings.ToLower(strings.TrimSpace(m.counterparty)),
			DisplayName:     m.counterpartyName,
			Domain:          domainOf(m.counterparty),
			Direction:       m.direction,
			ListUnsubscribe: m.listUnsubscribe,
		}.WithOwnerAttestation(m.sentByOwner),
		ThreadKey: m.threadKey,
	}
}

// domainOf returns the lowercased domain part of an address, or "" if the
// address carries no "@". It splits at the LAST "@" so a quoted local part
// containing one (e.g. `"weird@local"@example.com`) still yields the domain.
func domainOf(addr string) string {
	addr = strings.ToLower(strings.TrimSpace(addr))
	if idx := strings.LastIndex(addr, "@"); idx >= 0 {
		return addr[idx+1:]
	}
	return ""
}

// domainsOf returns the lowercased, de-duplicated domains of an address
// list, order-preserving — every recipient's domain the RC-2 gate may match.
func domainsOf(list []*mail.Address) []string {
	var out []string
	seen := map[string]struct{}{}
	for _, a := range list {
		d := domainOf(a.Address)
		if d == "" {
			continue
		}
		if _, dup := seen[d]; dup {
			continue
		}
		seen[d] = struct{}{}
		out = append(out, d)
	}
	return out
}

// extractText returns the message's plain-text body. It prefers a
// text/plain part; falling back to a crude tag-strip of text/html only when
// no plain part exists, so an HTML-only newsletter still yields readable text.
func extractText(reader *mail.Reader) string {
	var plain, html string
	for {
		part, err := reader.NextPart()
		if err != nil {
			// io.EOF (and any structural read error) ends the walk; whatever
			// text was already collected stands.
			break
		}
		inline, ok := part.Header.(*mail.InlineHeader)
		if !ok {
			continue
		}
		contentType, _, err := inline.ContentType()
		if err != nil {
			continue
		}
		content, err := io.ReadAll(part.Body)
		if err != nil {
			continue
		}
		switch {
		case strings.HasPrefix(contentType, "text/plain") && plain == "":
			plain = string(content)
		case strings.HasPrefix(contentType, "text/html") && html == "":
			html = string(content)
		}
	}
	if strings.TrimSpace(plain) != "" {
		return strings.TrimSpace(plain)
	}
	if html != "" {
		return strings.TrimSpace(htmlTag.ReplaceAllString(html, " "))
	}
	return ""
}

func firstAddress(list []*mail.Address) string {
	if len(list) == 0 {
		return ""
	}
	return list[0].Address
}

func firstNonOwner(list []*mail.Address, ownerLower string) string {
	for _, a := range list {
		if strings.ToLower(a.Address) != ownerLower {
			return a.Address
		}
	}
	return firstAddress(list)
}

// isDeliverySystemSender flags mail from the message-transport system itself —
// a bounce, a DSN, a postmaster notice. There is no correspondent behind it and
// nothing on the other end to have a relationship with, so it is dropped before
// anything is written.
//
// Deliberately NARROWER than "looks automated". A no-reply or notifications
// address is a real organization writing to the workspace — a signed envelope,
// an invoice, a shipping notice — and ADR-0072 §1 is explicit that such a
// message keeps its place on the timeline while the tier gate suppresses the
// person and company derivation. Dropping it here would make that promise false
// and would starve the T2 corroboration rule (CAP-PARAM-6), which exists to
// recognize exactly these machine localparts, of the mail it judges.
func isDeliverySystemSender(addr string) bool {
	local, _, found := strings.Cut(strings.ToLower(addr), "@")
	if !found {
		return false
	}
	local = strings.ReplaceAll(local, ".", "")
	local = strings.ReplaceAll(local, "-", "")
	switch local {
	case "mailerdaemon", "postmaster", "bounce", "bounces":
		return true
	}
	return false
}

// isAutoReply reads the RFC 3834 Auto-Submitted header and the legacy
// Precedence hint for mail generated IN RESPONSE to something the workspace
// sent — a vacation responder, an auto-reply. Nobody chose to write it, so it
// is dropped before anything is written.
//
// Deliberately narrower than RFC 3834's whole vocabulary. `auto-generated`
// covers the signed envelopes, invoices and shipping notices ADR-0072 §1 keeps
// on the timeline, and `Precedence: bulk`/`list` is what a newsletter carries —
// the very corroboration the T2 registry reads (CAP-PARAM-6). Dropping those
// here would re-open, through a second door, the contradiction that narrowing
// the machine-sender filter closed: the tier gate cannot judge mail it never
// sees.
//
// Auto-replies stay dropped, and that is load-bearing beyond noise: an
// autoresponder answering a stranger produces a genuine owner-authored message,
// which is the one shape that could induce a T1 correspondence spare for an
// address nobody chose to write to (ADR-0072 residual (b)).
func isAutoReply(hasAutoSubmitted bool, autoSubmitted, precedence string) bool {
	// RFC 3834 §5: the value may carry parameters after a semicolon
	// (`auto-replied; owner-email=…`), and RFC 5322 permits comments anywhere
	// around it (`auto-generated (invoice)`). Match the keyword alone — with
	// the default below being "treat as a reply", a comment left in place would
	// silently discard the ordinary and transactional mail this filter exists
	// to let through.
	keyword, _, _ := strings.Cut(stripComments(autoSubmitted), ";")
	switch {
	case !hasAutoSubmitted:
		// No such header, which is nearly all mail: a person wrote it. Presence
		// is asked of the header set, not inferred from the value — a header
		// written with no value at all is malformed, not missing, and the two
		// are indistinguishable once both have collapsed to "".
	case strings.EqualFold(strings.TrimSpace(keyword), "no"):
		// Explicitly not automatic.
	case strings.EqualFold(strings.TrimSpace(keyword), autoGenerated):
		// The signed envelopes and notices ADR-0072 §1 keeps on the timeline.
	default:
		// Everything else is treated as a reply: `auto-replied`,
		// `auto-notified`, an extension token a future RFC or a provider
		// invents — and a header that is PRESENT but yields no keyword at all,
		// which an unclosed comment produces. Absent and unreadable are not the
		// same claim, and only the first of them means a person wrote this.
		return true
	}

	switch strings.ToLower(strings.TrimSpace(stripComments(precedence))) {
	case "junk", "auto_reply":
		return true
	}
	return false
}

// stripComments removes RFC 5322 comments — parenthesized runs, which may nest
// and may contain quoted pairs — leaving the header's semantic value. Header
// values are attacker-shaped text, so this walks the bytes rather than trusting
// a pattern: an unclosed parenthesis simply swallows the rest, which keeps a
// malformed value from resolving to a keyword it does not carry.
func stripComments(value string) string {
	var out strings.Builder
	depth := 0
	for i := 0; i < len(value); i++ {
		switch c := value[i]; {
		case c == '\\' && depth > 0 && i+1 < len(value):
			i++ // quoted-pair inside a comment: the escaped byte is comment text
		case c == '(':
			depth++
		case c == ')' && depth > 0:
			depth--
		default:
			if depth == 0 {
				out.WriteByte(c)
			}
		}
	}
	return strings.TrimSpace(out.String())
}

// autoGenerated is the RFC 3834 value for mail a system originated on its own —
// an invoice, a shipping notice, a signed envelope — as opposed to mail it
// generated in reply to something we sent.
const autoGenerated = "auto-generated"

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func truncate(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	// Back off to a rune boundary so the stored excerpt is never a broken
	// UTF-8 sequence.
	cut := limit
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "…"
}
