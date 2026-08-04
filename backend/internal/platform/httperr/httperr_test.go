// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package httperr

// The problem-detail boundary: a sentinel match carries its crafted
// domain detail onto the wire, but never the text of an infrastructure
// failure that happened to be wrapped into the same chain — that text
// (SQL fragments, hosts, ports) is operator material and goes to the
// server log instead. And a malformed keyset cursor is the client's
// fault: 422, never a 500.

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
)

func writeAndDecode(t *testing.T, err error) (int, map[string]any) {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/test", nil)
	Write(rec, req, err)
	var body map[string]any
	if decodeErr := json.Unmarshal(rec.Body.Bytes(), &body); decodeErr != nil {
		t.Fatalf("decoding problem body %q: %v", rec.Body.String(), decodeErr)
	}
	return rec.Code, body
}

func TestWrite_craftedDomainDetailFlows(t *testing.T) {
	err := fmt.Errorf("approval expired 15m0s after decision: %w", apperrors.ErrConflict)
	status, body := writeAndDecode(t, err)
	if status != http.StatusConflict {
		t.Fatalf("status = %d, want 409", status)
	}
	if detail := body["detail"]; detail != err.Error() {
		t.Errorf("detail = %q, want the crafted domain message %q", detail, err.Error())
	}
}

func TestWrite_infrastructureCauseNeverReachesTheWire(t *testing.T) {
	cases := map[string]error{
		"postgres": fmt.Errorf("%w: %w", apperrors.ErrConflict,
			&pgconn.PgError{Severity: "ERROR", Code: "23505", Message: "duplicate key on host db-internal:5432"}),
		"network": fmt.Errorf("%w: %w", apperrors.ErrConflict,
			&fakeNetError{msg: "dial tcp 10.0.0.7:5432: connection refused"}),
	}
	for name, err := range cases {
		t.Run(name, func(t *testing.T) {
			status, body := writeAndDecode(t, err)
			if status != http.StatusConflict {
				t.Fatalf("status = %d, want 409 (the sentinel still maps)", status)
			}
			detail, _ := body["detail"].(string)
			if detail != apperrors.ErrConflict.Error() {
				t.Errorf("detail = %q, want the sentinel's canonical text %q", detail, apperrors.ErrConflict.Error())
			}
			if strings.Contains(detail, "5432") || strings.Contains(detail, "10.0.0.7") {
				t.Errorf("infrastructure text leaked onto the wire: %q", detail)
			}
		})
	}
}

func TestWrite_malformedCursorIsAClientFault(t *testing.T) {
	_, err := storekit.DecodeCursor("garbage!!")
	if err == nil {
		t.Fatal("garbage cursor decoded")
	}
	status, body := writeAndDecode(t, fmt.Errorf("listing people: %w", err))
	if status != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422", status)
	}
	if body["code"] != "validation_error" {
		t.Errorf("code = %v, want validation_error", body["code"])
	}
}

// fakeNetError satisfies net.Error without opening a socket.
type fakeNetError struct{ msg string }

func (e *fakeNetError) Error() string   { return e.msg }
func (e *fakeNetError) Timeout() bool   { return false }
func (e *fakeNetError) Temporary() bool { return false }

// Both seam errors document "422 on every surface"; this pins the HTTP half
// of that promise for each, so a future seam error added without a branch in
// clientInputValidation cannot silently answer 500 to a client mistake. The
// unserved case uses `deal` — a type EntityTypes() DOES return — because that
// is what the raise sites actually produce: a valid record type arriving at a
// provider that does not own it, not a misspelling.
func TestWrite_datasourceSeamRefusalsAreClientFaults(t *testing.T) {
	for _, tc := range []struct {
		name, field, code, wants string
		err                      error
	}{
		{
			name:  "a valid entity_type at a provider that does not serve it",
			err:   &datasource.UnsupportedEntityError{Type: "deal"},
			field: "entity_type",
			code:  "unsupported_entity_type",
			wants: "deal is not served here",
		},
		{
			// The seam's own typed key refusal, which is what StrictDecode
			// raises: an untyped cause is indistinguishable from a library's
			// message and is masked, which is the point of the type.
			name:  "a write payload the seam could not decode",
			err:   &datasource.FieldDecodeError{Cause: &datasource.UnknownFieldError{Fields: []string{"naem"}}},
			field: "fields",
			code:  "invalid_field",
			wants: "naem",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			status, body := writeAndDecode(t, fmt.Errorf("creating a record: %w", tc.err))
			if status != http.StatusUnprocessableEntity {
				t.Fatalf("status = %d, want 422 — a client naming the wrong %s is never a server fault", status, tc.field)
			}
			if body["code"] != "validation_error" {
				t.Errorf("code = %v, want validation_error", body["code"])
			}
			detail, _ := body["detail"].(string)
			if !strings.Contains(detail, tc.wants) {
				t.Errorf("detail = %q, want it to say %q so the caller can see what to correct", detail, tc.wants)
			}
			// The machine-readable half: a client that branches on the
			// structured error, rather than on prose, must find the field
			// and the code it is supposed to branch on.
			details, ok := body["details"].(map[string]any)
			if !ok {
				t.Fatalf("details = %v, want the structured problem member", body["details"])
			}
			errs, ok := details["errors"].([]any)
			if !ok || len(errs) != 1 {
				t.Fatalf("details.errors = %v, want exactly one structured entry", details["errors"])
			}
			entry, ok := errs[0].(map[string]any)
			if !ok {
				t.Fatalf("details.errors[0] = %v, want an object", errs[0])
			}
			if entry["field"] != tc.field {
				t.Errorf("details.errors[0].field = %v, want %q", entry["field"], tc.field)
			}
			if entry["code"] != tc.code {
				t.Errorf("details.errors[0].code = %v, want %q", entry["code"], tc.code)
			}
		})
	}
}

