// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package aiactivity

// The handler's decisions BEFORE the database: what it ignores, what it
// refuses, and the stale_after it derives. The projection itself is proved
// against a real Postgres in compose/integration — the guard is SQL, and a fake
// store would agree with whatever it was told.

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"testing"
	"time"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/events"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

func testConsumer() *Consumer {
	return NewConsumer(NewStore(nil), slog.New(slog.NewTextHandler(io.Discard, nil)))
}

// A group carrying somebody else's traffic must keep flowing. The store here
// has no database handle at all, so a handler that reached it would fail loudly
// rather than pass by accident.
func TestAnEventOfAnotherTypeIsIgnored(t *testing.T) {
	err := testConsumer().HandleEvent(context.Background(), events.Envelope{
		Type: "deal.created", EventID: ids.NewV7(),
	})
	if err != nil {
		t.Fatalf("an unrelated event must be ignored, got %v", err)
	}
}

// An undecodable payload is an error, not a silent skip: ACKing it would drop
// a state change nothing else will resend, and the row would keep displaying
// whatever it last held.
func TestAnUndecodablePayloadIsRefused(t *testing.T) {
	err := testConsumer().HandleEvent(context.Background(), events.Envelope{
		Type: eventType, EventID: ids.NewV7(), Payload: json.RawMessage(`{"attempt":"one"}`),
	})
	if err == nil {
		t.Fatal("a payload that does not decode must not be ACKed as handled")
	}
}

// The actor rule refuses before the write, so an unattributable occurrence
// never reaches the table as a workspace-scoped one.
func TestAnUnattributableActorStopsTheProjection(t *testing.T) {
	body, err := json.Marshal(crmcontracts.InternalEventAiTaskStateChanged{
		Source: "attachment_extraction", OccurrenceKey: "k", Kind: "document_extract",
		Attempt: 1, State: "queued",
	})
	if err != nil {
		t.Fatalf("marshaling the payload: %v", err)
	}
	err = testConsumer().HandleEvent(context.Background(), events.Envelope{
		Type: eventType, EventID: ids.NewV7(), Payload: body,
		Actor: events.Actor{Type: "human", ID: "human:nonsense"},
	})
	if err == nil {
		t.Fatal("an unparseable human actor must stop the projection, not become an ownerless row")
	}
}

func TestStaleAfterIsTheLeaseOnTopOfWhicheverInstantMadeTheAttemptCurrent(t *testing.T) {
	queued := time.Date(2026, 8, 21, 10, 0, 0, 0, time.UTC)
	started := queued.Add(90 * time.Second)
	lease := 300

	cases := []struct {
		name string
		in   crmcontracts.InternalEventAiTaskStateChanged
		want *time.Time
	}{{
		// A queued occurrence ages from when it was enqueued: there is no claim
		// yet, and a lease that only started counting at the claim would let a
		// queue nobody drains display as live forever.
		name: "queued ages from queued_at",
		in:   crmcontracts.InternalEventAiTaskStateChanged{State: "queued", QueuedAt: queued, LeaseSeconds: &lease},
		want: ptr(queued.Add(300 * time.Second)),
	}, {
		name: "running ages from started_at",
		in:   crmcontracts.InternalEventAiTaskStateChanged{State: "running", QueuedAt: queued, StartedAt: &started, LeaseSeconds: &lease},
		want: ptr(started.Add(300 * time.Second)),
	}, {
		// A settled occurrence is not claiming to be working, so it has nothing
		// to go stale. Storing one would render a finished row as stalled the
		// moment its lease elapsed.
		name: "settled has none",
		in:   crmcontracts.InternalEventAiTaskStateChanged{State: "done", QueuedAt: queued, StartedAt: &started, LeaseSeconds: &lease},
		want: nil,
	}, {
		name: "a source that declares no lease has none",
		in:   crmcontracts.InternalEventAiTaskStateChanged{State: "running", QueuedAt: queued, StartedAt: &started},
		want: nil,
	}}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := staleAfter(tc.in)
			switch {
			case tc.want == nil && got != nil:
				t.Fatalf("stale_after = %v, want none", *got)
			case tc.want != nil && got == nil:
				t.Fatalf("stale_after = none, want %v", *tc.want)
			case tc.want != nil && !got.Equal(*tc.want):
				t.Fatalf("stale_after = %v, want %v", *got, *tc.want)
			}
		})
	}
}

func ptr[T any](v T) *T { return &v }
