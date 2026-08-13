// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// What the ledger port refuses before any SQL runs, and what a unit cannot say
// at all. Every case here holds with NO database: a write reaching a pool to be
// told no would mean the check ran too late.

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/provenance"
	"github.com/gradionhq/margince/backend/pkg/extension"
)

// ledgerRowID is a canonical UUID, which is what the entity_id column takes.
const ledgerRowID = "018f3a2b-6c7d-7e8f-9a0b-1c2d3e4f5061"

// notesLedger is the port as the notes unit holds it, over a transaction that
// PANICS if anything is executed on it.
//
// That is the assertion, not the scaffolding. Every refusal below has to happen
// before a statement runs — a write reaching the database to be told no would
// mean the check ran too late — and a test that only asked "was there an error"
// would pass just as happily on a refusal the DATABASE made, or on one a nil
// handle produced. Under this transaction, a check that stopped refusing shows
// up as a panic naming the statement it let through.
func notesLedger() extensionLedger {
	return extensionLedger{
		tx:        refusingTx{},
		namespace: "ext_notes",
		// The Runtime's own scoped, standing in — and it binds the actor and the
		// correlation as well as the attribution ON PURPOSE. storekit refuses a
		// write carrying neither, so a stub that bound only the attribution
		// would stop every case below on the plumbing rather than on the
		// refusal it is named for, and the panicking transaction would never be
		// reached to prove anything at all.
		authority: func(ctx context.Context) (context.Context, error) {
			ctx = principal.WithActor(ctx, principal.Principal{
				Type: principal.PrincipalHuman, ID: "human:the-caller", UserID: ids.NewV7(),
			})
			ctx = principal.WithCorrelationID(ctx, ids.NewV7())
			return provenance.WithExtension(ctx, provenance.Extension{
				Unit: "notes", Version: "1.0.0", Via: "tool/add_note",
			}), nil
		},
	}
}

// refusingTx is a pgx.Tx no statement may reach. Every method panics: the port
// is supposed to refuse before any of them, so reaching one is the defect these
// tests exist to catch rather than a path worth stubbing.
type refusingTx struct{}

func (refusingTx) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	panic("extledger_test: a refusal let a statement through to the database")
}

func (refusingTx) Query(context.Context, string, ...any) (pgx.Rows, error) {
	panic("extledger_test: a refusal let a query through to the database")
}

func (refusingTx) QueryRow(context.Context, string, ...any) pgx.Row {
	panic("extledger_test: a refusal let a query through to the database")
}
func (refusingTx) Begin(context.Context) (pgx.Tx, error) { panic("refusingTx: Begin") }
func (refusingTx) Commit(context.Context) error          { panic("refusingTx: Commit") }
func (refusingTx) Rollback(context.Context) error        { panic("refusingTx: Rollback") }

func (refusingTx) CopyFrom(context.Context, pgx.Identifier, []string, pgx.CopyFromSource) (int64, error) {
	panic("refusingTx: CopyFrom")
}

func (refusingTx) SendBatch(context.Context, *pgx.Batch) pgx.BatchResults {
	panic("refusingTx: SendBatch")
}
func (refusingTx) LargeObjects() pgx.LargeObjects { panic("refusingTx: LargeObjects") }

func (refusingTx) Prepare(context.Context, string, string) (*pgconn.StatementDescription, error) {
	panic("refusingTx: Prepare")
}
func (refusingTx) Conn() *pgx.Conn { panic("refusingTx: Conn") }

var _ pgx.Tx = refusingTx{}

// A unit audits its own rows. The refusal is the point of the namespace being
// derived from the INVOCATION: a ledger row against a core record would be a
// line in that record's history describing a write the core never made, and one
// against a sibling unit's table would be the same forgery a directory away.
func TestTheLedgerRefusesATableTheUnitDoesNotOwn(t *testing.T) {
	ledger := notesLedger()
	for name, entity := range map[string]string{
		"a core record":            "person",
		"the audit log itself":     "audit_log",
		"another unit's table":     "ext_de_retention",
		"a near-miss on the name":  "ext_notes2_note",
		"the namespace with no _":  "ext_notes",
		"a schema-qualified table": "ext.ext_notes_note",
	} {
		err := ledger.Record(context.Background(),
			extension.Change{Action: extension.AuditCreate, Entity: entity, ID: ledgerRowID},
			extension.Event{Verb: "thing_added"})
		if err == nil {
			t.Errorf("the ledger accepted %s (%q)", name, entity)
		}
	}
}

// The published rules are enforced at the port too, so a unit that skipped
// Change.Validate cannot reach the audit statement with a value the column
// would refuse — where the report is a SQLSTATE.
func TestTheLedgerRefusesAChangeTheColumnsCannotHold(t *testing.T) {
	ledger := notesLedger()
	for name, ch := range map[string]extension.Change{
		"an action outside the ledger's vocabulary": {
			Action: "merge", Entity: "ext_notes_note", ID: ledgerRowID,
		},
		"an id that is not a UUID": {
			Action: extension.AuditCreate, Entity: "ext_notes_note", ID: "note-7",
		},
		"an image that is not JSON": {
			Action: extension.AuditCreate, Entity: "ext_notes_note", ID: ledgerRowID,
			After: json.RawMessage("{"),
		},
	} {
		if err := ledger.Record(context.Background(), ch, extension.Event{Verb: "thing_added"}); err == nil {
			t.Errorf("the ledger accepted %s", name)
		}
	}
}

