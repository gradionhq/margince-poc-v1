// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package settings

// The mechanism's behaviour that needs no database: validation, the refusal
// shape, the canonical comparison the write path uses to tell "unchanged" from
// "differently spelled", and the in-transaction read resolving an absent row to
// the declared default — that last one over a fake for the one true boundary it
// touches (the row read), because the decision it makes is not a database
// question. The rest of the database-backed half — the audit row, the write
// path's own gate, and the registry refusing an unregistered key on a write — is
// proven in internal/compose/integration, which fails loudly without Postgres
// rather than skipping.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

type overlayPair struct {
	Mode      string `json:"mode"`
	Incumbent string `json:"incumbent"`
}

func TestValidateRefusesAValueOfTheWrongType(t *testing.T) {
	e := Define[bool]("capture.probe", "capture_settings", "update", true, nil)

	err := e.ValidateJSON(json.RawMessage(`"yes"`))
	if err == nil {
		t.Fatal("a string passed validation for a bool setting; the wrong type must be refused, not coerced")
	}
	var fault apperrors.FieldFault
	if !errors.As(err, &fault) {
		t.Fatalf("refusal does not implement FieldFault, so it would report as an internal fault on the MCP surface: %v", err)
	}
	field, code, _ := fault.FieldFault()
	if field != "capture.probe" {
		t.Errorf("refusal names field %q, want the setting key so the caller knows what to change", field)
	}
	if code != "setting_type_mismatch" {
		t.Errorf("refusal code = %q, want setting_type_mismatch", code)
	}
}

func TestValidateCarriesTheOwningModulesReason(t *testing.T) {
	e := Define[string]("installation.probe", "capture_settings", "update", "EUR",
		func(v string) error {
			if len(v) != 3 {
				return errors.New("a base currency is three ISO-4217 letters")
			}
			return nil
		})

	err := e.ValidateJSON(json.RawMessage(`"EURO"`))
	if err == nil {
		t.Fatal("a four-letter currency passed a three-letter validator")
	}
	// The module's own sentence has to survive to the caller: platform could
	// not have written it, and a generic "invalid value" would not tell the
	// operator what to type instead.
	if !strings.Contains(err.Error(), "three ISO-4217 letters") {
		t.Errorf("refusal lost the module's reason: %v", err)
	}
}

func TestValidateAcceptsWhenTheEntryDeclaresNoValidator(t *testing.T) {
	e := Define[bool]("capture.probe", "capture_settings", "update", true, nil)
	if err := e.ValidateJSON(json.RawMessage(`false`)); err != nil {
		t.Fatalf("a well-typed value was refused by an entry with no validator: %v", err)
	}
}

// The regression this guards: a candidate is encoded by Go, while a stored
// value comes back from Postgres, which normalizes jsonb on its own terms.
// Comparing the two byte-for-byte makes an unchanged composite look changed,
// so every write would store a row and an audit entry recording nothing.
func TestCanonicalFormMakesAReEncodedValueComparable(t *testing.T) {
	e := Define[overlayPair]("overlay.probe", "capture_settings", "update", overlayPair{}, nil)

	// Field order reversed and whitespace added, exactly as a jsonb round-trip
	// is free to hand it back.
	stored := json.RawMessage(`{"incumbent": "hubspot",   "mode": "overlay"}`)
	next, err := json.Marshal(overlayPair{Mode: "overlay", Incumbent: "hubspot"})
	if err != nil {
		t.Fatalf("encoding the candidate value: %v", err)
	}

	canonical, err := e.CanonicalJSON(stored)
	if err != nil {
		t.Fatalf("canonicalizing the stored value: %v", err)
	}
	if string(canonical) != string(next) {
		t.Errorf("a re-encoded identical value did not compare equal:\n stored canonical = %s\n candidate        = %s\n"+
			"the write would be a no-op recorded as a change", canonical, next)
	}
}

