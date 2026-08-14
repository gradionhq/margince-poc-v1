// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// The ledger seam — what a unit's write to its OWN tables records, and the
// event it publishes — is part of the published extension surface.
//
//margince:extension-surface

package extension

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
)

// AuditAction is what a write to a unit's own table DID, in the core's own
// audit vocabulary rather than a second one invented for the tier.
//
// The five below are the actions that have an honest meaning for a row a unit
// owns. The core's ledger admits more (merge, promote, export, login…), and
// each of those is a thing the PRODUCT does to a record it understands — a unit
// naming one would be describing an operation nothing else in the system agrees
// happened.
type AuditAction string

const (
	// AuditCreate is a row that did not exist before this write.
	AuditCreate AuditAction = "create"
	// AuditUpdate is a row that changed.
	AuditUpdate AuditAction = "update"
	// AuditArchive is a row withdrawn from use but still present.
	AuditArchive AuditAction = "archive"
	// AuditRestore is an archived row brought back.
	AuditRestore AuditAction = "restore"
	// AuditErase is a row that is gone — the honest action for a hard DELETE,
	// where the ledger row and its before-image are the only remaining trace.
	AuditErase AuditAction = "erase"
)

// Validate reports whether the action is one the ledger admits. The refusal
// names the vocabulary because the alternative is a constraint violation from
// the database, whose message is written for nobody.
func (a AuditAction) Validate() error {
	switch a {
	case AuditCreate, AuditUpdate, AuditArchive, AuditRestore, AuditErase:
		return nil
	}
	return fmt.Errorf("extension: %q is not an audit action (create, update, archive, restore, erase)", string(a))
}

// entityGrammar is a unit table's name as the ledger records it: the unit's own
// `ext_<namespace>_<table>` identifier, unqualified. Unqualified because
// audit_log.entity_type names a KIND of record, not a path to one — the `ext.`
// schema belongs in the SQL a unit writes, and a reader joining the ledger to
// anything else would have to strip it back off.
var entityGrammar = regexp.MustCompile(`^ext_[a-z0-9_]+$`)

// uuidGrammar is the canonical spelling of a row id, checked here because
// entity_id is a uuid column: a non-canonical string fails at the driver,
// inside the transaction, with a message about a text-to-uuid cast.
var uuidGrammar = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// Change is one write to a unit's own table, as the ledger will record it.
//
// A unit's own SQL is the one write in the product that the core cannot
// describe — it has no entity type, no id and no field images to derive,
// because it is a statement the core does not parse (see Tx). So the unit
// describes it, and everything else about the ledger row — the actor, the
// workspace, the authorization rule, the attribution naming the unit — is
// stamped by the core from the invocation and is not the unit's to influence.
type Change struct {
	// Action is what the write did.
	Action AuditAction

	// Entity is the unit's own table, `ext_<namespace>_<table>`, unqualified.
	// It must be a table in the INVOKING unit's namespace: a ledger row is a
	// record's history, and one written against a core record — or another
	// unit's — would be a line in that record's trail describing a write its
	// owner never made.
	Entity string

	// ID is the row's id, a canonical UUID.
	ID string

	// Before and After are the row's field images either side of the write.
	// They are the RECORD's own shape: what the row looked like, not why it
	// changed. A create leaves Before empty; an erase leaves After empty.
	//
	// json.RawMessage rather than a Go value, because these bytes become
	// jsonb: the raw type says so, and it makes a value that cannot be
	// marshaled impossible to hand over in the first place.
	Before json.RawMessage
	After  json.RawMessage

	// Detail is context ABOUT the write — which core event caused it, which of
	// the unit's own rules fired — and never the record's fields. Folding
	// operational context into the images above makes every downstream reader
	// of field history see changes that never happened on the record.
	//
	// It lands under the unit's own attribution entry, at
	// `evidence.extension.detail`, so the whole tier stays inside the one
	// evidence member the core stamps for it. A unit cannot write the members
	// beside it (the unit name, its version, the surface the call arrived on):
	// those are the core's answer to "what carried this", and a unit able to
	// write them could sign another unit's name.
	Detail json.RawMessage
}