// Classify is the verdict every surface reads, so the scrubbing that keeps
// operator text off the REST wire has to live in it rather than in Write —
// otherwise the MCP dispatcher, rendering the same Fault, would put a driver
// message in front of an untrusted agent. Detail carries the sentinel's own
// words and the raw cause comes back separately, for the surface to log.
func TestClassify_withholdsInfrastructureTextFromEverySurface(t *testing.T) {
	cause := &pgconn.PgError{Severity: "ERROR", Code: "23505", Message: "duplicate key on host db-internal:5432"}
	fault, ok := Classify(fmt.Errorf("saving: %w: %w", apperrors.ErrConflict, cause))
	if !ok {
		t.Fatal("a wrapped sentinel was not classified")
	}
	if strings.Contains(fault.Detail, "db-internal") || strings.Contains(fault.Detail, "23505") {
		t.Errorf("Detail = %q, want the sentinel's own words with no driver text", fault.Detail)
	}
	if fault.InfraCause == nil {
		t.Error("InfraCause is nil — the surface has nothing to log, so the cause is lost entirely")
	}
}

// Whether repeating a call could ever help is the one thing a caller most
// needs from a verdict, and it is read off the status so that no surface has
// to re-derive it. A rate limit clears on its own; a refusal does not.
func TestFault_transientMeansRepeatingTheCallCanHelp(t *testing.T) {
	cases := map[error]bool{
		apperrors.ErrBudgetExceeded:                      true,
		apperrors.ErrIncumbentBudgetExhausted:            true,
		apperrors.ErrConflict:                            false,
		apperrors.ErrConsentNotGranted:                   false,
		apperrors.ErrSeatTierInsufficient:                false,
		apperrors.ErrUnsupportedBySoR:                    false,
		&datasource.UnsupportedEntityError{Type: "deal"}: false,
	}
	for err, want := range cases {
		fault, ok := Classify(fmt.Errorf("call: %w", err))
		if !ok {
			t.Errorf("%v was not classified", err)
			continue
		}
		if got := fault.Transient(); got != want {
			t.Errorf("Classify(%v).Transient() = %v, want %v (status %d)", err, got, want, fault.Status)
		}
	}
}

// An error outside the taxonomy is the one case that must NOT be classified:
// it is a server fault, and reporting it as anything else hands a caller a
// verdict the system never actually reached.
func TestClassify_leavesUnknownErrorsToTheOpaque500(t *testing.T) {
	if fault, ok := Classify(errors.New("pgx: connection refused at 10.7.0.5:5432")); ok {
		t.Errorf("an unmapped error was classified as %+v, want the unhandled path", fault)
	}
}