func TestCanonicalFormStillDistinguishesAGenuineChange(t *testing.T) {
	e := Define[overlayPair]("overlay.probe", "capture_settings", "update", overlayPair{}, nil)
	next, err := json.Marshal(overlayPair{Mode: "native"})
	if err != nil {
		t.Fatalf("encoding the candidate value: %v", err)
	}

	canonical, err := e.CanonicalJSON(json.RawMessage(`{"mode":"overlay","incumbent":"hubspot"}`))
	if err != nil {
		t.Fatalf("canonicalizing the stored value: %v", err)
	}
	if string(canonical) == string(next) {
		t.Error("a genuinely different value compared as unchanged; the write would be silently dropped")
	}
}

func TestAnUndecodableStoredValueRefusesRatherThanOverwriting(t *testing.T) {
	e := Define[bool]("capture.probe", "capture_settings", "update", true, nil)

	// Whatever wrote this, this build cannot read it. Treating it as "different"
	// would overwrite it and destroy the only evidence of what happened.
	_, err := e.CanonicalJSON(json.RawMessage(`{"unexpected":"shape"}`))
	if err == nil {
		t.Fatal("an undecodable stored value was silently treated as comparable")
	}
	if !strings.Contains(err.Error(), "cannot decode") {
		t.Errorf("refusal does not say the stored value is unreadable: %v", err)
	}
}

func TestAnUnregisteredKeyIsRefusedRatherThanTreatedAsUnset(t *testing.T) {
	s := New(nil, NewRegistry(Define[bool]("capture.probe", "capture_settings", "update", true, nil)))

	if _, err := s.lookup("capture.probe"); err != nil {
		t.Fatalf("a registered setting did not resolve: %v", err)
	}
	// The pool is nil, so reaching SQL would panic — proving the refusal
	// happens before any database work, on both the read and the write path.
	_, err := s.lookup("capture.never_declared")
	if err == nil {
		t.Fatal("an unregistered key resolved; a typo must not read as a real setting")
	}
	if !errors.Is(err, apperrors.ErrNotFound) {
		t.Errorf("unregistered key returned %v, want ErrNotFound so the surface reports 404", err)
	}
}

func TestTheRegistryKeepsAnEntrysGovernance(t *testing.T) {
	s := New(nil, NewRegistry(Define[bool]("capture.probe", "capture_settings", "update", true, nil)))
	def, err := s.lookup("capture.probe")
	if err != nil {
		t.Fatalf("resolving the entry: %v", err)
	}
	if def.Object() != "capture_settings" {
		t.Errorf("registry lost the RBAC object: %q", def.Object())
	}
	if def.AuditVerb() != "update" {
		t.Errorf("registry lost the audit verb: %q", def.AuditVerb())
	}
}

func TestDefaultIsTheValueUntilSomeoneChangesIt(t *testing.T) {
	e := Define[bool]("capture.probe", "capture_settings", "update", true, nil)
	raw, err := e.DefaultJSON()
	if err != nil {
		t.Fatalf("encoding the default: %v", err)
	}
	if string(raw) != "true" {
		t.Errorf("default encoded as %s, want true — a read of an unset setting resolves to this", raw)
	}
}

// readerCtx binds a human holding read on the entry's object, which is what the
// HTTP middleware would have resolved before any store method runs.
func readerCtx(object string) context.Context {
	return principal.WithActor(context.Background(), principal.Principal{
		Type: principal.PrincipalHuman, ID: "human:test",
		Permissions: principal.Permissions{
			RoleKeys: []string{"fixture"},
			Objects:  map[string]principal.ObjectGrant{object: {Read: true}},
		},
	})
}

