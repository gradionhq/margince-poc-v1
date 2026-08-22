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

// The two directions a message travels, as the activity contract spells them.
const (
	directionInbound  = "inbound"
	directionOutbound = "outbound"
)

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
func (m message) record() connector.NormalizedRecord {
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
	if m.Direction == directionInbound {
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
	//
	// The outbound OWNER ATTESTATION is deliberately NOT minted here.
	// WithOwnerAttestation is the T1 correspondence gate's only evidence
	// (ADR-0072 §1) and may be called solely by the mail mapper, which knows
	// both the message's authorship and the provider's own filing of it. A
	// generator deriving it from its own content is precisely the hole the
	// rule closes: whatever supplied the argument could whitelist an
	// arbitrary address past transactional suppression.
	//
	// The cost is that a generated outbound reads as correspondence without
	// T1 evidence, which is honest — nobody filed these in a Sent folder,
	// because nobody sent them.
	if m.Kind != "meeting" {
		rec.Counterparty = connector.Counterparty{
			Email:       strings.ToLower(counterparty),
			DisplayName: counterpartyName,
			Domain:      domainOf(counterparty),
			Direction:   m.Direction,
		}
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
	Opener   string // directionOutbound or directionInbound
	Replies  int
	DayStart int // days after the account's anchor
	Deal     bool
	Meeting  bool
	Body     [3]string // opener, reply, follow-up
}

// threadsFor picks the conversations an account's own state calls for.
func threadsFor(account Account) []threadSpec {
	locale := localeFor(account)
	words := wordsFor(locale)
	// One helper so every spec below states only what differs. The subject and
	// the body come from the same locale, which is what stops a German subject
	// wrapping a Korean company name — the shape this generator shipped with.
	spec := func(key, subject string, opener string, replies, dayStart int,
		deal, meeting bool,
	) threadSpec {
		return threadSpec{
			Key: key, Subject: subject, Opener: opener, Replies: replies,
			DayStart: dayStart, Deal: deal, Meeting: meeting,
			Body: bodiesFor(locale, key),
		}
	}

	stage := strings.ToLower(dealStage(account))
	switch {
	case account.Lifecycle == "customer":
		return []threadSpec{
			spec(threadKickoff, words.Kickoff+" "+account.Name, directionOutbound, 2, 20, true, true),
			spec(threadInvoice, words.Invoice+" "+orDash(account.ContractNumber), directionInbound, 1, 60, false, false),
		}
	case account.Lifecycle == "former_customer":
		return []threadSpec{
			spec(threadOffboarding, words.Offboarding, directionOutbound, 1, 30, false, false),
		}
	case stage == "proposal" || stage == "negotiation":
		return []threadSpec{
			spec(threadOffer, words.Offer+" "+account.Name, directionOutbound, 2, 10, true, true),
		}
	case stage != "":
		return []threadSpec{
			spec(threadIntro, words.Intro, directionOutbound, 1, 5, true, false),
		}
	case account.Lifecycle == "prospect":
		return []threadSpec{
			spec(threadInbound, words.Enquiry, directionInbound, 1, 8, false, false),
		}
	default:
		// A target nobody has worked. Most get nothing, which is the honest
		// majority; a few carry one unanswered outbound.
		if hashIndex("touch:"+account.Domain, 4) != 0 {
			return nil
		}
		return []threadSpec{
			spec(threadCold, words.Cold, directionOutbound, 0, 12, false, false),
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

	out := []message{newMessage(mailbox, account, contact, openerID, openerID, "",
		spec.Subject, spec.Body[0], occurred, spec.Opener, "email", dealID)}

	direction := flip(spec.Opener)
	for reply := 1; reply <= spec.Replies && reply < len(spec.Body); reply++ {
		body := spec.Body[reply]
		if body == "" {
			break
		}
		occurred = occurred.AddDate(0, 0, 2+hashIndex(fmt.Sprintf("gap:%s:%d", base, reply), 4))
		id := fmt.Sprintf("<%s.m%d@offline-demo.invalid>", base, reply)
		out = append(out, newMessage(mailbox, account, contact, id, openerID, openerID,
			"Re: "+spec.Subject, body, occurred, direction, "email", dealID))
		direction = flip(direction)
	}

	if spec.Meeting {
		occurred = occurred.AddDate(0, 0, 5)
		id := fmt.Sprintf("<%s.meet@offline-demo.invalid>", base)
		words := wordsFor(localeFor(account))
		meeting := newMessage(mailbox, account, contact, id, openerID, "",
			words.Meeting+": "+spec.Subject, words.MeetingBody,
			occurred, "", "meeting", dealID)
		out = append(out, meeting)
	}
	return out
}

func newMessage(mailbox Mailbox, account Account, contact Person,
	id, threadKey, inReplyTo, subject, body string, occurred time.Time,
	direction, kind, dealID string,
) message {
	from, fromName := mailbox.Email, mailbox.DisplayName
	to, toName := contact.Email, contact.Name
	if direction == directionInbound {
		from, fromName, to, toName = contact.Email, contact.Name, mailbox.Email, mailbox.DisplayName
	}
	words := wordsFor(localeFor(account))
	addressee := firstWord(contact.Name)
	if direction == directionInbound {
		addressee = firstWord(mailbox.DisplayName)
	}
	// A nameless addressee would render " 님께," with a leading space in Korean
	// and "Hallo ," in German. The compose directory filters out people with no
	// name, so this is a guard on the Directory CONTRACT rather than on the one
	// implementation, and it drops the salutation line rather than greeting
	// nobody.
	greeting := ""
	if addressee != "" {
		greeting = words.Greeting(addressee) + "\n\n"
	}
	cc := ""
	// A CC on some threads, so the participant fan-out has more than two
	// parties to record.
	if mailbox.ColleagueEmail != "" && hashIndex("cc:"+id, 3) == 0 {
		cc = mailbox.ColleagueEmail
	}
	return message{
		Mailbox: mailbox, MessageID: id, ThreadKey: threadKey, InReplyTo: inReplyTo,
		Subject: subject, Body: greeting + body + "\n\n" + words.SignOff,
		OccurredAt: occurred.UTC(), Direction: direction, Kind: kind,
		FromAddr: from, FromName: fromName, ToAddr: to, ToName: toName, CCAddr: cc,
		OrgID: account.OrganizationID, DealID: dealID, PersonEmail: contact.Email,
	}
}

func flip(direction string) string {
	if direction == directionOutbound {
		return directionInbound
	}
	return directionOutbound
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
//
// Walking the sum down by n keeps every value an int and every step in range,
// so the bucket is provably inside the slice without a conversion anyone has
// to reason about — and without the int/uint32 narrowing gosec rightly flags.
// The seeder's own hashIndex is written the same way for the same reason.
func hashIndex(key string, n int) int {
	if n <= 0 {
		return 0
	}
	h := fnv.New32a()
	_, _ = h.Write([]byte(key)) // hash.Write never returns an error, as its own contract states
	sum := h.Sum32()
	bucket := 0
	for i := 0; i < 32; i++ {
		bucket = (bucket*2 + int((sum>>(31-i))&1)) % n
	}
	return bucket
}
