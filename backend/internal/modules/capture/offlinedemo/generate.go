// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package offlinedemo

// What the demo's correspondence says, and how a message becomes a record the
// sink will accept.
//
// The threads are derived from the account's own state rather than picked at
// random: a company at Proposal has an offer thread, a customer has a kickoff,
// a contract about to run out has a renewal chase. That is what makes the
// inbox agree with the pipeline instead of being decoration beside it.
//
// Deterministic throughout. Message ids, dates and template choices are
// hashed from the account, so a re-sync emits the same conversation and the
// natural key refuses it. Nothing here reads the clock except through the
// account's own created_at, which the directory supplies.

import (
	"encoding/json"
	"fmt"
	"hash/fnv"
	"strings"
	"time"

	"github.com/gradionhq/margince/backend/internal/modules/capture"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/ports/connector"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
)

// maxBodyLen keeps a generated body inside what the activity write accepts.
const maxBodyLen = 4000

// message is one generated mail, and the shape stored in Raw so Normalize can
// rebuild the record from it alone.
type message struct {
	Mailbox     Mailbox   `json:"mailbox"`
	MessageID   string    `json:"message_id"`
	ThreadKey   string    `json:"thread_key"`
	InReplyTo   string    `json:"in_reply_to,omitempty"`
	Subject     string    `json:"subject"`
	Body        string    `json:"body"`
	OccurredAt  time.Time `json:"occurred_at"`
	Direction   string    `json:"direction"`
	Kind        string    `json:"kind"`
	FromAddr    string    `json:"from"`
	FromName    string    `json:"from_name"`
	ToAddr      string    `json:"to"`
	ToName      string    `json:"to_name"`
	CCAddr      string    `json:"cc,omitempty"`
	OrgID       string    `json:"organization_id"`
	DealID      string    `json:"deal_id,omitempty"`
	PersonEmail string    `json:"person_email,omitempty"`
}

// record maps one generated message onto what the sink accepts.
//
// Mirrors mailmap.ToRecord deliberately: the body carries a From/To header the
// way a captured mail's does, the counterparty is the human on the other side,
// and an OUTBOUND message is attested as sent by the mailbox owner — which is
// what makes it a sent copy rather than something we received from ourselves.
func (m message) record(mailbox Mailbox) connector.NormalizedRecord {
	header := fmt.Sprintf("From: %s\nTo: %s", m.FromAddr, m.ToAddr)
	if m.CCAddr != "" {
		header += "\nCc: " + m.CCAddr
	}
	body := header + "\n\n" + m.Body
	if len(body) > maxBodyLen {
		body = body[:maxBodyLen]
	}

	counterparty := m.ToAddr
	counterpartyName := m.ToName
	if m.Direction == "inbound" {
		counterparty, counterpartyName = m.FromAddr, m.FromName
	}

	addresses := []string{m.FromAddr, m.ToAddr}
	if m.CCAddr != "" {
		addresses = append(addresses, m.CCAddr)
	}

	// Links are explicit. The org always; the deal when the thread is about
	// one. NOT the person: the sink's counterparty ladder resolves and links
	// them, and a second link here would be a duplicate row.
	// An id that will not parse costs its link rather than the message: the
	// directory produced it from the database, so a bad one is a bug worth
	// seeing as a missing link rather than a lost thread.
	var links []datasource.EntityRef
	if orgID, err := ids.Parse(m.OrgID); err == nil {
		links = append(links, datasource.EntityRef{Type: datasource.EntityOrganization, ID: orgID})
	}
	if m.DealID != "" {
		if dealID, err := ids.Parse(m.DealID); err == nil {
			links = append(links, datasource.EntityRef{Type: datasource.EntityDeal, ID: dealID})
		}
	}

	raw, _ := json.Marshal(m) //nolint:errchkjson // a struct of strings and times cannot fail to marshal

	rec := connector.NormalizedRecord{
		EntityType: datasource.EntityActivity,
		NaturalKey: connector.NaturalKey{SourceSystem: Name, SourceID: m.MessageID},
		Fields: capture.ActivityFields{
			Kind:       m.Kind,
			Subject:    m.Subject,
			Body:       body,
			OccurredAt: m.OccurredAt,
			Direction:  m.Direction,
		},
		Source:     Name + ":" + m.MessageID,
		CapturedBy: "connector:" + Name,
		Raw:        raw,
		ThreadKey:  m.ThreadKey,
		Addresses:  addresses,
		Links:      links,
	}
	// A meeting has no counterparty, matching how the calendar connector maps
	// one: attendance is a participant list, not a correspondent.
	if m.Kind != "meeting" {
		rec.Counterparty = connector.Counterparty{
			Email:       strings.ToLower(counterparty),
			DisplayName: counterpartyName,
			Domain:      domainOf(counterparty),
			Direction:   m.Direction,
		}.WithOwnerAttestation(m.Direction == "outbound")
	}
	return rec
}

