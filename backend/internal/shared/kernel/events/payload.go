// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package events

// Payload is the seam that binds a generated webhook payload struct to its
// event type at compile time (gen-payloads, Task 1): EventType/EntityType
// are generated methods on each `crmcontracts.PublicEvent*` struct, so a
// caller that hands storekit.EmitEvent the wrong payload for an event is
// impossible to express — the compiler enforces the pairing instead of a
// reviewer catching a mismatched literal string at the call site.
type Payload interface {
	// EventType is the catalog event type this payload carries (events.md
	// §5), e.g. "deal.stage_changed".
	EventType() string
	// EntityType is the payload's static subject entity type, e.g. "deal".
	// The handful of dynamic-entity event types (x-entity-type: dynamic in
	// the contract) still implement this to satisfy the interface, but
	// their value is unused — the caller supplies the runtime entity type
	// via storekit.EmitEventForEntity instead.
	EntityType() string
}
