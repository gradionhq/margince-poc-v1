// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// What a unit's listener consumes, what one delivery carries, and what the boot
// refuses to register. The write a delivery makes is the database lane's
// question; these are the ones that must hold with no database at all.

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	kevents "github.com/gradionhq/margince/backend/internal/shared/kernel/events"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/pkg/extension"
)

func composedSubscription(events ...string) ComposedSubscription {
	return ComposedSubscription{
		Unit: "notes", Version: "1.0.0",
		Sub: extension.Subscription{
			Name: "withdraw_filing", Events: events,
			Handle: func(context.Context, extension.Runtime, extension.Delivery) error { return nil },
		},
	}
}

// A listener consumes the streams its DECLARED types route to, and no others.
// Deriving them from the types is what keeps a unit off a stream it never asked
// about: an event it does not want is not filtered in process, it is not read.
func TestAListenersGroupCoversTheStreamsItsTypesRouteTo(t *testing.T) {
	group, err := composedSubscription(
		"activity.archived", "activity.captured", "person.archived", "ext_notes.note_added").Group()
	if err != nil {
		t.Fatal(err)
	}
	if group.Name != "cg:ext-notes-withdraw_filing" {
		t.Errorf("group name = %q, want cg:ext-notes-withdraw_filing", group.Name)
	}
	want := []string{"gw:events:crm:activity", "gw:events:crm:extension", "gw:events:crm:person"}
	if !reflect.DeepEqual(group.Streams, want) {
		t.Errorf("streams = %v, want %v — deduplicated and sorted", group.Streams, want)
	}
}

// One group per SUBSCRIPTION, not per unit: consumers on one group share a
// pending entry, so a handler that kept failing would hold up a sibling with
// nothing to do with it.
func TestTwoListenersOfOneUnitGetDistinctGroups(t *testing.T) {
	first := composedSubscription("activity.archived")
	second := composedSubscription("person.archived")
	second.Sub.Name = "another_listener"
	if first.GroupName() == second.GroupName() {
		t.Fatalf("both listeners consume %q", first.GroupName())
	}
}

// An unroutable type has no stream, so its listener would be created, hold a
// cursor and never receive anything — indistinguishable from a product where
// that fact never happens. The boot says so instead.
func TestTheBootRefusesASubscriptionNothingCouldDeliver(t *testing.T) {
	for name, sub := range map[string]extension.Subscription{
		"a type outside the catalog": {
			Name: "listens", Events: []string{"invoice.created"},
			Handle: func(context.Context, extension.Runtime, extension.Delivery) error { return nil },
		},
		"a malformed extension type": {
			Name: "listens", Events: []string{"ext_notes.NoteAdded"},
			Handle: func(context.Context, extension.Runtime, extension.Delivery) error { return nil },
		},
		"no handler": {Name: "listens", Events: []string{"activity.archived"}},
		"no events": {
			Name:   "listens",
			Handle: func(context.Context, extension.Runtime, extension.Delivery) error { return nil },
		},
	} {
		err := preflightSubscriptions(extension.Extension{
			Name: "notes", Version: "1.0.0", Subscriptions: []extension.Subscription{sub},
		})
		if err == nil {
			t.Errorf("the boot registered a subscription with %s", name)
		}
	}

	// Two listeners of one name would key one consumer group twice.
	handler := func(context.Context, extension.Runtime, extension.Delivery) error { return nil }
	duplicate := extension.Subscription{Name: "listens", Events: []string{"activity.archived"}, Handle: handler}
	if err := preflightSubscriptions(extension.Extension{
		Name: "notes", Version: "1.0.0", Subscriptions: []extension.Subscription{duplicate, duplicate},
	}); err == nil || !strings.Contains(err.Error(), "twice") {
		t.Errorf("err = %v, want the duplicate-subscription refusal", err)
	}

	// And the well-formed one registers, so the refusals above are not passing
	// over a preflight that refuses everything.
	if err := preflightSubscriptions(extension.Extension{
		Name: "notes", Version: "1.0.0",
		Subscriptions: []extension.Subscription{
			{Name: "listens", Events: []string{"activity.archived", "ext_notes.note_added"}, Handle: handler},
		},
	}); err != nil {
		t.Errorf("a well-formed subscription was refused: %v", err)
	}
}