// TestGetTxResolvesAnAbsentRowToTheDefault is the difference between GetTx and
// RequireTx, and the reason both exist: a posture nobody has changed is simply
// off. Refusing an unset value here would mean an installation that never opened
// the retention screen could not run its nightly pass at all.
func TestGetTxResolvesAnAbsentRowToTheDefault(t *testing.T) {
	e := Define[bool]("privacy.probe", "retention_policy", "update", false, nil)

	got, err := GetTx(readerCtx("retention_policy"), &settingRowTx{absent: true}, e)
	if err != nil {
		t.Fatalf("GetTx over a setting with no stored row: %v", err)
	}
	if got {
		t.Error("an unset setting read as true, not as its registered default")
	}

	on := Define[bool]("privacy.probe_on", "retention_policy", "update", true, nil)
	got, err = GetTx(readerCtx("retention_policy"), &settingRowTx{absent: true}, on)
	if err != nil {
		t.Fatalf("GetTx over a setting with no stored row: %v", err)
	}
	if !got {
		t.Error("an unset setting read as false rather than the declared default; the default is the value, not the zero value")
	}
}

func TestGetTxReadsTheStoredValue(t *testing.T) {
	// Default false, stored true: only a real read of the row can tell them
	// apart, so a decode that fell back to the default would fail here.
	e := Define[bool]("privacy.probe", "retention_policy", "update", false, nil)

	got, err := GetTx(readerCtx("retention_policy"), &settingRowTx{stored: json.RawMessage(`true`)}, e)
	if err != nil {
		t.Fatalf("GetTx over a stored row: %v", err)
	}
	if !got {
		t.Error("a stored true read as false; the stored row must win over the registered default")
	}
}

// TestGetTxTakesTheObjectGateBeforeReading matters because the `setting` table
// carries no RLS: this gate is the only control on it. The fake refuses to serve
// a read at all, so a passing case here would mean the SQL ran first.
func TestGetTxTakesTheObjectGateBeforeReading(t *testing.T) {
	e := Define[bool]("privacy.probe", "retention_policy", "update", false, nil)

	_, err := GetTx(readerCtx("capture_settings"), &settingRowTx{refuseRead: true}, e)
	if !errors.Is(err, apperrors.ErrPermissionDenied) {
		t.Fatalf("GetTx without the object grant = %v, want ErrPermissionDenied", err)
	}
}

func TestGetTxReportsAnUndecodableStoredValue(t *testing.T) {
	e := Define[bool]("privacy.probe", "retention_policy", "update", false, nil)

	_, err := GetTx(readerCtx("retention_policy"), &settingRowTx{stored: json.RawMessage(`"yes"`)}, e)
	if err == nil {
		t.Fatal("a string stored for a bool setting decoded silently; a value this build cannot read must refuse")
	}
	if !strings.Contains(err.Error(), "privacy.probe") {
		t.Errorf("refusal does not name the setting that could not be decoded: %v", err)
	}
}

// settingRowTx is the DB boundary — the only boundary this file fakes (T11). It
// answers the one QueryRow currentJSON issues, either with a stored value or
// with pgx.ErrNoRows for the absent row. refuseRead makes the read itself a test
// failure, for the case that must never reach SQL. Every other pgx.Tx method
// panics: GetTx calls none of them, so reaching one is this test's bug.
type settingRowTx struct {
	stored     json.RawMessage
	absent     bool
	refuseRead bool
	// execErr, when set, is what Exec returns — the database refusing the insert
	// Seed issues, which is a different outcome from the row already existing.
	execErr error
}

// settingRow carries one answer back to the caller's Scan.
type settingRow struct {
	value json.RawMessage
	err   error
}

func (r settingRow) Scan(dest ...any) error {
	if r.err != nil {
		return r.err
	}
	if len(dest) != 1 {
		return fmt.Errorf("settingRow: scanned into %d destinations, want 1", len(dest))
	}
	target, ok := dest[0].(*json.RawMessage)
	if !ok {
		return fmt.Errorf("settingRow: scanned into %T, want *json.RawMessage", dest[0])
	}
	*target = r.value
	return nil
}

