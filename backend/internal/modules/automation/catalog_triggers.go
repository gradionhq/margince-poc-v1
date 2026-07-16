// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package automation

// TriggerKind is the closed, user-facing trigger vocabulary (RC-11,
// features/10 §1): the seven ways a catalog or agent-authored automation
// may fire. A new member is a code-and-test change, never data — the same
// anti-builder guard the action side carries.
type TriggerKind string

// The seven closed trigger kinds (RC-11), in declaration order.
const (
	TriggerRecordCreatedUpdated  TriggerKind = "record_created_updated"
	TriggerFieldReachesValue     TriggerKind = "field_reaches_value"
	TriggerDealEntersLeavesStage TriggerKind = "deal_enters_leaves_stage"
	TriggerNoActivityForNDays    TriggerKind = "no_activity_for_n_days"
	TriggerDateFieldApproaching  TriggerKind = "date_field_approaching"
	TriggerInboundReply          TriggerKind = "inbound_reply"
	TriggerTaskOverdue           TriggerKind = "task_overdue"
)

// Trigger entry points (see TriggerDef.Entry): an event trigger reaches the
// matcher off cg:workflows, a clock trigger reaches the time-scan.
const (
	entryEvent = "event"
	entryClock = "clock"
)

// Event types the closed event-triggers pin to. These mirror
// shared/kernel/events' catalog keys; kept as constants here because more
// than one automation surface (the trigger registry, the starter handlers,
// the legacy catalog) references the same string.
const (
	eventDealStageChanged = "deal.stage_changed"
	eventEngagementReply  = "engagement.reply"
)

// TriggerDef declares how one trigger reaches the engine. Entry is "event"
// (the matcher, off cg:workflows) or "clock" (the time-scan). The three
// clock triggers consume no event by design (AUTO-EV-7).
//
// EventType is set to the ONE catalog type a trigger pins to
// (deal_enters_leaves_stage, inbound_reply); it is deliberately left empty
// for the two triggers that ride many entity streams rather than one
// fixed type (record_created_updated: any entity's created/updated verb;
// field_reaches_value: the same streams, narrowed by the automation's own
// field predicate) — Match() decides against the envelope, there is no
// single type name to pin. It is always empty for a clock trigger.
type TriggerDef struct {
	Kind      TriggerKind
	Entry     string // "event" | "clock"
	EventType string // set iff the trigger pins to exactly one catalog type
}

// triggerDefs is the registry body: one row per closed TriggerKind. A
// lookup miss is impossible for any TriggerKind returned by
// AllTriggerKinds — enforced by the closure test.
var triggerDefs = map[TriggerKind]TriggerDef{
	TriggerRecordCreatedUpdated: {Kind: TriggerRecordCreatedUpdated, Entry: entryEvent},
	TriggerFieldReachesValue:    {Kind: TriggerFieldReachesValue, Entry: entryEvent},
	// A specific verb, never the generic entity update (EVT-SEM-2):
	// "enters/leaves stage" is a stage move, not any deal field edit.
	TriggerDealEntersLeavesStage: {Kind: TriggerDealEntersLeavesStage, Entry: entryEvent, EventType: eventDealStageChanged},
	TriggerNoActivityForNDays:    {Kind: TriggerNoActivityForNDays, Entry: entryClock},
	TriggerDateFieldApproaching:  {Kind: TriggerDateFieldApproaching, Entry: entryClock},
	// Idempotent per reply (EVT-SEM-14): a re-delivered engagement.reply
	// must not re-fire the same automation instance twice.
	TriggerInboundReply: {Kind: TriggerInboundReply, Entry: entryEvent, EventType: eventEngagementReply},
	TriggerTaskOverdue:  {Kind: TriggerTaskOverdue, Entry: entryClock},
}

// AllTriggerKinds is the closed set, in declaration order. The closure
// test asserts this exactly matches the pinned RC-11 list, in both
// directions.
func AllTriggerKinds() []TriggerKind {
	return []TriggerKind{
		TriggerRecordCreatedUpdated, TriggerFieldReachesValue, TriggerDealEntersLeavesStage,
		TriggerNoActivityForNDays, TriggerDateFieldApproaching, TriggerInboundReply, TriggerTaskOverdue,
	}
}

// TriggerDefFor resolves one trigger's registry entry; ok=false for
// anything outside the closed set.
func TriggerDefFor(k TriggerKind) (TriggerDef, bool) {
	def, ok := triggerDefs[k]
	return def, ok
}
