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
	autoReply        bool     // a reply nobody chose to write: kept off the timeline
	// autoish is the BROADER question — did any machine have a hand in this?
	// It never drops a message; it only refuses the outbound attestation, so a
	// responder's reply cannot vouch for an address the owner never chose.
	autoish         bool
	listUnsubscribe bool // an RFC 2369 List-Unsubscribe header — transactional-gate corroboration
	sentByOwner     bool // the PROVIDER attested the owner sent this — set by AttestSentByOwner, never parsed
}

// AttestSentByOwner returns a copy carrying the provider's own attestation
// that the authenticated mailbox owner sent this message — Gmail's SENT
// label, an IMAP \Sent special-use mailbox, Microsoft's SentItems folder.
// The signal cannot come from Parse: every header this package reads is
// attacker-controlled, and the T1 correspondence-positive gate (ADR-0072 §1)
// treats a sent message as affirmative intent toward its recipient. Only a
// connector holding an authenticated provider handle can vouch for it.
func (m Message) AttestSentByOwner(sent bool) Message {
	// A machine-touched message never attests, however the provider filed it.
	// An autoresponder's reply IS genuinely owner-authored and genuinely in
	// Sent, so nothing downstream could tell it from correspondence the owner
	// chose — and it would spare an address the owner never chose to write to
	// (ADR-0072 residual (b)).
	m.sentByOwner = sent && !m.autoish
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

	autoSubmitted, precedence := header.Values("Auto-Submitted"), header.Values("Precedence")
	autoReply := isAutoReply(autoSubmitted, precedence)
	autoish := isMachineTouched(autoSubmitted, precedence, header.Has("X-Autoreply"), header.Has("X-Autorespond"))

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
		autoish:          autoish,
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
// timeline: no stable id, no sender, an auto-reply, or the delivery system
// itself.
func (m Message) SkipReason() (string, bool) {
	if m.messageID == "" {
		return "no Message-ID", true
	}
	if m.from == "" {
		return "no From address", true
	}
	if m.autoReply {
		return autoReplied, true
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
	// BATV/prvs signs the RETURN PATH, so the tag sits before any separator a
	// normalization would strip.
	if strings.HasPrefix(local, "prvs=") || strings.HasPrefix(local, "msprvs1=") {
		return true
	}
	// VERP encodes the original recipient into the bounce address
	// (`bounces-12345@`, `bounce+tag@`), so the trailing tag is dropped before
	// matching. `_` is stripped for parity with the T2 registry's own reading
	// of these localparts (CAP-PARAM-6) — one spelling, two rules.
	local, _, _ = strings.Cut(local, "+")
	local = strings.NewReplacer(".", "", "-", "", "_", "").Replace(local)
	local = strings.TrimRight(local, "0123456789")
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
// Deliberately narrower than RFC 3834's whole vocabulary. Transactional mail
// must reach the tier gate: `auto-generated` covers the signed envelopes,
// invoices and shipping notices ADR-0072 §1 keeps on the timeline, and
// `Precedence: bulk`/`list` is what a newsletter carries. The gate cannot judge
// what it never sees.
//
// Auto-replies stay dropped, and that is load-bearing beyond noise: an
// autoresponder answering a stranger produces a genuine owner-authored message,
// which is the one shape that could induce a T1 correspondence spare for an
// address nobody chose to write to (ADR-0072 residual (b)).
// The two RFC 3834 values that mean a person's mail reaches the tier gate:
// `no` is not automatic at all, and `auto-generated` is mail a system
// originated on its own — an invoice, a notice, a signed envelope — as opposed
// to mail it generated in reply to something we sent.
const (
	notAutomatic  = "no"
	autoGenerated = "auto-generated"
	// autoReplied is the RFC 3834 keyword, the legacy Precedence spelling, and
	// the reason SkipReason gives — one string because they name one thing.
	autoReplied = "auto-reply"
)

// reachesGate reports whether an Auto-Submitted value names mail the tier gate
// should judge. RFC 3834 §5 allows parameters after a semicolon
// (`auto-replied; owner-email=…`) and RFC 5322 allows comments around the value
// (`auto-generated (invoice)`), so only the keyword decides.
func reachesGate(autoSubmitted string) bool {
	keyword, _, _ := strings.Cut(stripHeaderComments(autoSubmitted), ";")
	keyword = strings.TrimSpace(keyword)
	return strings.EqualFold(keyword, notAutomatic) || strings.EqualFold(keyword, autoGenerated)
}

func isAutoReply(autoSubmitted, precedence []string) bool {
	for _, v := range autoSubmitted {
		// Every occurrence is read, not the topmost: a relay that PREPENDS its
		// own `auto-generated` would otherwise mask the responder's
		// `auto-replied` sitting underneath it.
		if !reachesGate(v) {
			return true
		}
	}
	for _, v := range precedence {
		switch strings.ToLower(strings.TrimSpace(stripHeaderComments(v))) {
		case "auto_reply", autoReplied, "auto_replied", "auto-replied":
			return true
		}
	}
	return false
}

// isMachineTouched reports whether ANY machine hand shows on this message —
// including the bulk-family markers a newsletter carries, which isAutoReply
// deliberately lets through so the tier gate can judge them.
//
// The two questions are separate because they pull opposite ways on the same
// headers. Keeping transactional mail on the timeline means the drop filter has
// to be narrow; refusing to let an autoresponder vouch for a stranger means the
// attestation veto has to be wide. One boolean serving both is why narrowing
// the drop kept re-opening the spare. This one only ever withholds evidence.
func isMachineTouched(autoSubmitted, precedence []string, autoreplyHeaders ...bool) bool {
	for _, present := range autoreplyHeaders {
		if present {
			return true
		}
	}
	for _, v := range autoSubmitted {
		if !strings.EqualFold(strings.TrimSpace(stripHeaderComments(v)), notAutomatic) {
			return true
		}
	}
	for _, v := range precedence {
		switch strings.ToLower(strings.TrimSpace(stripHeaderComments(v))) {
		case "bulk", "list", "junk", "auto_reply", autoReplied, "auto_replied", "auto-replied":
			return true
		}
	}
	return false
}

// stripHeaderComments removes RFC 5322 comments — parenthesized runs, which may
// nest and may contain quoted pairs. Header values are attacker-shaped text, so
// this walks the bytes rather than trusting a pattern: an unclosed parenthesis
// swallows the rest, which keeps a malformed value from resolving to a keyword
// it does not carry.
//
// A `(` inside a quoted-string is NOT distinguished from a real comment. That
// is safe for both callers, which read only the keyword before the first
// semicolon, and it fails toward swallowing rather than inventing a keyword.
func stripHeaderComments(value string) string {
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
