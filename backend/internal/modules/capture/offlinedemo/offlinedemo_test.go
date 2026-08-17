// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package offlinedemo

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/gradionhq/margince/backend/internal/shared/ports/connector"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
)

// TestTheConnectorCannotSend is the guarantee the whole design rests on. The
// addresses in this dataset are synthesized for identifiable real people, and
// the dataset's own rule is that nothing is ever delivered to one. A connector
// that grew a send seam would break that rule silently, so the absence is
// pinned rather than described.
func TestTheConnectorCannotSend(t *testing.T) {
	var c any = New(stubDirectory{})
	if _, ok := c.(connector.EmailSender); ok {
		t.Error("the offline demo connector implements EmailSender — it must never be able to deliver")
	}
	if _, ok := c.(connector.MessageSender); ok {
		t.Error("the offline demo connector implements MessageSender — it must never be able to deliver")
	}
}

type stubDirectory struct{ box Mailbox }

func (s stubDirectory) Mailbox(context.Context, string) (Mailbox, error) { return s.box, nil }

func demoMailbox() Mailbox {
	return Mailbox{
		UserID: "01a00000-0000-7000-8000-000000000001", DisplayName: "Lena Fischer",
		Email: "lena.fischer@demo.test", ColleagueName: "Markus Steiner",
		ColleagueEmail: "markus.steiner@demo.test",
		Accounts: []Account{{
			OrganizationID: "01a00000-0000-7000-8000-0000000000aa",
			Name:           "Acme GmbH", Domain: "acme.de", Lifecycle: "customer",
			ContractNumber: "V-1234-ACME",
			Now:            time.Date(2026, 8, 17, 9, 0, 0, 0, time.UTC),
			People:         []Person{{Name: "Petra Wolf", Email: "petra.wolf@acme.de", Role: "Head of IT"}},
			Deals:          []Deal{{ID: "01a00000-0000-7000-8000-0000000000bb", Name: "Acme Rollout", Stage: "Proposal"}},
		}},
	}
}

// TestGenerationIsDeterministic — a re-sync must produce the same
// conversation, or the natural key stops deduplicating and every pass files
// the thread again.
func TestGenerationIsDeterministic(t *testing.T) {
	box := demoMailbox()
	first := generate(box, box.Accounts[0])
	second := generate(box, box.Accounts[0])
	if len(first) != len(second) || len(first) == 0 {
		t.Fatalf("generated %d then %d messages", len(first), len(second))
	}
	for i := range first {
		if first[i].MessageID != second[i].MessageID || !first[i].OccurredAt.Equal(second[i].OccurredAt) {
			t.Errorf("message %d differs between runs: %s@%s vs %s@%s",
				i, first[i].MessageID, first[i].OccurredAt, second[i].MessageID, second[i].OccurredAt)
		}
	}
}

// TestThreadShape — the opener roots the thread and every reply joins it. The
// sink's reply detection keys on a thread whose earlier message was outbound,
// so an inbound reply that rooted itself would join nothing.
func TestThreadShape(t *testing.T) {
	box := demoMailbox()
	msgs := generate(box, box.Accounts[0])
	byThread := map[string][]message{}
	for _, m := range msgs {
		byThread[m.ThreadKey] = append(byThread[m.ThreadKey], m)
	}
	if len(byThread) == 0 {
		t.Fatal("a customer account generated no threads")
	}
	for key, thread := range byThread {
		if thread[0].MessageID != key {
			t.Errorf("thread %s does not root on its own opener (%s)", key, thread[0].MessageID)
		}
		for i := 1; i < len(thread); i++ {
			if !thread[i].OccurredAt.After(thread[i-1].OccurredAt) {
				t.Errorf("thread %s message %d does not follow the one before it", key, i)
			}
		}
	}
}

// TestEveryMessageIdIsValid — a Message-ID the product refuses costs the whole
// message, and offline-demo.invalid is reserved (RFC 2606) so no such mailbox
// can ever exist.
func TestEveryMessageIdIsValid(t *testing.T) {
	box := demoMailbox()
	for _, m := range generate(box, box.Accounts[0]) {
		if !strings.HasPrefix(m.MessageID, "<") || !strings.HasSuffix(m.MessageID, ">") {
			t.Errorf("message id %q is not angle-bracketed", m.MessageID)
		}
		if !strings.Contains(m.MessageID, "@offline-demo.invalid") {
			t.Errorf("message id %q does not sit on the reserved demo domain", m.MessageID)
		}
	}
}

// TestEveryAddressIsKnown — the generator must never invent a correspondent.
// An address outside the mailbox and its accounts would be a real person
// nobody in this dataset agreed to.
func TestEveryAddressIsKnown(t *testing.T) {
	box := demoMailbox()
	known := map[string]bool{box.Email: true, box.ColleagueEmail: true}
	for _, a := range box.Accounts {
		for _, p := range a.People {
			known[p.Email] = true
		}
	}
	for _, m := range generate(box, box.Accounts[0]) {
		for _, addr := range []string{m.FromAddr, m.ToAddr, m.CCAddr} {
			if addr != "" && !known[addr] {
				t.Errorf("message %s names %q, which is nobody in this mailbox", m.MessageID, addr)
			}
		}
	}
}

// TestEveryThreadHasAnExternalParty — the sink DROPS a record whose every
// party is on an own domain. A thread between colleagues would vanish
// silently, which looks like a generator that produced nothing.
func TestEveryThreadHasAnExternalParty(t *testing.T) {
	box := demoMailbox()
	for _, m := range generate(box, box.Accounts[0]) {
		rec := m.record()
		external := false
		for _, addr := range rec.Addresses {
			if !strings.HasSuffix(addr, "@demo.test") {
				external = true
			}
		}
		if !external {
			t.Errorf("message %s names only own-domain parties — the sink drops it as internal-only", m.MessageID)
		}
	}
}