func (f *settingRowTx) QueryRow(_ context.Context, sql string, _ ...any) pgx.Row {
	if f.refuseRead {
		return settingRow{err: errors.New("settingRowTx: the read must not happen before the object gate")}
	}
	if !strings.Contains(sql, "FROM setting") {
		return settingRow{err: fmt.Errorf("settingRowTx: unexpected statement %q", sql)}
	}
	if f.absent {
		return settingRow{err: pgx.ErrNoRows}
	}
	return settingRow{value: f.stored}
}

func (f *settingRowTx) Begin(context.Context) (pgx.Tx, error) {
	panic("settingRowTx: Begin not implemented")
}
func (f *settingRowTx) Commit(context.Context) error { panic("settingRowTx: Commit not implemented") }
func (f *settingRowTx) Rollback(context.Context) error {
	panic("settingRowTx: Rollback not implemented")
}

func (f *settingRowTx) CopyFrom(context.Context, pgx.Identifier, []string, pgx.CopyFromSource) (int64, error) {
	panic("settingRowTx: CopyFrom not implemented")
}

func (f *settingRowTx) SendBatch(context.Context, *pgx.Batch) pgx.BatchResults {
	panic("settingRowTx: SendBatch not implemented")
}

func (f *settingRowTx) LargeObjects() pgx.LargeObjects {
	panic("settingRowTx: LargeObjects not implemented")
}

func (f *settingRowTx) Prepare(context.Context, string, string) (*pgconn.StatementDescription, error) {
	panic("settingRowTx: Prepare not implemented")
}

func (f *settingRowTx) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	if f.execErr != nil {
		return pgconn.CommandTag{}, f.execErr
	}
	panic("settingRowTx: Exec not implemented")
}

func (f *settingRowTx) Query(context.Context, string, ...any) (pgx.Rows, error) {
	panic("settingRowTx: Query not implemented")
}
func (f *settingRowTx) Conn() *pgx.Conn { panic("settingRowTx: Conn not implemented") }

// Seed's two refusals, neither of which reaches the conflict clause at all. They
// are the paths where `stored` must be false for a reason other than "a row was
// already there", and a caller that read a false as "discarded" would report a
// discard where the write never happened.
func TestSeedRefusesAValueTheEntryWouldNotAccept(t *testing.T) {
	tx := &settingRowTx{refuseRead: true}

	entry := Define[string]("installation.seedprobe", "capture_settings", "update", "EUR",
		func(v string) error {
			if len(v) != 3 {
				return fmt.Errorf("a currency is three letters, got %q", v)
			}
			return nil
		})

	stored, err := Seed(context.Background(), tx, entry, json.RawMessage(`"EURO"`))
	if err == nil {
		t.Fatal("Seed accepted a value the entry's validator rejects; bootstrap would have written it")
	}
	if stored {
		t.Error("a refused seed reported that it stored something")
	}
}

func TestSeedReportsNotStoredWhenTheInsertItselfFails(t *testing.T) {
	refused := errors.New("connection reset")
	tx := &settingRowTx{execErr: refused}

	entry := Define[bool]("capture.seedprobe", "capture_settings", "update", true, nil)

	stored, err := Seed(context.Background(), tx, entry, json.RawMessage(`true`))
	if !errors.Is(err, refused) {
		t.Fatalf("Seed swallowed the database's refusal: %v", err)
	}
	if stored {
		t.Error("a failed insert reported that it stored the value, which a caller would read as a successful seed")
	}
}

func TestSeedValueRefusesAValueItCannotEncode(t *testing.T) {
	tx := &settingRowTx{refuseRead: true}

	entry := Define[chan int]("capture.unencodable", "capture_settings", "update", nil, nil)

	stored, err := SeedValue(context.Background(), tx, entry, make(chan int))
	if err == nil {
		t.Fatal("SeedValue accepted a value json.Marshal cannot represent")
	}
	if stored {
		t.Error("a value that never became JSON was reported as stored")
	}
}
