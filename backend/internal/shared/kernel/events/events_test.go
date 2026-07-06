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
	"testing"
	"time"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

func TestStreamsMatchSpecList(t *testing.T) {
	want := []string{
		"gw:events:crm:activity",
		"gw:events:crm:approval",
		"gw:events:crm:audit",
		"gw:events:crm:capture",
		"gw:events:crm:coldstart",
		"gw:events:crm:deal",
		"gw:events:crm:lead",
		"gw:events:crm:organization",
		"gw:events:crm:person",
	}
	if got := Streams(); !reflect.DeepEqual(got, want) {
		t.Errorf("Streams() = %v, want the nine events.md §4.1 streams %v", got, want)
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
		"restored": true, "stage_changed": true, "owner_changed": true,
		"promoted": true, "captured": true, "requested": true,
		"decided": true, "failed": true, "appended": true,
		"changed": true, "applied": true, "sent": true, "accepted": true,
		"rejected": true, "superseded": true, "disqualified": true,
		"received": true, "normalized": true, "skipped": true,
		"read_back_proposed": true, "detected": true, "resolved": true,
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
	for typ, want := range map[string]string{
		"consent.changed":   "gw:events:crm:person",
		"retention.applied": "gw:events:crm:person",
		"offer.accepted":    "gw:events:crm:deal",
		"deal.updated":      "gw:events:crm:deal",
		"signal.detected":   "gw:events:crm:capture",
		"signal.resolved":   "gw:events:crm:capture",
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
	all := Streams()
	want := map[string][]string{
		"cg:context-graph":   {"gw:events:crm:activity", "gw:events:crm:deal", "gw:events:crm:lead", "gw:events:crm:organization", "gw:events:crm:person"},
		"cg:overnight-agent": {"gw:events:crm:activity", "gw:events:crm:approval", "gw:events:crm:deal", "gw:events:crm:lead"},
		"cg:workflows":       all,
		"cg:capture":         {"gw:events:crm:capture"},
		"cg:flow-bridge":     {"gw:events:crm:activity", "gw:events:crm:deal", "gw:events:crm:person"},
		"cg:read-model":      all,
		"cg:audit-stream":    all,
	}

	groups := Groups()
	if len(groups) != len(want) {
		t.Fatalf("Groups() returned %d groups, want the seven events.md §4.3 groups", len(groups))
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
		EventID:     ids.NewV7(),
		Type:        "deal.stage_changed",
		Version:     1,
		WorkspaceID: ids.NewV7(),
		OccurredAt:  time.Date(2026, 7, 3, 10, 15, 30, 123e6, time.UTC),
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
			EventID:     ids.NewV7(),
			Type:        "person.created",
			Version:     1,
			WorkspaceID: ids.NewV7(),
			OccurredAt:  time.Now().UTC(),
			Actor:       Actor{Type: "human", ID: "human:x"},
			Entity:      EntityRef{Type: "person", ID: ids.NewV7()},
			Trace:       Trace{CorrelationID: ids.NewV7(), AuditLogID: ids.NewV7()},
		}
	}

	cases := map[string]func(*Envelope){
		"zero event_id":       func(e *Envelope) { e.EventID = ids.Nil },
		"uncataloged type":    func(e *Envelope) { e.Type = "person.exploded" },
		"wrong version":       func(e *Envelope) { e.Version = 2 },
		"missing workspace":   func(e *Envelope) { e.WorkspaceID = ids.Nil },
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