// A group's streams carry more than one listener asked for — one activity event
// means the whole activity stream — so a delivery it did not declare is ACKED
// rather than failed. It can never succeed on a retry, and an entry that fails
// forever stalls every later event on the group.
func TestADeliveryOfAnUndeclaredTypeIsAckedWithoutRunningTheHandler(t *testing.T) {
	var ran bool
	sub := composedSubscription("activity.archived")
	sub.Sub.Handle = func(context.Context, extension.Runtime, extension.Delivery) error {
		ran = true
		return nil
	}
	// A nil pool is safe here BECAUSE the filter runs first; if it ever stopped
	// running first, this test would fail on the tenant read rather than pass.
	err := sub.Handler(nil)(context.Background(), kevents.Envelope{
		EventID: ids.NewV7(), Type: "activity.captured",
		Entity: kevents.EntityRef{Type: "activity", ID: ids.NewV7()},
	})
	if err != nil {
		t.Fatalf("an undeclared type was not acked: %v", err)
	}
	if ran {
		t.Error("the handler ran for a type its subscription never declared")
	}
}

// What a unit is handed is a REF and a payload, never the record. The ids are
// strings because the published surface exports no id type.
func TestADeliveryCarriesTheEnvelopesRefAndNothingElse(t *testing.T) {
	eventID, subject := ids.NewV7(), ids.NewV7()
	occurred := time.Date(2026, 8, 13, 9, 30, 0, 0, time.UTC)
	got := deliveryOf(kevents.Envelope{
		EventID: eventID, Type: "activity.archived", OccurredAt: occurred,
		Entity:  kevents.EntityRef{Type: "activity", ID: subject},
		Payload: json.RawMessage(`{"reason":"noise"}`),
	})
	want := extension.Delivery{
		EventID: eventID.String(), Type: "activity.archived", OccurredAt: occurred,
		Entity:  extension.EntityRef{Type: "activity", ID: subject.String()},
		Payload: json.RawMessage(`{"reason":"noise"}`),
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("delivery = %+v, want %+v", got, want)
	}
}

// The core's entity-less pipeline events name nothing at all, and a Delivery
// must say so rather than hand a unit an all-zero UUID it would happily use as
// a subject.
func TestAnEntitylessEventDeliversAnEmptyRef(t *testing.T) {
	got := deliveryOf(kevents.Envelope{
		EventID: ids.NewV7(), Type: "capture.skipped", OccurredAt: time.Now().UTC(),
	})
	if got.Entity != (extension.EntityRef{}) {
		t.Errorf("entity = %+v, want the empty ref", got.Entity)
	}
}

// NOBODY is behind a delivery. rt.Caller() answers the zero Caller — the least
// authority — however the delivery's own principal is bound, and the runtime is
// unattended, which is what shuts the core port. The two travel together
// deliberately: a delivery runs as PrincipalSystem, which auth.Require does not
// check at all, so the port's refusal is the whole distance between a bus event
// and an unchecked core write.
func TestADeliveryRunsAsNobody(t *testing.T) {
	rt := deliveryRuntimeFor(context.Background(), "notes", "1.0.0",
		"subscription/withdraw_filing", extensionRuntimeBinding{})
	if rt.Caller() != (extension.Caller{}) {
		t.Errorf("Caller() = %+v, want the zero Caller", rt.Caller())
	}
	if !rt.unattended {
		t.Fatal("a delivery's runtime is not unattended, so the core port would be open to it")
	}
	// The refusal that flag reaches, asserted here rather than assumed: the
	// join through Tx needs a pool and is the database lane's.
	if err := (extensionCore{unattended: rt.unattended}).refuseUnattended(); err == nil {
		t.Error("an unattended invocation was allowed a core write")
	}
}
