// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The core's half of extension.Tx.Record: the ledger row and the bus event a
// unit's OWN write records, written through the product's own storekit seam on
// the transaction the unit already holds.
//
// Everything here is a translation and a set of refusals. The WRITES are
// storekit.Audit and storekit.Emit — the same two calls every module store
// makes — so the actor resolution, the workspace binding, the attribution merge
// and the envelope validation are inherited rather than re-implemented. What
// this file adds is the one thing storekit cannot know: which unit is asking,
// and therefore which rows and which event names are its own to write.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/provenance"
	"github.com/gradionhq/margince/backend/pkg/extension"
)

// extensionLedger is one transaction's ledger seam.
//
// It holds the CALLER's transaction for the reason extensionCore does: the
// unit's row, the audit row and the outbox row are one commit or none, which is
// the whole write shape. It reaches for no connection of its own.
type extensionLedger struct {
	tx pgx.Tx
	// namespace is the invoking unit's `ext_<name>`, derived where the unit is
	// known and never taken from anything a handler passes. It is what makes
	// "its own tables" and "its own event names" answerable at all: a unit that
	// could spell a namespace could write another unit's history.
	namespace string
	// authority re-binds the INVOCATION's workspace, actor, correlation and
	// attribution onto whatever context a verb is handed — the Runtime's own
	// scoped. Without it a handler could pass a context it kept from an earlier,
	// higher-privileged call and put that actor on the ledger row.
	authority func(context.Context) (context.Context, error)
}

// Record writes the ledger row and stages the event for one write to the unit's
// own tables, on the caller's transaction.
//
// The two are one call because they are one obligation: the write shape the
// product holds itself to is domain row + audit row + outbox event in a single
// transaction, and a seam that let a unit take the first two would be offering
// it a weaker rule than the core keeps. It also means the event's subject is the
// ledger row's subject by construction — the entity is named once, here, so the
// two cannot drift.
func (l extensionLedger) Record(ctx context.Context, ch extension.Change, ev extension.Event) error {
	if err := ch.Validate(); err != nil {
		return err
	}
	if err := ev.Validate(); err != nil {
		return err
	}
	if err := l.ownTable(ch.Entity); err != nil {
		return err
	}
	entityID, err := ids.Parse(ch.ID)
	if err != nil {
		// Unreachable through Change.Validate's canonical-UUID grammar, and
		// checked anyway: this value becomes a uuid bind parameter, and the
		// grammar and the parser agreeing is not something this call should
		// assume on the strength of having read both.
		return fmt.Errorf("extension: %q is not a row id", ch.ID)
	}
	ctx, err = l.authorised(ctx)
	if err != nil {
		return err
	}
	ctx, err = withChangeDetail(ctx, ch.Detail)
	if err != nil {
		return err
	}

	auditID, err := storekit.Audit(ctx, l.tx, string(ch.Action), ch.Entity, entityID,
		imageOrNil(ch.Before), imageOrNil(ch.After))
	if err != nil {
		return ledgerFailure(ctx, "writing the ledger row for an extension's own write", err)
	}
	// The type is BUILT here, from the namespace the core derived and the verb
	// the unit chose. There is no path by which a unit names the left-hand side,
	// which is why the port needs no check that it did not: `person.created`
	// from a unit is not refused, it is unsayable.
	eventType := l.namespace + "." + ev.Verb
	if err := storekit.Emit(ctx, l.tx, auditID, eventType, ch.Entity, entityID,
		imageOrNil(ev.Payload)); err != nil {
		return ledgerFailure(ctx, "staging an extension's own event", err)
	}
	return nil
}

// ownTable refuses a ledger row against anything outside the invoking unit's
// namespace.
//
// A ledger row is a RECORD's history. One written against `person` would put a
// line into a core record's trail describing a write the core never made,
// attributed to a caller who never made it — and the same holds for another
// unit's table. The check is on the namespace the core derived from the
// invocation, so a unit has nothing to spell here and nothing to get past.
func (l extensionLedger) ownTable(entity string) error {
	if !strings.HasPrefix(entity, l.namespace+"_") {
		return fmt.Errorf("extension: %q is not this unit's table — a unit audits rows in its own %s_ namespace", entity, l.namespace)
	}
	return nil
}

// authorised re-binds the invocation's authority onto the context a verb was
// handed, and refuses on a Runtime the call has finished with.
func (l extensionLedger) authorised(ctx context.Context) (context.Context, error) {
	if l.authority == nil {
		return nil, errors.New("compose: this ledger seam was built without the invocation's authority, so no row can be attributed to it")
	}
	return l.authority(ctx)
}

// withChangeDetail carries the unit's free-form context into the attribution
// entry the core stamps.
//
// It travels on the CONTEXT rather than as an evidence argument because
// storekit refuses a caller-supplied `extension` evidence member outright: the
// member is core-owned, and it is the same member the unit name, version and
// via ride in. So the port re-binds a copy of the attribution the Runtime
// already bound, with detail filled in, for this one write. What a unit can
// influence stays exactly one member deep.
func withChangeDetail(ctx context.Context, detail json.RawMessage) (context.Context, error) {
	if len(detail) == 0 {
		return ctx, nil
	}
	ext, ok := provenance.ExtensionFrom(ctx)
	if !ok {
		// The Runtime binds attribution on every scoped context, so an absent
		// one is a core wiring fault rather than anything the unit did — and
		// writing the detail without it would put unattributed free-form
		// content into a core-owned evidence member.
		return nil, errors.New("compose: no extension attribution is bound to this call, so a unit's audit detail has nothing to hang off")
	}
	ext.Detail = detail
	return provenance.WithExtension(ctx, ext), nil
}

// imageOrNil hands storekit a JSON image or SQL NULL.
//
// The nil check cannot be left to storekit: a nil json.RawMessage inside an
// `any` is not a nil interface, so it would marshal to the four bytes `null`
// and land as jsonb null — a value, not the absence of one. The two read
// differently in every query that asks whether an image is there.
//
//craft:ignore naked-any storekit's jsonb seam takes any; this is the one place that decides between bytes and NULL
func imageOrNil(raw json.RawMessage) any {
	if len(raw) == 0 {
		return nil
	}
	return raw
}

// ledgerFailure reports that a ledger write failed without telling a unit how.
//
// The text of a failed audit or outbox write is a relation name, a constraint
// and a SQL state, written for the people who operate the installation. A unit
// is other people's code, so what it gets is that the write failed — which is
// all it can act on, since its own transaction is going back either way. The
// detail stays where the operators already look.
func ledgerFailure(ctx context.Context, what string, err error) error {
	slog.Default().ErrorContext(ctx, "compose: "+what, "error", err)
	return errors.New("extension: the core could not record this write, so nothing was committed")
}
