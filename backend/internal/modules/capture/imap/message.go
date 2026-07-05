// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package imap

// The pure RFC822 → activity mapping: no provider handle, no I/O beyond
// reading the in-memory message bytes. This is the test-guarded surface —
// the connector's Sync and Normalize both compose these functions, so the
// classification (direction, skip rules) and the field mapping are proven by
// fixtures, not a live mailbox.

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

// parsedMessage is the pure result of reading one RFC822 message against
// the mailbox owner — everything the mapping needs, with no provider handle.
type parsedMessage struct {
	messageID    string
	subject      string
	body         string
	occurredAt   time.Time
	direction    string // inbound | outbound
	from         string
	to           string
	counterparty string
	autoSubmit   bool
}

var htmlTag = regexp.MustCompile(`(?s)<[^>]*>`)

// parseMessage reads the headers and the text body of one message and
// classifies its direction relative to the mailbox owner.
func parseMessage(raw []byte, owner string) (parsedMessage, error) {
	reader, err := mail.CreateReader(bytes.NewReader(raw))
	if err != nil {
		return parsedMessage{}, fmt.Errorf("imap: parsing message: %w", err)
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
	direction := "inbound"
	counterparty := from
	if strings.ToLower(from) == ownerLower && ownerLower != "" {
		direction = "outbound"
		counterparty = firstNonOwner(toList, ownerLower)
	}

	autoSubmit := isAutoSubmitted(header.Get("Auto-Submitted"), header.Get("Precedence"))

	return parsedMessage{
		messageID:    strings.TrimSpace(messageID),
		subject:      strings.TrimSpace(subject),
		body:         body,
		occurredAt:   occurredAt,
		direction:    direction,
		from:         from,
		to:           to,
		counterparty: counterparty,
		autoSubmit:   autoSubmit,
	}, nil
}

// skipReason names why a message is intentionally dropped, or reports that
// it should be captured. The rule set keeps automated/system noise off the
// timeline: no stable id, no sender, auto-submitted, or a machine sender.
func (m parsedMessage) skipReason() (string, bool) {
	if m.messageID == "" {
		return "no Message-ID", true
	}
	if m.from == "" {
		return "no From address", true
	}
	if m.autoSubmit {
		return "auto-submitted", true
	}
	if isMachineSender(m.from) {
		return "automated sender", true
	}
	return "", false
}

// toRecord builds the provenance-stamped activity record. The counterparty
// (From/To) is folded into a compact header on the body — the activity
// schema has no dedicated participant column, and the timeline needs to
// show who the mail was with.
func (m parsedMessage) toRecord(raw []byte) connector.NormalizedRecord {
	source := "imap:" + m.messageID
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
	}
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

// isMachineSender flags the common no-reply / bounce localparts that carry
// no human counterparty worth a CRM row.
func isMachineSender(addr string) bool {
	local, _, found := strings.Cut(strings.ToLower(addr), "@")
	if !found {
		return false
	}
	local = strings.ReplaceAll(local, ".", "")
	local = strings.ReplaceAll(local, "-", "")
	switch local {
	case "noreply", "donotreply", "mailerdaemon", "postmaster", "bounce", "bounces", "notifications", "notification":
		return true
	}
	return false
}

// isAutoSubmitted reads the RFC 3834 Auto-Submitted header and the legacy
// Precedence hint: either marks machine-generated mail.
func isAutoSubmitted(autoSubmitted, precedence string) bool {
	v := strings.ToLower(strings.TrimSpace(autoSubmitted))
	if v != "" && v != "no" {
		return true
	}
	switch strings.ToLower(strings.TrimSpace(precedence)) {
	case "bulk", "list", "junk", "auto_reply":
		return true
	}
	return false
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