// TestOutboundNamesItsRecipient — and deliberately does NOT carry the owner
// attestation. WithOwnerAttestation is the T1 correspondence gate's only
// evidence and may be minted solely by the mail mapper, which knows the
// provider's own filing of the message; a generator asserting it from its own
// content is the hole that rule closes. A fitness test in package backendarch
// enforces it.
func TestOutboundNamesItsRecipient(t *testing.T) {
	box := demoMailbox()
	sawOutbound := false
	for _, m := range generate(box, box.Accounts[0]) {
		if m.Kind == "meeting" {
			if m.record().Counterparty.Email != "" {
				t.Errorf("meeting %s carries a counterparty; a calendar record has attendees instead", m.MessageID)
			}
			continue
		}
		rec := m.record()
		if m.Direction == directionOutbound {
			sawOutbound = true
			if rec.Counterparty.Email != strings.ToLower(m.ToAddr) {
				t.Errorf("outbound %s names %q as counterparty, want the recipient", m.MessageID, rec.Counterparty.Email)
			}
		}
		if rec.Counterparty.Email == "" {
			t.Errorf("mail %s has no counterparty", m.MessageID)
		}
	}
	if !sawOutbound {
		t.Error("no outbound message was generated, so the attestation path is untested")
	}
}

// TestRecordsLinkTheAccount — an activity that links nothing shows on no
// company page, which is the failure the seeder's verify pass exists to catch.
func TestRecordsLinkTheAccount(t *testing.T) {
	box := demoMailbox()
	for _, m := range generate(box, box.Accounts[0]) {
		rec := m.record()
		found := false
		for _, link := range rec.Links {
			if link.Type == datasource.EntityOrganization {
				found = true
			}
		}
		if !found {
			t.Errorf("message %s links to no organization", m.MessageID)
		}
	}
}

// TestAnAccountWithNoPeopleWritesNothing — most Automation World companies
// publish no staff, and a thread addressed to a company rather than a person
// is not correspondence.
func TestAnAccountWithNoPeopleWritesNothing(t *testing.T) {
	box := demoMailbox()
	account := box.Accounts[0]
	account.People = nil
	if msgs := generate(box, account); len(msgs) != 0 {
		t.Errorf("an account with no contacts generated %d messages", len(msgs))
	}
}

// TestTheSecondSyncEmitsNothing — the cursor is what keeps a two-minute sweep
// from re-walking every mailbox. Without it the steady state is a full replay
// against the natural key on every pass.
func TestTheSecondSyncEmitsNothing(t *testing.T) {
	box := demoMailbox()
	c := New(stubDirectory{box: box})
	sink := &countingSink{}
	cursor, err := c.Sync(context.Background(), connector.Auth(box.UserID), nil, sink)
	if err != nil {
		t.Fatalf("first sync: %v", err)
	}
	if sink.n == 0 {
		t.Fatal("the first sync emitted nothing")
	}
	first := sink.n
	if _, err := c.Sync(context.Background(), connector.Auth(box.UserID), cursor, sink); err != nil {
		t.Fatalf("second sync: %v", err)
	}
	if sink.n != first {
		t.Errorf("the second sync emitted %d more records; the cursor is not holding", sink.n-first)
	}
}

// TestSyncNeedsASeat — without one there is no mailbox to generate, and
// guessing would put another seat's correspondence in somebody's inbox.
func TestSyncNeedsASeat(t *testing.T) {
	c := New(stubDirectory{box: demoMailbox()})
	if _, err := c.Sync(context.Background(), nil, nil, &countingSink{}); err == nil {
		t.Error("a sync with no seat succeeded")
	}
}

type countingSink struct{ n int }

func (s *countingSink) Upsert(context.Context, connector.NormalizedRecord) (datasource.EntityRef, error) {
	s.n++
	return datasource.EntityRef{}, nil
}

// TestNoMessageIsDatedInTheFuture is the bug that cost the most to find.
//
// The first version anchored an account's correspondence on the organization's
// created_at. In a fresh installation that is TODAY for every company, so
// "twenty days after the account existed" landed twenty days from now — and a
// captured message that has not happened yet is refused. The generator
// produced six mails per customer, the database stayed empty, and nothing was
// logged, because the refusal was the product doing its job.
func TestNoMessageIsDatedInTheFuture(t *testing.T) {
	box := demoMailbox()
	now := box.Accounts[0].Now
	for _, m := range generate(box, box.Accounts[0]) {
		if m.OccurredAt.After(now) {
			t.Errorf("message %s is dated %s, which is after the run at %s — the sink refuses it",
				m.MessageID, m.OccurredAt.Format(time.RFC3339), now.Format(time.RFC3339))
		}
	}
}

// TestHistoryReachesBack — correspondence bunched into one afternoon reads as
// generated. A worked account has a few months behind it.
func TestHistoryReachesBack(t *testing.T) {
	box := demoMailbox()
	msgs := generate(box, box.Accounts[0])
	if len(msgs) < 2 {
		t.Fatalf("only %d messages to spread", len(msgs))
	}
	oldest, newest := msgs[0].OccurredAt, msgs[0].OccurredAt
	for _, m := range msgs {
		if m.OccurredAt.Before(oldest) {
			oldest = m.OccurredAt
		}
		if m.OccurredAt.After(newest) {
			newest = m.OccurredAt
		}
	}
	if newest.Sub(oldest) < 14*24*time.Hour {
		t.Errorf("the whole history spans %s — an account worked for a quarter should reach further back",
			newest.Sub(oldest))
	}
}