// A port built without the invocation's authority writes nothing. It is a
// wiring fault rather than anything a unit did, and it must fail loudly: a
// ledger row with no bound actor would be attributed to nobody.
func TestTheLedgerRefusesAWriteItCannotAttribute(t *testing.T) {
	unbound := extensionLedger{tx: refusingTx{}, namespace: "ext_notes"}
	err := unbound.Record(context.Background(),
		extension.Change{Action: extension.AuditCreate, Entity: "ext_notes_note", ID: ledgerRowID},
		extension.Event{Verb: "thing_added"})
	if err == nil || !strings.Contains(err.Error(), "authority") {
		t.Fatalf("err = %v, want the missing-authority refusal", err)
	}
}

// The detail a unit supplies rides INSIDE the core's attribution entry, and
// nowhere else. The core's own members are beside it and are not the unit's to
// write — storekit refuses a caller-supplied `extension` member outright, so
// this is how a unit says anything at all about its own write.
func TestAUnitsDetailRidesInsideTheAttributionEntry(t *testing.T) {
	detail := json.RawMessage(`{"cause":"activity.archived"}`)
	ctx, err := withChangeDetail(provenance.WithExtension(context.Background(), provenance.Extension{
		Unit: "notes", Version: "1.0.0", Via: "subscription/withdraw_filing",
	}), detail)
	if err != nil {
		t.Fatal(err)
	}
	bound, ok := provenance.ExtensionFrom(ctx)
	if !ok {
		t.Fatal("the attribution did not survive the detail binding")
	}
	entry := bound.EvidenceEntry()
	for member, want := range map[string]string{
		"unit": "notes", "version": "1.0.0", "via": "subscription/withdraw_filing",
	} {
		if got, _ := entry[member].(string); got != want {
			t.Errorf("evidence.extension.%s = %v, want %q — the core's members must survive a unit's detail", member, entry[member], want)
		}
	}
	got, isRaw := entry["detail"].(json.RawMessage)
	if !isRaw || string(got) != string(detail) {
		t.Errorf("evidence.extension.detail = %v, want %s", entry["detail"], detail)
	}

	// No detail leaves the member OUT: `"detail": null` would be a unit saying
	// something where it said nothing.
	if entry := (provenance.Extension{Unit: "notes"}).EvidenceEntry(); entry["detail"] != nil {
		t.Errorf("an empty detail rendered as %v, want the member absent", entry["detail"])
	}
}

// A unit that has no attribution bound cannot supply detail either: the entry
// it would hang off does not exist, and writing free-form content into a
// core-owned evidence member with nothing naming its author is worse than
// refusing.
func TestDetailWithNoAttributionIsRefused(t *testing.T) {
	if _, err := withChangeDetail(context.Background(), json.RawMessage(`{"a":1}`)); err == nil {
		t.Fatal("detail was accepted with no attribution bound")
	}
	// With no detail there is nothing to hang, so an unattributed context is
	// simply passed through — the ordinary core write, which stamps nothing.
	if _, err := withChangeDetail(context.Background(), nil); err != nil {
		t.Fatalf("a write with no detail was refused: %v", err)
	}
}

// A published event's TYPE is the namespace the core derived plus the verb the
// unit chose. The half a unit spells is the verb, so publishing under another
// unit's name is not refused — it is unsayable, and the grammar is what makes
// it so. The derivation itself is asserted over a real transaction in the
// database lane, where the type reaches the outbox.
func TestAVerbCannotSmuggleANamespace(t *testing.T) {
	for _, verb := range []string{"ext_de.note_added", "person.created", "Note_Added", ""} {
		if err := (extension.Event{Verb: verb}).Validate(); err == nil {
			t.Errorf("the event grammar accepted %q as a verb", verb)
		}
	}
}

// An event the bus could not carry refuses the WHOLE call, before the ledger
// row is written: the two halves are one write, so a malformed event must not
// leave a history behind claiming something was announced.
func TestAnEventTheBusRefusesWritesNoLedgerRow(t *testing.T) {
	err := notesLedger().Record(context.Background(),
		extension.Change{Action: extension.AuditCreate, Entity: "ext_notes_note", ID: ledgerRowID},
		extension.Event{Verb: "Note Added"})
	if err == nil {
		t.Fatal("the port accepted an event the bus grammar refuses")
	}
}

// imageOrNil decides between bytes and SQL NULL, and the distinction is not
// cosmetic: a nil json.RawMessage inside an `any` marshals to the four bytes
// `null`, which lands as jsonb null — a VALUE. "There was no such state" and
// "the state was null" read differently in every query that asks.
func TestAnAbsentImageIsNullAndNotTheBytesNull(t *testing.T) {
	if got := imageOrNil(nil); got != nil {
		t.Errorf("imageOrNil(nil) = %v, want a nil bind parameter", got)
	}
	if got := imageOrNil(json.RawMessage{}); got != nil {
		t.Errorf("imageOrNil(empty) = %v, want a nil bind parameter", got)
	}
	raw := json.RawMessage(`{"body":"hello"}`)
	got, ok := imageOrNil(raw).(json.RawMessage)
	if !ok || string(got) != string(raw) {
		t.Errorf("imageOrNil(%s) = %v, want the bytes through unchanged", raw, got)
	}
}

// A failed ledger write tells a unit that the write failed and nothing about
// how: the text of one is a relation, a constraint and a SQL state, written for
// the people who operate the installation.
func TestALedgerFailureLeaksNoCoreDetail(t *testing.T) {
	internal := errors.New(`insert into "audit_log" violates constraint audit_log_action_check`)
	got := ledgerFailure(context.Background(), "probing", internal)
	if got == nil {
		t.Fatal("a failed ledger write was mapped to success")
	}
	for _, leaked := range []string{"audit_log", "constraint"} {
		if strings.Contains(got.Error(), leaked) {
			t.Errorf("the core's own error text reached the unit: %v", got)
		}
	}
}
