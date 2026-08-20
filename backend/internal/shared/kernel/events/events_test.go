// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package events

// Contract fitness tests (B-EP04.1/.2): the stream set matches events.md
// §4.1 exactly, every catalog type obeys the §1 naming law, the envelope
// survives a JSON round-trip bit-for-bit, and event_ids are time-ordered.

import (
	"encoding/json"
	"reflect"
	"regexp"
	"sort"
	"testing"
	"time"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// coreFamilyStreams is the events.md §4.1 stream set, spelled out rather than
// derived: these tests exist to catch a change to the stream layout, and an
// expectation computed from the code under test would move with it.
var coreFamilyStreams = []string{
	"gw:events:crm:activity",
	"gw:events:crm:approval",
	"gw:events:crm:audit",
	"gw:events:crm:capture",
	"gw:events:crm:coldstart",
	"gw:events:crm:deal",
	"gw:events:crm:identity",
	"gw:events:crm:lead",
	"gw:events:crm:organization",
	"gw:events:crm:overlay",
	"gw:events:crm:person",
	"gw:events:crm:voice",
}

func TestStreamsMatchSpecList(t *testing.T) {
	// The families, plus the extension tier's one stream — enumerated here
	// because the data reset unlinks exactly what this returns, and a stream
	// missing from it is one a reset silently leaves behind.
	want := append(append([]string{}, coreFamilyStreams...), "gw:events:crm:extension")
	sort.Strings(want)
	if got := Streams(); !reflect.DeepEqual(got, want) {
		t.Errorf("Streams() = %v, want the events.md stream set plus the extension stream %v", got, want)
	}
}

// segment is the §1 law: lowercase snake_case, no leading/trailing/double
// underscores.
var segment = regexp.MustCompile(`^[a-z]+(_[a-z]+)*$`)

func TestCatalogTypesObeyNamingConvention(t *testing.T) {
	// The closed verb law: events.md §1's enumeration plus the §5
	// catalog's own additions.
	pastTenseVerbs := map[string]bool{
		"created": true, "updated": true, "archived": true, "merged": true,
		"restored": true, "stage_changed": true, "phase_changed": true, "owner_changed": true,
		// A contract's asserted status moved — the same shape as a deal's stage
		// and a project's phase, one noun further along.
		"status_changed": true,
		// A lead's first-response deadline passed unanswered (formulas §18.2).
		"sla_breached": true,
		"promoted":     true, "captured": true, "requested": true,
		"decided": true, "failed": true, "appended": true,
		"changed": true, "applied": true, "sent": true, "accepted": true,
		"rejected": true, "superseded": true, "disqualified": true, "demoted": true,
		"received": true, "normalized": true, "skipped": true,
		"read_back_proposed": true, "detected": true, "resolved": true,
		"deactivated": true, "revoked": true, "restricted": true,
		"invited": true, "reactivated": true,
		"state_changed":   true,
		"profile_created": true, "profile_updated": true, "profile_archived": true,
		"corpus_changed": true, "build_changed": true, "version_changed": true,
		"draft_outcome_recorded": true,
		// engagement.reply is the §5.11 spec-pinned type name (EVT-SEM-14):
		// "reply" is the noun naming the fact, not a verb — the contract
		// wins over the tense convention (P3).
		"reply":    true,
		"conflict": true, "budget_degraded": true, "write_rejected": true, "deleted": true,
		"connected": true, "disconnected": true,
		// A member imported their own LinkedIn network. Past tense like the
		// rest; the list simply had not met it before.
		"imported": true,
		// An admin issued a member's set-password link. The verb carries its
		// object because "issued" alone would not say what was issued, and on
		// an identity stream that also mints passports the distinction is the
		// whole point of the event.
		"password_link_issued": true,
		// A won deal earned its partner a commission. Past tense like the rest;
		// `decided` above already covers the approve/pay/void half.
		"accrued": true,
	}

	for _, typ := range Types() {
		entity, verb, err := SplitType(typ)
		if err != nil {
			t.Errorf("catalog type %q: %v", typ, err)
			continue
		}
		if !segment.MatchString(entity) || !segment.MatchString(verb) {
			t.Errorf("catalog type %q: segments must be lowercase snake_case", typ)
		}
		if !pastTenseVerbs[verb] {
			t.Errorf("catalog type %q: verb %q is not a known past-tense catalog verb", typ, verb)
		}
		if stream, err := StreamFor(typ); err != nil || stream == StreamPrefix {
			t.Errorf("catalog type %q: no stream route (%v)", typ, err)
		}
	}
}

func TestStreamForRoutesFamiliesWithoutOwnStream(t *testing.T) {
	// consent/retention ride the person family, offer rides deal — the
	// documented routing for §5 types whose entity segment has no §4.1
	// stream.
	for typ, want := range map[string]string{ // #nosec G101 -- event-type→stream routing pins, not credentials
		"consent.changed":          "gw:events:crm:person",
		"retention.applied":        "gw:events:crm:person",
		"offer.accepted":           "gw:events:crm:deal",
		"deal.updated":             "gw:events:crm:deal",
		"signal.detected":          "gw:events:crm:capture",
		"signal.resolved":          "gw:events:crm:capture",
		"user.deactivated":         "gw:events:crm:identity",
		"role.changed":             "gw:events:crm:identity",
		"passport.revoked":         "gw:events:crm:identity",
		"onboarding.state_changed": "gw:events:crm:identity",
		"voice.profile_created":    "gw:events:crm:voice",
		"voice.version_changed":    "gw:events:crm:voice",
	} {
		if got, err := StreamFor(typ); err != nil || got != want {
			t.Errorf("StreamFor(%q) = %q, %v; want %q", typ, got, err, want)
		}
	}

	if _, err := StreamFor("invoice.created"); err == nil {
		t.Error("StreamFor accepted a type outside the catalog; an unroutable outbox row would wedge the relay")
	}
}

func TestGroupStreamSetsMatchSpecTable(t *testing.T) {
	// "Everything" a CORE group subscribes to is the FAMILY streams. The
	// extension stream is deliberately not among them; see
	// TestNoCoreGroupCarriesTheExtensionStream.
	all := coreFamilyStreams
	want := map[string][]string{
		"cg:context-graph": {"gw:events:crm:activity", "gw:events:crm:deal", "gw:events:crm:lead", "gw:events:crm:organization", "gw:events:crm:person"},
		// The interaction-edge projection (ADR-0078): activity events move an
		// edge, person events (merge, archive, restore) move every edge to
		// that contact.
		"cg:graph-edge": {"gw:events:crm:activity", "gw:events:crm:person"},
		// The audience-change corrector: an activity.updated carrying an
		// audience narrows the derived signals citing the message and drops
		// the thread's scan watermark.
		"cg:audience-rescope": {"gw:events:crm:activity"},
		// The LinkedIn ghost matcher (ADR-0078 §8b): a contact appearing is a
		// chance to attach a ghost, and so is an account appearing — employer
		// resolution is what most unmatched ghosts are waiting on.
		"cg:linkedin-match":     {"gw:events:crm:organization", "gw:events:crm:person"},
		"cg:person-auto-enrich": {"gw:events:crm:person"},
		// Automatic enrichment from a licensed provider (ADR-0101). Its own
		// group rather than a second handler on the pass above: that one
		// reads pages already crawled, this one SPENDS credits, and a
		// consumer whose retries buy data must not share a cursor with one
		// whose retries are free.
		"cg:person-data": {"gw:events:crm:person"},
		// Turning a won deal into what its partner earned. Its own group
		// because accrual is money: a projection rebuild must not be able to
		// stall it, and a failure to accrue must not read as a failure to index.
		"cg:commissions":     {"gw:events:crm:deal"},
		"cg:overnight-agent": {"gw:events:crm:activity", "gw:events:crm:approval", "gw:events:crm:deal", "gw:events:crm:lead"},
		"cg:workflows":       all,
		"cg:capture":         {"gw:events:crm:capture"},
		"cg:flow-bridge":     {"gw:events:crm:activity", "gw:events:crm:deal", "gw:events:crm:person"},
		"cg:read-model":      all,
		"cg:audit-stream":    all,
		"cg:webhooks":        all,
	}

	groups := Groups()
	if len(groups) != len(want) {
		t.Fatalf("Groups() returned %d groups, want %d — the events.md §4.3 groups, the E10 outbound-webhook fan-out, the ADR-0078 consumers (graph-edge projection, LinkedIn matcher), the ADR-0101 provider-enrichment consumer, the audience-rescope corrector, and the commission accrual", len(groups), len(want))
	}
	for _, g := range groups {
		if !reflect.DeepEqual(g.Streams, want[g.Name]) {
			t.Errorf("group %s subscribes %v, want %v", g.Name, g.Streams, want[g.Name])
		}
	}
}

func TestEnvelopeRoundTripPreservesEveryField(t *testing.T) {
	passport := ids.NewV7()
	env := Envelope{
		EventID:    ids.NewV7(),
		Type:       "deal.stage_changed",
		Version:    1,
		OccurredAt: time.Date(2026, 7, 3, 10, 15, 30, 123e6, time.UTC),
		Actor: Actor{
			Type:       "agent",
			ID:         "agent:overnight",
			PassportID: &passport,
			// OnBehalfOf nil: the null branch must survive too.
		},
		Entity:  EntityRef{Type: "deal", ID: ids.NewV7()},
		Payload: json.RawMessage(`{"from_stage_id":"a","to_stage_id":"b"}`),
		Trace: Trace{
			CorrelationID: ids.NewV7(),
			CausationID:   nil, // first event in its chain
			AuditLogID:    ids.NewV7(),
		},
	}
	if err := env.Validate(); err != nil {
		t.Fatalf("fixture envelope invalid: %v", err)
	}

	raw, err := json.Marshal(env)
	if err != nil {
		t.Fatal(err)
	}
	var back Envelope
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(env, back) {
		t.Errorf("round trip changed the envelope:\n got %+v\nwant %+v", back, env)
	}
	if back.Trace.CausationID != nil {
		t.Error("null causation_id came back non-nil")
	}
}

func TestEventIDsAreTimeOrdered(t *testing.T) {
	earlier := ids.NewV7()
	later := ids.NewV7()
	if earlier.String() >= later.String() {
		t.Errorf("UUIDv7 ordering violated: %s minted before %s but does not sort earlier", earlier, later)
	}
}

func TestValidateRejectsTheDishonestEnvelopes(t *testing.T) {
	valid := func() Envelope {
		return Envelope{
			EventID:    ids.NewV7(),
			Type:       "person.created",
			Version:    1,
			OccurredAt: time.Now().UTC(),
			Actor:      Actor{Type: "human", ID: "human:x"},
			Entity:     EntityRef{Type: "person", ID: ids.NewV7()},
			Trace:      Trace{CorrelationID: ids.NewV7(), AuditLogID: ids.NewV7()},
		}
	}

	cases := map[string]func(*Envelope){
		"zero event_id":       func(e *Envelope) { e.EventID = ids.Nil },
		"uncataloged type":    func(e *Envelope) { e.Type = "person.exploded" },
		"wrong version":       func(e *Envelope) { e.Version = 2 },
		"missing occurred_at": func(e *Envelope) { e.OccurredAt = time.Time{} },
		"missing actor":       func(e *Envelope) { e.Actor = Actor{} },
		"missing entity":      func(e *Envelope) { e.Entity = EntityRef{} },
		"missing trace":       func(e *Envelope) { e.Trace.AuditLogID = ids.Nil },
	}
	for name, corrupt := range cases {
		env := valid()
		corrupt(&env)
		if err := env.Validate(); err == nil {
			t.Errorf("Validate accepted an envelope with %s", name)
		}
	}
	if err := valid().Validate(); err != nil {
		t.Errorf("Validate rejected the valid fixture: %v", err)
	}
}
