// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package extension

import (
	"context"
	"errors"

	"github.com/gradionhq/margince/backend/pkg/extension/crm"
)

// Core is the governed door onto the installation's own records, reached from
// a transaction the unit already holds: `tx.Core().Activities().Create(…)`.
//
// It exists because the alternative is worse than a missing feature. A unit
// that wants a core record has SQL and the shared application role, so it can
// reach the table today — and a write made that way lands with no audit row, no
// outbox event, no RBAC check and no attribution, which is every guarantee the
// product's own write shape exists to keep. This is the same write the product
// makes, minus nothing.
//
// FOUR PROPERTIES, all of them the core's and none of them the unit's:
//
//   - ONE TRANSACTION. The unit's own row and the core record commit together
//     or not at all, because the core write runs on the transaction the unit is
//     already inside. That is the whole reason this hangs off Tx rather than
//     off Runtime: a unit whose bookkeeping row and whose core record commit
//     separately has a dual write, which is exactly what the transactional
//     outbox exists to prevent.
//   - THE CALLER'S AUTHORITY, never the unit's. Every write is checked against
//     the live RBAC of the human the invocation arrived under, so a unit can
//     never write what its caller could not have written itself. A unit
//     declares no authority of its own, and there is no principal here to
//     escalate to.
//   - ATTRIBUTION. The audit row names the human as the actor and records the
//     unit beside them, so "who did this" and "what carried it" stay separate
//     answers. A unit cannot write, forge or suppress that record.
//   - THE PRODUCT'S OWN SHAPES. What a verb takes and returns is generated from
//     backend/api/crm.yaml (see the crm package), so a unit writes the record
//     the HTTP surface documents rather than a second dialect of it.
//
// WHAT IT REFUSES, and why each refusal is a refusal rather than a silence:
//
//   - A JOB TICK. A scheduled tick runs as the unit, with no human behind it,
//     so there is no authority to check a write against. Rather than invent one
//     — a system principal passes every check ever written — a tick's Core call
//     answers ErrForbidden. A tick may still write the unit's OWN tables, which
//     is what a tick is for.
//   - OVERLAY MODE. Where an installation mirrors an incumbent system of
//     record, the native tables this writes are not the live ones, so a write
//     here would land somewhere nobody reads. It answers
//     ErrOverlayUnsupported rather than writing into the dark.
//   - CUSTOM FIELDS. A record's custom-field values need the field catalog, and
//     reading the catalog takes a second database connection — inside a
//     transaction the unit already holds, that is a deadlock shape rather than
//     a slow path. A write carrying them is refused with ErrInvalid, never
//     accepted-and-dropped.
type Core interface {
	// Activities returns the timeline door: a unit files what happened.
	Activities() ActivityRepo
}

// ActivityRepo writes the shared timeline — the record every other one hangs
// its history off.
type ActivityRepo interface {
	// Create logs one activity against the subjects its links name, and returns
	// it as the product's own read shape.
	//
	// The links are checked the way the product checks them: a subject the
	// caller cannot see reads as ErrNotFound, not as a permission error, so a
	// unit cannot use this to learn which records exist.
	Create(ctx context.Context, in crm.CreateActivityRequest) (crm.Activity, error)
}

// The refusals a Core verb can answer. They are sentinels rather than typed
// errors because a unit's only reasonable response to each is a decision, not
// an inspection — and because the core's own error text must not reach a unit,
// which would make internal wording (a table name, a constraint, a SQL state) a
// surface some unit ends up parsing.
var (
	// ErrNotFound is a record that is not there, or is not the caller's to see.
	// One error for both, deliberately: distinguishing them tells a unit which
	// records exist.
	ErrNotFound = errors.New("extension: no such record")

	// ErrForbidden is the caller's own RBAC refusing the write — or a job tick,
	// which has no caller to check.
	ErrForbidden = errors.New("extension: the caller may not do that")

	// ErrConflict is a record that changed under the write.
	ErrConflict = errors.New("extension: the record changed under this write")

	// ErrInvalid is a request the contract does not admit.
	ErrInvalid = errors.New("extension: the request is malformed")

	// ErrOverlayUnsupported is a core write in an overlay workspace, where the
	// native record this would write is not the live one.
	ErrOverlayUnsupported = errors.New("extension: core records are not writable in overlay mode")
)
