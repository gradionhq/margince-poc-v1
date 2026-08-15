// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package approvals

// What a caller hands the engine to hold one action for decision.
//
// Split from staging.go because it is the SHAPE of a request and that file is
// the behaviour that acts on it: the fields below are read by four entry points
// (Stage, StageInTx, StageOrJoinPendingInTx and the bundle path), each of which
// honours a different subset, and a reader working out which needs the whole
// vocabulary in one place rather than interleaved with the transactions.

import (
	"encoding/json"
	"time"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/events"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// StageInput describes one refused 🟡 call to hold for decision.
type StageInput struct {
	Kind           string // the tool name, e.g. advance_deal
	ProposedChange json.RawMessage
	DiffHash       string
	// TargetType + TargetID are the polymorphic reference to the staged
	// action's target (any entity kind); the id stays untyped because the
	// pair is the discriminated reference, not one entity's typed id.
	TargetType    string
	TargetID      ids.UUID
	TargetVersion *int64
	Summary       string
	// JoinPending collapses an identical live proposal under an atomic
	// transaction lock. It is for at-least-once worker paths whose retries
	// must return the existing approval instead of multiplying inbox rows.
	JoinPending bool
	// TTL is THIS staging's approvable window, or nil to take the kind's
	// default — APPR-PARAM-1's per-item half beside the 72h default.
	//
	// It exists because two stagings of one kind can deserve different windows
	// when the thing they are about does, and the kind cannot tell which is
	// which. A kind exempt from expiry ignores it: that exemption is a property
	// of the subject rather than a knob a caller may turn.
	TTL *time.Duration
	// Identity is the proposal's logical identity — a JSON object contained
	// in ProposedChange (e.g. {"from_currency":"GBP"}). Requires JoinPending:
	// staging then serializes per identity instead of per diff hash, and any
	// OTHER live pending proposal of the same kind+target carrying this
	// identity is withdrawn (forced expiry, audited) — a fresher diff for one
	// identity supersedes a stale one instead of competing with it in the
	// inbox, where approving stale-after-fresh would restore an outdated value.
	Identity json.RawMessage
	// Announce is an optional kind-specific domain event (e.g.
	// coldstart.read_back_proposed) emitted in the SAME transaction as
	// approval.requested, linked to the same audit row.
	Announce []AnnouncedEvent
	// Evidence is the material each claim in ProposedChange was read out of,
	// so the human confirming it can check the proposal instead of trusting
	// it (evidence.go). Empty for a staging derived from record state rather
	// than from reading something.
	Evidence []Evidence
	// BundleID names the act that proposed this row together with its siblings —
	// today, a website read's company facts and the leads it published. Zero for
	// a proposal staged alone.
	//
	// It is a grouping, never a second authority object: every member keeps its
	// own diff hash, version pin, expiry and verdict, and a bundle decision is N
	// per-row decisions (ADR-0036 — the staged row IS the authority object).
	BundleID ids.UUID
}

// AnnouncedEvent is one extra catalog event a staging carries. Payload
// names its own event type (events.Payload.EventType()), the same seam
// storekit.EmitEvent uses — a caller cannot pair the wrong payload with
// an announced event without failing to compile.
type AnnouncedEvent struct {
	Payload events.Payload
}