func domainOf(addr string) string {
	if i := strings.LastIndex(addr, "@"); i >= 0 {
		return strings.ToLower(addr[i+1:])
	}
	return ""
}

// historyDays is how far back an account's correspondence reaches.
//
// The anchor is NOT the organization's created_at, which is when the seeder
// wrote the row — today, for every company in a fresh installation. Anchoring
// there put every message in the FUTURE (created + 20 days), and a captured
// message that has not happened yet is refused: the generator produced six
// mails per customer and the database stayed empty, with nothing logged
// because the refusal is the sink doing its job.
//
// So the history runs BACKWARD from now instead. A thread that starts 90 days
// ago and ends last week is what an account worked for a quarter looks like.
const historyDays = 90

// generate writes one account's correspondence with this mailbox.
func generate(mailbox Mailbox, account Account) []message {
	if len(account.People) == 0 {
		// Nobody to write to. A thread addressed to a company rather than a
		// person is not correspondence, and inventing a contact here would
		// bypass the dataset's rule about where people come from.
		return nil
	}
	contact := account.People[hashIndex("contact:"+account.Domain, len(account.People))]
	if account.Now.IsZero() {
		return nil
	}
	// Backward from the run: the newest message lands a few days ago and the
	// oldest about a quarter back, so the timeline reads as a worked account
	// rather than as everything happening at once.
	anchor := account.Now.AddDate(0, 0, -historyDays)

	var out []message
	for _, thread := range threadsFor(account) {
		out = append(out, writeThread(mailbox, account, contact, anchor, thread)...)
	}
	return out
}

// threadSpec is one conversation to write: what it is about, and how it goes.
type threadSpec struct {
	Key      string // stable per account+thread
	Subject  string
	Opener   string // "outbound" or "inbound"
	Replies  int
	DayStart int // days after the account's anchor
	Deal     bool
	Meeting  bool
	Body     [3]string // opener, reply, follow-up
}

// threadsFor picks the conversations an account's own state calls for.
func threadsFor(account Account) []threadSpec {
	stage := strings.ToLower(dealStage(account))
	switch {
	case account.Lifecycle == "customer":
		return []threadSpec{
			{Key: "kickoff", Subject: "Kickoff " + account.Name, Opener: "outbound", Replies: 2,
				DayStart: 20, Deal: true, Meeting: true, Body: [3]string{
					"vielen Dank für Ihr Vertrauen. Anbei der Terminvorschlag für den Kickoff.",
					"passt uns gut, wir bringen die Fachbereiche mit.",
					"prima, Einladung ist raus. Agenda hängt an."}},
			{Key: "invoice", Subject: "Rechnung " + orDash(account.ContractNumber), Opener: "inbound", Replies: 1,
				DayStart: 60, Body: [3]string{
					"kurze Rückfrage zur letzten Rechnung — ist die Position 3 anteilig berechnet?",
					"ja, anteilig bis zum Periodenende. Ich schicke die Aufstellung mit.", ""}},
		}
	case account.Lifecycle == "former_customer":
		return []threadSpec{
			{Key: "offboarding", Subject: "Kündigung bestätigt", Opener: "outbound", Replies: 1,
				DayStart: 30, Body: [3]string{
					"wir bestätigen den Eingang Ihrer Kündigung zum Ende der Laufzeit.",
					"danke für die Bestätigung und die Zusammenarbeit.", ""}},
		}
	case stage == "proposal" || stage == "negotiation":
		return []threadSpec{
			{Key: "offer", Subject: "Angebot " + account.Name, Opener: "outbound", Replies: 2,
				DayStart: 10, Deal: true, Meeting: true, Body: [3]string{
					"anbei unser Angebot wie besprochen. Die Staffel greift ab 50 Lizenzen.",
					"danke — zwei Rückfragen zur Laufzeit und zum Support-Level.",
					"beides gerne im Termin, Vorschlag hängt an."}},
		}
	case stage != "":
		return []threadSpec{
			{Key: "intro", Subject: "Kurzer Austausch?", Opener: "outbound", Replies: 1,
				DayStart: 5, Deal: true, Body: [3]string{
					"wir arbeiten mit mehreren Häusern Ihrer Größe — lohnt ein kurzer Austausch?",
					"gerne, schicken Sie ein paar Slots.", ""}},
		}
	case account.Lifecycle == "prospect":
		return []threadSpec{
			{Key: "inbound", Subject: "Anfrage über die Website", Opener: "inbound", Replies: 1,
				DayStart: 8, Body: [3]string{
					"wir prüfen gerade Anbieter und würden gerne mehr erfahren.",
					"sehr gerne — ich melde mich mit zwei Terminvorschlägen.", ""}},
		}
	default:
		// A target nobody has worked. Most get nothing, which is the honest
		// majority; a few carry one unanswered outbound.
		if hashIndex("touch:"+account.Domain, 4) != 0 {
			return nil
		}
		return []threadSpec{
			{Key: "cold", Subject: "Kurze Frage zu Ihrem Shop", Opener: "outbound", Replies: 0,
				DayStart: 12, Body: [3]string{
					"eine kurze Frage zu Ihrer Plattform — haben Sie zehn Minuten?", "", ""}},
		}
	}
}

