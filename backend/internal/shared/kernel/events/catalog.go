// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package events

import (
	"fmt"
	"sort"
	"strings"
)

// StreamPrefix namespaces every CRM stream on the shared gw:events bus
// (03-architecture §3.4: the same bus Dispact rides).
const StreamPrefix = "gw:events:crm:"

// streamEntities are the nine V1 per-entity-type streams (events.md
// §4.1). Workspace is a field inside the envelope, never a stream —
// per-tenant streams would explode key count at multi-tenant scale.
var streamEntities = []string{
	"person", "organization", "deal", "lead", "activity",
	"approval", "capture", "coldstart", "audit",
}

// Streams returns the full stream key set, sorted, for the subscriber
// and the ops surface to enumerate.
func Streams() []string {
	out := make([]string, len(streamEntities))
	for i, e := range streamEntities {
		out[i] = StreamPrefix + e
	}
	sort.Strings(out)
	return out
}

// catalog is the enumerable V1 event catalog (events.md §5.1–§5.9): each
// type's home stream entity and current payload schema version. §5.10
// (overlay mirror) is overlay-mode-only and §5.11 (engagement/signals)
// rides E08/E15 — both are deferred with their work packages.
//
// Types whose entity segment is not itself a stream ride their family's
// stream (events.md §1 routing rule): consent.*/retention.* are
// person-lifecycle events, offer.*/pipeline.*/stage.* belong to the
// deal family — each declares its stream home here, and no catalog type
// may imply a stream §4.1 does not define.
var catalog = map[string]struct {
	stream  string
	version int
}{
	"person.created":    {"person", 1},
	"person.updated":    {"person", 1},
	"person.archived":   {"person", 1},
	"person.merged":     {"person", 1},
	"person.restored":   {"person", 1},
	"consent.changed":   {"person", 1},
	"retention.applied": {"person", 1},

	"organization.created":  {"organization", 1},
	"organization.updated":  {"organization", 1},
	"organization.archived": {"organization", 1},
	"organization.merged":   {"organization", 1},

	"deal.created":       {"deal", 1},
	"pipeline.created":   {"deal", 1},
	"pipeline.updated":   {"deal", 1},
	"pipeline.archived":  {"deal", 1},
	"stage.created":      {"deal", 1},
	"stage.updated":      {"deal", 1},
	"stage.archived":     {"deal", 1},
	"deal.updated":       {"deal", 1},
	"deal.stage_changed": {"deal", 1},
	"deal.owner_changed": {"deal", 1},
	"deal.archived":      {"deal", 1},
	"deal.restored":      {"deal", 1},
	"offer.created":      {"deal", 1},
	"offer.sent":         {"deal", 1},
	"offer.accepted":     {"deal", 1},
	"offer.rejected":     {"deal", 1},
	"offer.superseded":   {"deal", 1},

	"lead.created":      {"lead", 1},
	"lead.updated":      {"lead", 1},
	"lead.promoted":     {"lead", 1},
	"lead.disqualified": {"lead", 1},

	"activity.captured": {"activity", 1},
	"activity.updated":  {"activity", 1},
	"activity.archived": {"activity", 1},

	"approval.requested": {"approval", 1},
	"approval.decided":   {"approval", 1},

	"capture.received":   {"capture", 1},
	"capture.normalized": {"capture", 1},
	"capture.failed":     {"capture", 1},
	"capture.skipped":    {"capture", 1},

	"coldstart.read_back_proposed": {"coldstart", 1},
	"coldstart.accepted":           {"coldstart", 1},
	"coldstart.rejected":           {"coldstart", 1},

	"audit.appended": {"audit", 1},
}

// Types returns every catalog event type, sorted — the enumerable set
// codegen and the naming fitness test walk.
func Types() []string {
	out := make([]string, 0, len(catalog))
	for t := range catalog {
		out = append(out, t)
	}
	sort.Strings(out)
	return out
}

// StreamFor routes an event type to its stream key. An unknown type is a
// programming error the publisher must surface before the outbox write —
// an unroutable row would wedge the relay forever.
func StreamFor(eventType string) (string, error) {
	spec, ok := catalog[eventType]
	if !ok {
		return "", fmt.Errorf("events: %q is not in the events.md §5 catalog", eventType)
	}
	return StreamPrefix + spec.stream, nil
}

// VersionOf returns the current payload schema version of a catalog type
// (0 for an unknown type; Validate rejects those via StreamFor first).
// Publishers stamp envelopes from here — never a literal — so a future
// v2 bump happens in exactly one place.
func VersionOf(eventType string) int {
	return catalog[eventType].version
}

// Group is a §4.3 consumer group: one per consuming module, so each
// module sees every event once and scales horizontally inside the group.
type Group struct {
	Name    string
	Streams []string
}

// Groups returns the seven V1 consumer groups with their subscribed
// streams (events.md §4.3). cg:workflows and cg:read-model subscribe to
// everything by design; cg:audit-stream also does, because its "all
// actor.type=agent events" slice cuts across every stream and Redis
// consumer groups can only partition by stream, not by envelope field —
// the actor filter is in-process, like the workspace filter.
func Groups() []Group {
	all := Streams()
	forEntities := func(entities ...string) []string {
		keys := make([]string, len(entities))
		for i, e := range entities {
			keys[i] = StreamPrefix + e
		}
		sort.Strings(keys)
		return keys
	}
	return []Group{
		{Name: "cg:context-graph", Streams: forEntities("person", "organization", "deal", "activity", "lead")},
		{Name: "cg:overnight-agent", Streams: forEntities("activity", "deal", "lead", "approval")},
		{Name: "cg:workflows", Streams: all},
		{Name: "cg:capture", Streams: forEntities("capture")},
		{Name: "cg:flow-bridge", Streams: forEntities("person", "deal", "activity")},
		{Name: "cg:read-model", Streams: all},
		{Name: "cg:audit-stream", Streams: all},
	}
}

// SplitType breaks a catalog type into its <entity>.<verb> segments
// (events.md §1). Multi-word verbs keep their underscores
// ("stage_changed", "read_back_proposed").
func SplitType(eventType string) (entity, verb string, err error) {
	entity, verb, ok := strings.Cut(eventType, ".")
	if !ok || entity == "" || verb == "" {
		return "", "", fmt.Errorf("events: %q is not <entity>.<verb>", eventType)
	}
	return entity, verb, nil
}