// A constraint the database enforced and no path translated is the CALLER's
// mistake, not a server fault. It used to fall through to an opaque 500 whose
// advice was "Retry" — advice that can never work, since the same call violates
// the same constraint forever. A UAT agent burned its retries on exactly this
// and then escalated to a human for a fixable input error.
func TestClassify_anUntranslatedConstraintIsTheCallersMistakeNotAServerFault(t *testing.T) {
	for _, tc := range []struct {
		name, sqlstate, table, constraint, pgMessage, wantCode, wantDetail string
	}{
		{
			name:     "a foreign key names the column that pointed nowhere",
			sqlstate: "23503", table: "organization", constraint: "organization_owner_id_fkey",
			pgMessage: `insert or update on table "organization" violates foreign key constraint`,
			wantCode:  "reference_not_found",
			wantDetail: "`owner_id` names no record of the kind it references (an owner is a user, a parent " +
				"an organization). Send an id of the right kind; do not retry unchanged.",
		},
		{
			name:     "a CHECK refuses the value",
			sqlstate: "23514", table: "organization", constraint: "organization_size_band_check",
			pgMessage: `new row for relation "organization" violates check constraint`,
			wantCode:  "value_not_allowed",
			wantDetail: "a value in this request is outside what its field accepts. Check each value against " +
				"this operation's schema; do not retry unchanged.",
		},
		{
			name:     "an EXCLUDE refuses the overlap",
			sqlstate: "23P01", table: "calendar_block", constraint: "calendar_block_no_overlap",
			pgMessage: `conflicting key value violates exclusion constraint`,
			wantCode:  "value_not_allowed",
			wantDetail: "a value in this request is outside what its field accepts. Check each value against " +
				"this operation's schema; do not retry unchanged.",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := fmt.Errorf("writing the row: %w", &pgconn.PgError{
				Code: tc.sqlstate, TableName: tc.table, ConstraintName: tc.constraint, Message: tc.pgMessage,
			})
			fault, ok := Classify(err)
			if !ok {
				t.Fatal("the constraint reached the unhandled path, which answers 500 internal")
			}
			if fault.Status != http.StatusUnprocessableEntity {
				t.Errorf("status = %d, want 422 — a constraint breach is never a server fault", fault.Status)
			}
			if fault.Code != tc.wantCode {
				t.Errorf("code = %q, want %q", fault.Code, tc.wantCode)
			}
			// The EXACT sentence, not a substring: this is the whole of what the
			// caller reads, and a partial match passes on a message that also
			// carries something it should not.
			if fault.Detail != tc.wantDetail {
				t.Errorf("detail =\n  %q\nwant\n  %q", fault.Detail, tc.wantDetail)
			}
			// Each row's OWN metadata, so a leak of this SQLSTATE's constraint or
			// message cannot ride out under another row's assertions.
			//
			// The TABLE is not swept: `organization` is both a table name and an
			// ordinary word this refusal legitimately uses. The exact-detail
			// assertion above is the stronger guard anyway — it pins the whole
			// sentence, so anything riding along fails there first.
			for _, leak := range []string{tc.constraint, tc.sqlstate, tc.pgMessage} {
				if strings.Contains(fault.Detail, leak) {
					t.Errorf("detail leaks %q: %q", leak, fault.Detail)
				}
			}
			if fault.InfraCause == nil {
				t.Error("the constraint reaches no log — withholding a message is not the same as losing it")
			}
		})
	}
}

// The net sits UNDER the per-path validations: a module that names the field
// still wins, or the better message would be replaced by the generic one.
func TestClassify_aTypedRefusalStillWinsOverTheConstraintNet(t *testing.T) {
	err := fmt.Errorf("checking the band: %w",
		Validation("size_band", "invalid_enum", `"banana" is not a size band; expected one of: 1-10, 11-50`))
	fault, ok := Classify(err)
	if !ok {
		t.Fatal("a typed validation refusal was not classified")
	}
	if !strings.Contains(fault.Detail, "expected one of") {
		t.Errorf("the net replaced a refusal that named the field and its values: %q", fault.Detail)
	}
}

// The reference refusal names the FIELD whose id pointed nowhere. Its first
// version named none and told the caller to check their ids "against records
// this workspace actually has" — advice a UAT agent followed into a dead end,
// because the field it blamed references a user, which no tool on that surface
// lists, and a genuinely existing person id came back with byte-identical text.
func TestClassify_theReferenceRefusalNamesTheFieldWhenTheConstraintYieldsIt(t *testing.T) {
	for _, tc := range []struct {
		name, constraint, wantField string
		wantNamed                   bool
	}{
		{"a default-named foreign key yields its column", "organization_owner_id_fkey", "owner_id", true},
		{"a multi-word column survives whole", "organization_parent_org_id_fkey", "parent_org_id", true},
		{"a constraint that is not a foreign key names nothing", "organization_display_name_key", "", false},
		{"a hand-named constraint names nothing rather than guessing", "one_primary_domain", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := fmt.Errorf("writing: %w", &pgconn.PgError{Code: "23503", TableName: "organization", ConstraintName: tc.constraint})
			fault, ok := Classify(err)
			if !ok || fault.Status != http.StatusUnprocessableEntity {
				t.Fatalf("fault = %+v, ok = %v, want a 422", fault, ok)
			}
			if tc.wantNamed && !strings.Contains(fault.Detail, "`"+tc.wantField+"`") {
				t.Errorf("detail does not name %q: %q", tc.wantField, fault.Detail)
			}
			if !tc.wantNamed && strings.Contains(fault.Detail, "`") {
				t.Errorf("detail names a field it could not derive: %q", fault.Detail)
			}
			// Whichever branch ran, the schema stays behind and the advice never
			// sends the caller looking for a record kind this surface cannot list.
			if strings.Contains(fault.Detail, tc.constraint) {
				t.Errorf("detail leaks the constraint name: %q", fault.Detail)
			}
			if strings.Contains(fault.Detail, "records this workspace actually has") {
				t.Errorf("detail still gives the advice that could not be followed: %q", fault.Detail)
			}
		})
	}
}