func dealStage(account Account) string {
	if len(account.Deals) == 0 {
		return ""
	}
	return account.Deals[0].Stage
}

// writeThread turns one spec into its messages, opener first so the sink's
// reply join sees an outbound before the inbound that answers it.
func writeThread(mailbox Mailbox, account Account, contact Person, anchor time.Time, spec threadSpec) []message {
	base := fmt.Sprintf("offline-demo.%s.%s", shortKey(account.Domain), spec.Key)
	openerID := fmt.Sprintf("<%s.m0@offline-demo.invalid>", base)
	occurred := anchor.AddDate(0, 0, spec.DayStart)

	var dealID string
	if spec.Deal && len(account.Deals) > 0 {
		dealID = account.Deals[0].ID
	}

	out := []message{newMessage(mailbox, account, contact, spec, openerID, openerID, "",
		spec.Subject, spec.Body[0], occurred, spec.Opener, "email", dealID)}

	direction := flip(spec.Opener)
	for reply := 1; reply <= spec.Replies && reply < len(spec.Body); reply++ {
		body := spec.Body[reply]
		if body == "" {
			break
		}
		occurred = occurred.AddDate(0, 0, 2+hashIndex(fmt.Sprintf("gap:%s:%d", base, reply), 4))
		id := fmt.Sprintf("<%s.m%d@offline-demo.invalid>", base, reply)
		out = append(out, newMessage(mailbox, account, contact, spec, id, openerID, openerID,
			"Re: "+spec.Subject, body, occurred, direction, "email", dealID))
		direction = flip(direction)
	}

	if spec.Meeting {
		occurred = occurred.AddDate(0, 0, 5)
		id := fmt.Sprintf("<%s.meet@offline-demo.invalid>", base)
		meeting := newMessage(mailbox, account, contact, spec, id, openerID, "",
			"Termin: "+spec.Subject, "Abstimmung, 45 Minuten, per Video.",
			occurred, "", "meeting", dealID)
		out = append(out, meeting)
	}
	return out
}

func newMessage(mailbox Mailbox, account Account, contact Person, spec threadSpec,
	id, threadKey, inReplyTo, subject, body string, occurred time.Time,
	direction, kind, dealID string,
) message {
	from, fromName := mailbox.Email, mailbox.DisplayName
	to, toName := contact.Email, contact.Name
	if direction == "inbound" {
		from, fromName, to, toName = contact.Email, contact.Name, mailbox.Email, mailbox.DisplayName
	}
	greeting := "Hallo " + firstWord(contact.Name) + ","
	if direction == "inbound" {
		greeting = "Hallo " + firstWord(mailbox.DisplayName) + ","
	}
	cc := ""
	// A CC on some threads, so the participant fan-out has more than two
	// parties to record.
	if mailbox.ColleagueEmail != "" && hashIndex("cc:"+id, 3) == 0 {
		cc = mailbox.ColleagueEmail
	}
	return message{
		Mailbox: mailbox, MessageID: id, ThreadKey: threadKey, InReplyTo: inReplyTo,
		Subject: subject, Body: greeting + "\n\n" + body + "\n\nViele Grüße",
		OccurredAt: occurred.UTC(), Direction: direction, Kind: kind,
		FromAddr: from, FromName: fromName, ToAddr: to, ToName: toName, CCAddr: cc,
		OrgID: account.OrganizationID, DealID: dealID, PersonEmail: contact.Email,
	}
}

func flip(direction string) string {
	if direction == "outbound" {
		return "inbound"
	}
	return "outbound"
}

func firstWord(s string) string {
	if i := strings.IndexByte(s, ' '); i > 0 {
		return s[:i]
	}
	return s
}

func orDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}

// shortKey is a domain reduced to something safe inside a Message-ID.
func shortKey(domain string) string {
	var b strings.Builder
	for _, r := range domain {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r >= 'A' && r <= 'Z':
			b.WriteRune(r + 32)
		}
	}
	if b.Len() == 0 {
		return "unknown"
	}
	return b.String()
}

// hashIndex spreads a key across n buckets, stably across runs and machines.
func hashIndex(key string, n int) int {
	if n <= 0 {
		return 0
	}
	h := fnv.New32a()
	_, _ = h.Write([]byte(key)) //nolint:errcheck // hash.Write never returns an error
	return int(h.Sum32() % uint32(n))
}
