// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// The subscription seam — how a unit hears that something happened — is part
// of the published extension surface.
//
//margince:extension-surface

package extension

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// EntityRef names the subject of a delivered event: a REF, never the record.
// A consumer that needs the record reads it back under its own capabilities,
// which is what keeps a fact on the bus from ageing into a stale copy of the
// row it describes.
type EntityRef struct {
	// Type is the kind of record — a core entity type (`activity`, `person`)
	// for a core event, a unit's own table for an extension one.
	Type string
	// ID is the record's id, a canonical UUID.
	ID string
}

// Delivery is one event as a unit receives it.
//
// It is named apart from Event, which is what a unit PUBLISHES, because the two
// directions are not the same shape and one name for both is how a handler ends
// up republishing what it was handed.
type Delivery struct {
	// EventID is the bus event's own id. It is the idempotency key: the core
	// suppresses a redelivery it has already seen for this subscription, and a
	// handler that keeps its own record of what it has processed keys on this.
	EventID string

	// Type is the full event type — `activity.archived` for a core fact,
	// `ext_<namespace>.<verb>` for another unit's.
	Type string

	// OccurredAt is when the fact happened, not when it was delivered. The two
	// differ by the relay, by a retry, and by however long a consumer was down.
	OccurredAt time.Time

	// Entity is the subject. It is empty for the handful of core pipeline
	// events that name nothing at all (a message excluded from capture creates
	// no row), so a handler that keys on the subject checks it.
	Entity EntityRef

	// Payload is the event's own body as its publisher wrote it, re-rendered
	// by the jsonb column it travelled through: the document is the
	// publisher's, the whitespace and key order are the database's. The core
	// makes no promise about the SHAPE — for a core event it is the contract's,
	// for another unit's it is that unit's — so a handler decodes the fields it
	// needs and tolerates the ones it does not know.
	Payload json.RawMessage
}

// EventHandler runs ONE delivery of ONE subscribed event.
//
// It is the BEHAVIOR half of a subscription, exactly as ToolHandler is a tool's
// and JobHandler is a job's, and like them it is not derivable from the AST —
// the manifest records WHAT a unit subscribes to, and this is the part a static
// document cannot hold.
//
// rt arrives HERE, at delivery, because a declaration is inert data: a
// Subscription value sitting in a slice holds no route into the core. The core
// mints rt for this one delivery and releases it when the handler returns.
//
// THERE IS NOBODY BEHIND A DELIVERY. rt.Caller() answers the zero Caller and
// rt.Tx(…).Core() refuses, for the same reason a job tick's does: a core write
// is checked against the caller's own permissions and a bus event has no
// caller. A handler may read and write the unit's OWN tables, audit those
// writes, and publish — which is what reacting to a fact consists of.
//
// THE BUS IS AT-LEAST-ONCE. The core suppresses the redelivery it can see (the
// same event to the same subscription), but a handler must still be safe to run
// twice: the suppression is a cache, and a crash between the effect and the ack
// is exactly the case it cannot cover. Returning an error leaves the entry
// pending and re-delivers it later; returning nil acks it. So a handler that
// cannot succeed — a malformed payload, a subject it does not recognize —
// returns nil and logs, rather than failing forever on a delivery no retry can
// fix.
type EventHandler func(ctx context.Context, rt Runtime, d Delivery) error

// Subscription is the BEHAVIOR half of one thing a unit listens for: which
// events, and the Go function that runs when one arrives.
//
// The event list is DECLARED rather than filtered in code, and that is a
// governance property rather than a convenience: it is derived into the unit's
// manifest.generated.json, so an operator reading the manifest can see which of
// the installation's facts a unit consumes without reading its source.
type Subscription struct {
	// Name identifies the subscription within its unit, lower snake_case. It
	// keys the consumer group the core builds for it, so it is stable
	// FOREVER for a given unit: renaming one starts a fresh group, which
	// re-reads the stream's retained history from the beginning.
	Name string

	// Events are the event types this subscription wants, spelled in full —
	// `activity.archived`, `ext_notes.note_added`. A type the installation
	// cannot route is refused at boot rather than silently never delivered.
	//
	// A unit may name any routable type, its own or the product's. What a unit
	// learns from one is an id and a verb; reading the record behind it is
	// bounded by the capabilities the unit already holds.
	Events []string

	// Handle is the behavior. A nil Handle is the same as no entry at all, so
	// a unit with nothing to run declares no Subscription rather than one
	// holding nil — and the core says so at boot rather than registering a
	// group that acks every delivery into nothing.
	Handle EventHandler
}

// Validate enforces everything about a subscription that must hold wherever it
// is read, and nothing about the EVENTS' routability: whether a type exists is
// a question only the core can answer (the catalog is not reachable from this
// package), so it is asked at registration, exactly as a job's tier is.
func (s Subscription) Validate() error {
	if err := s.ValidateDeclaration(); err != nil {
		return err
	}
	if s.Handle == nil {
		return fmt.Errorf("subscription %q has no handler — declare nothing rather than a subscription that acks every delivery into nothing", s.Name)
	}
	return nil
}

// ValidateDeclaration is Validate minus the handler check: the part a STATIC
// reader can decide.
//
// It exists because the manifest generator derives a unit's subscriptions from
// the source without compiling it, so the one thing it can never see is whether
// Handle is nil — and the alternative to this split is a generator that
// re-implements the name and event rules, which is how gen-time acceptance
// drifts from boot-time.
func (s Subscription) ValidateDeclaration() error {
	if !toolNameGrammar.MatchString(s.Name) {
		return fmt.Errorf("subscription name %q is not a valid subscription name (lower snake_case, e.g. withdraw_filing)", s.Name)
	}
	if len(s.Events) == 0 {
		return fmt.Errorf("subscription %q names no events — a subscription to nothing is a consumer group that never delivers", s.Name)
	}
	seen := make(map[string]bool, len(s.Events))
	for _, e := range s.Events {
		if e == "" {
			return fmt.Errorf("subscription %q names an empty event type", s.Name)
		}
		if seen[e] {
			return fmt.Errorf("subscription %q names %q twice", s.Name, e)
		}
		seen[e] = true
	}
	return nil
}