// Validate checks the SHAPE of a change — everything decidable without knowing
// which unit is writing it. Whether Entity is in the CALLER's namespace is the
// core's check, made at the port against the invocation, for the same reason a
// Job's tier is checked where it is registered rather than where it is declared.
func (c Change) Validate() error {
	if err := c.Action.Validate(); err != nil {
		return err
	}
	if !entityGrammar.MatchString(c.Entity) {
		return fmt.Errorf("extension: %q is not a unit table (ext_<namespace>_<table>, unqualified) — a unit audits its own rows", c.Entity)
	}
	if !uuidGrammar.MatchString(c.ID) {
		return fmt.Errorf("extension: %q is not a row id — an id is a canonical lower-case UUID", c.ID)
	}
	for _, image := range []struct {
		what string
		raw  json.RawMessage
	}{{"before", c.Before}, {"after", c.After}, {"detail", c.Detail}} {
		if len(image.raw) > 0 && !json.Valid(image.raw) {
			return fmt.Errorf("extension: the %s image is not valid JSON, and the ledger column holding it is jsonb", image.what)
		}
	}
	// An image the ACTION says cannot exist. A create with a before-image and an
	// erase with an after-image are each a ledger row that contradicts itself,
	// and the contradiction is permanent: audit_log is append-only, so nobody
	// can correct it afterwards. Refusing the write is the only moment this can
	// be said.
	if c.Action == AuditCreate && len(c.Before) > 0 {
		return errors.New("extension: a create carries a before image — the row did not exist, so there was no state to record")
	}
	if c.Action == AuditErase && len(c.After) > 0 {
		return errors.New("extension: an erase carries an after image — the row is gone, so there is no state left to record")
	}
	return nil
}

// MaxEventPayloadBytes caps what one published event may carry.
//
// A bus entry is read by every consumer of its stream and held in memory by the
// broker until each of them has acked it, so an oversized payload is not the
// publisher's problem — it is everyone else's, and it arrives without warning.
// The cap is generous next to what a payload is FOR (see Event.Payload) and
// small next to anything that would hurt.
const MaxEventPayloadBytes = 32 * 1024

// Event is one domain fact a unit publishes: the name it gives the write it
// just described, and what a listener needs in order to act on it.
//
// It is a PARAMETER of Tx.Record rather than a call of its own, and that is the
// whole of how the write shape is kept. An event with no ledger row is
// unauditable — the bus contract requires every envelope to name the row
// written in its own transaction — and a ledger row with no event is a change
// the rest of the installation is never told about, which is the exemption the
// core does not grant itself either. Neither is expressible here.
type Event struct {
	// Verb is what happened, lower snake_case and past tense by convention
	// (`note_added`, `filing_withdrawn`).
	//
	// It is a VERB, not a type: the core prefixes the publishing unit's own
	// namespace, so the type on the bus is `ext_<namespace>.<verb>`. A unit
	// therefore cannot publish under another unit's name, or inside a core
	// family, because this surface gives it no way to say either.
	Verb string

	// Payload is what a consumer needs in order to DECIDE whether to read the
	// record, and nothing more. An event carries a ref, never the body: a
	// consumer that needs the record reads it back under its own capabilities,
	// which is what keeps a fact on the bus from becoming a copy of the record
	// that goes stale. Optional — many facts are fully carried by their subject.
	//
	// The DOCUMENT survives, the bytes do not. It is stored as jsonb on its way
	// out, which normalizes whitespace and key order and collapses a duplicate
	// member, so a consumer sees the value this carried and never its
	// formatting. Nothing should be signed or compared as raw text across it.
	Payload json.RawMessage
}

// eventVerbGrammar is the verb half of an extension event type. The namespace
// half is the core's to supply, so this is the whole of what a unit spells.
var eventVerbGrammar = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

// Validate checks the shape of an event: the verb's grammar, the payload's
// JSON, and the payload's size against MaxEventPayloadBytes.
func (e Event) Validate() error {
	if !eventVerbGrammar.MatchString(e.Verb) {
		return fmt.Errorf("extension: %q is not an event verb (lower snake_case, e.g. note_added)", e.Verb)
	}
	if len(e.Payload) > MaxEventPayloadBytes {
		return fmt.Errorf("extension: the %s event's payload is %d bytes, over the %d-byte cap every consumer of the stream pays for", e.Verb, len(e.Payload), MaxEventPayloadBytes)
	}
	if len(e.Payload) > 0 && !json.Valid(e.Payload) {
		return fmt.Errorf("extension: the %s event's payload is not valid JSON", e.Verb)
	}
	return nil
}

// Validate on Change and Event is the whole of what this package can decide.
// Whether the entity and the verb belong to the INVOKING unit is the core's
// check, made at the port against the invocation — see Tx.Record.
