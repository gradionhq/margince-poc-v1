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
	counterpartyName string // display name from the counterparty's header — untrusted text
	threadKey        string // conversation identity: References root / In-Reply-To / own Message-ID
	autoReply        bool   // a reply nobody chose to write: kept off the timeline
	// machineTouched is the BROADER question — did any machine have a hand in
	// this? It never drops a message; it only refuses the outbound attestation,
	// so a responder's reply cannot vouch for an address the owner never chose.
	machineTouched  bool
	listUnsubscribe bool // an RFC 2369 List-Unsubscribe header — transactional-gate corroboration
	sentByOwner     bool // the PROVIDER attested the owner sent this — set by AttestSentByOwner, never parsed
	// participants are everyone on To and Cc who is neither the mailbox owner
	// nor the counterparty — the two ends already have their own rows.
	participants []connector.MessageParticipant
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
	m.sentByOwner = sent && !m.machineTouched
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
	// A malformed Cc line yields no addresses rather than failing the message:
	// the mail is already read off the wire, and losing the CCs is a smaller
	// loss than dropping the correspondence.
	ccList, _ := header.AddressList("Cc")
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
	machineTouched := isMachineTouched(autoSubmitted, precedence, hasMachineHandledHeader(header))

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
		autoReply:        autoReply,
		machineTouched:   machineTouched,
		listUnsubscribe:  strings.TrimSpace(header.Get("List-Unsubscribe")) != "",
		participants:     otherParties(toList, ccList, ownerLower, counterparty),
	}, nil
}

// ParticipantsOf reads the further parties out of one stored original.
//
// The replay pass calls it for messages captured before participants were
// recorded. It is a narrow seam on purpose: the pass wants exactly the CC and
// To names, and giving it Parse's whole Message would invite it to re-derive
// direction or subject from headers the activity row already settled at
// capture time.
func ParticipantsOf(raw []byte, owner string) ([]connector.MessageParticipant, error) {
	msg, err := Parse(raw, owner)
	if err != nil {
		return nil, err
	}
	return msg.participants, nil
}

// maxParticipants bounds how many further parties one message may contribute.
//
// The cap is not a performance guard, it is a shape guard. A message with two
// hundred addresses on its To line is a mailing list, and every name on it is
// evidence of a list membership rather than of a conversation — folding those
// into the interaction graph would report a relationship with everybody who
// ever received the same newsletter. Past the cap the further parties are
// dropped entirely rather than truncated, because half a distribution list is
// no more meaningful than all of it.
const maxParticipants = 50

// otherParties returns everyone on To and Cc who is neither the mailbox owner
// nor the counterparty.
//
// Both exclusions matter and for different reasons. The owner and the
// counterparty are the two ends of the exchange and already get their own
// rows, stamped from the connection rather than from a header — a second row
// for either would either collide with the uniqueness index or, worse, record
// the same human twice under two roles.
//
// To wins over Cc when an address appears on both, which is a real thing
// senders do: a direct recipient who is also copied was addressed directly,
// and that is the stronger claim about their part in the conversation.
func otherParties(toList, ccList []*mail.Address, ownerLower, counterparty string) []connector.MessageParticipant {
	counterpartyLower := strings.ToLower(strings.TrimSpace(counterparty))
	seen := map[string]bool{ownerLower: true, counterpartyLower: true}
	delete(seen, "")

	var out []connector.MessageParticipant
	add := func(list []*mail.Address, role string) {
		for _, a := range list {
			address := strings.ToLower(strings.TrimSpace(a.Address))
			if address == "" || seen[address] {
				continue
			}
			seen[address] = true
			out = append(out, connector.MessageParticipant{Email: address, Role: role})
		}
	}
	add(toList, connector.ParticipantRoleTo)
	add(ccList, connector.ParticipantRoleCC)

	if len(out) > maxParticipants {
		return nil
	}
	return out
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
		Counterparty: connector.Counterparty{
			Email:           strings.ToLower(strings.TrimSpace(m.counterparty)),
			DisplayName:     m.counterpartyName,
			Domain:          domainOf(m.counterparty),
			Direction:       m.direction,
			ListUnsubscribe: m.listUnsubscribe,
		}.WithOwnerAttestation(m.sentByOwner),
		ThreadKey:    m.threadKey,
		Participants: m.participants,
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
