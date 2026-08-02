// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// Art. 17 reaches an in-flight job through the row the job NAMES, never
// through river_job. That is the property the whole job-args design rests on:
// args carry an id, so scrubbing the row the id points at neutralizes the
// queued work, and no copy of the subject's data survives in a table with no
// workspace column and no RLS.
//
// privacy_comms_integration_test.go already proves the scrub parks the
// delivery and that the dispatcher's Load refuses it. What it does not prove
// is the last step: that the job which was already QUEUED against that row
// wakes up and transmits nothing. Without this the kill-switch claim stops one
// call short of the thing an operator actually worries about.
//
// The same guarantee for telegram_ingest is proven next door, and more
// sharply, by telegram_sinkerasure_integration_test.go and
// channelidentity_erasurelock_integration_test.go: they pin the mutex between
// an erasure and the transaction that makes a channel record durable, which is
// the harder half of the same property.

import (
	"context"
	"testing"
	"time"

	"github.com/gradionhq/margince/backend/internal/modules/activities"
	"github.com/gradionhq/margince/backend/internal/modules/comms"
	"github.com/gradionhq/margince/backend/internal/modules/consent"
	"github.com/gradionhq/margince/backend/internal/modules/privacy"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/ports/connector"
)

// refusingMailbox is the mailbox seam AND the sender behind it, and it fails
// loudly if either is ever asked to transmit. The whole point of the kill
// switch is that the provider is never reached, so a counter that merely
// recorded the call would let a regression pass as a number nobody asserted on.
type refusingMailbox struct{ t *testing.T }

func (m refusingMailbox) SendEmail(context.Context, connector.Auth, connector.EmailMessage) (connector.SendReceipt, error) {
	m.t.Fatal("the queued send reached the provider after its subject was erased — the scrub parked the row, and the job must find nothing to send")
	return connector.SendReceipt{}, nil
}

func (m refusingMailbox) Resolve(context.Context, ids.UserID, string) (connector.EmailSender, connector.Auth, []string, error) {
	return m, connector.Auth{}, nil, nil
}

func (m refusingMailbox) ResolveChannel(context.Context, string) (connector.MessageSender, connector.Auth, error) {
	m.t.Fatal("the queued send resolved a channel mailbox after erasure")
	return nil, connector.Auth{}, nil
}

// TestErasingASubjectNeutralizesTheirQueuedSend drives the delivery the way
// the comms_send_email worker does — through comms.Dispatcher over the scope
// compose builds — AFTER the subject is erased, and proves the provider is
// never reached.
func TestErasingASubjectNeutralizesTheirQueuedSend(t *testing.T) {
	e := Setup(t)
	person := seedMailRecipient(t, e)
	// Aged past the statutory correspondence floor: a fresh fixture would be
	// shielded from the erase and would prove nothing about the scrub.
	queued := seedDelivery(t, e, "9 years", "Queued for the subject",
		"the words still waiting to go out", "pending", mailRecipientEmail, person)

	if err := privacy.NewEraser(e.Pool).ErasePerson(e.Admin(), person, "test"); err != nil {
		t.Fatalf("ErasePerson: %v", err)
	}

	// The job wakes here. Nothing about the queue changed — the args still
	// name the same delivery id — so what stops the send is the ROW, and only
	// the row.
	dispatcher := comms.NewDispatcher(
		comms.NewStore(e.Pool, time.Now, activities.NewStore(e.Pool)),
		refusingMailbox{t: t},
		nil,
		consent.NewGate(consent.NewStore(e.Pool)),
		nil, time.Now, 24*time.Hour, 10,
	)
	outcome, _, err := dispatcher.DispatchWithWait(e.Admin(), queued.delivery)
	if err != nil {
		t.Fatalf("the woken job failed with %v; a closed row is not a fault to retry, it is nothing left to do", err)
	}
	// Skipped, not sent and not retryable: the worker maps this to a completed
	// job row. The kill switch is that the provider was never reached at all —
	// refusingSender is what actually proves it, and it fails the test from
	// inside the call rather than through a counter asserted afterwards.
	if outcome != comms.OutcomeSkipped {
		t.Fatalf("the woken job reported outcome %q, want %q — the delivery the scrub closed has nothing to transmit",
			outcome, comms.OutcomeSkipped)
	}

	// The row stays closed and empty. A dispatch that reopened it, or wrote a
	// send timestamp onto it, would mean the job had acted on the subject after
	// their erasure even though nothing left the building.
	row := readDelivery(t, e, queued.delivery)
	if row.status != "parked" {
		t.Errorf("delivery status = %q after the woken job ran, want parked", row.status)
	}
	if row.sentAt != nil {
		t.Errorf("the woken job stamped a send time on a delivery that never left: %v", row.sentAt)
	}
	if row.body != "" {
		t.Errorf("the delivery body came back: %q", row.body)
	}
}
