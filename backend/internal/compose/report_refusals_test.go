// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// How the report engine refuses a plan it cannot run, and what shape an empty
// result arrives in. Both were reported as MCP defects: `run_report`
// internal-errored on exactly the vocabulary mistakes its own description
// promises to police, and answered `"rows": null` where every other list-shaped
// tool answers `[]`.
//
// No database: the vocabulary check and the row normalization both happen before
// and around the query, which is what makes them testable here at all.

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/gradionhq/margince/backend/internal/platform/httperr"
)

func TestAnUnknownReportFieldIsAClassifiedCallerFault(t *testing.T) {
	// The verdict used to live only in writeReportError, an HTTP-only helper. The
	// MCP surface reaches this engine through run_report and never runs that
	// helper, so an unknown filters key or group_by came back as "the tool failed
	// for an internal reason; retry" — after the engine had already settled it.
	fault, ok := httperr.Classify(&FieldNotAllowedError{Field: "nonsense"})
	if !ok {
		t.Fatal("FieldNotAllowedError is classified by nothing, so the tool surface reports it as an " +
			"internal fault and tells the agent to retry a call that can never succeed")
	}
	if fault.Transient() {
		t.Error("a vocabulary mistake is reported as transient; waiting cannot make `nonsense` a valid field")
	}
	if fault.Code != "report_field_not_allowed" {
		t.Errorf("code = %q, want the code writeReportError already puts on the REST wire", fault.Code)
	}
	// The rejected token has to survive into the message: it is what locates the
	// mistake among three arguments that could have carried it.
	if !strings.Contains(fault.Detail, "nonsense") {
		t.Errorf("the refusal does not quote the rejected name: %q", fault.Detail)
	}
}

func TestAnUnknownReportFieldIsRefusedThroughEveryVocabularyDoor(t *testing.T) {
	// Derived rather than spot-checked: each of the three plan arguments is its
	// own door into the vocabulary, and one of them being classified says nothing
	// about the others.
	spec := reportSpec{
		dimensions: map[string]string{"stage": "t.stage_id"},
		measures:   map[string]string{"amount": "t.amount_minor"},
	}
	for _, tc := range []struct {
		name string
		call func() error
	}{
		{"group_by", func() error {
			_, _, err := buildSelectList(spec, []string{"nonsense"}, nil)
			return err
		}},
		{"aggregates[].field", func() error {
			_, _, err := aggregateSelect(spec, reportAggregate{Fn: "sum", Field: "nonsense"})
			return err
		}},
		{"aggregates[].fn", func() error {
			_, _, err := aggregateSelect(spec, reportAggregate{Fn: "nonsense"})
			return err
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.call()
			if err == nil {
				t.Fatal("an out-of-vocabulary name was accepted")
			}
			fault, ok := httperr.Classify(err)
			if !ok {
				t.Fatalf("%v is classified by nothing", err)
			}
			if fault.Transient() {
				t.Errorf("%v is reported as transient", err)
			}
		})
	}
}

func TestAReportWithNoRowsAnswersAnEmptyArray(t *testing.T) {
	// reportOutcome.Rows is what the MCP runner marshals straight through, so
	// "matched nothing" has to already be an array by the time it gets there. The
	// REST handler was papering over the nil by copying into a sized slice before
	// adding derivation handles; the runner had no such step and emitted null.
	outcome := reportOutcome{Rows: scanned(t)}
	encoded, err := json.Marshal(map[string]any{"rows": outcome.Rows})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if got := string(encoded); got != `{"rows":[]}` {
		t.Errorf("an empty report marshals to %s, want {\"rows\":[]} — a model reads null as "+
			"\"unknown\" where an empty array says \"none matched\"", got)
	}
}

// scanned returns what scanReportRows produces for a result set with no rows.
// The normalization under test is the slice's initial value, so an empty result
// is exactly the case that exercises it.
func scanned(t *testing.T) []map[string]any {
	t.Helper()
	rows, err := scanReportRows(emptyRows{}, []string{"stage"})
	if err != nil {
		t.Fatalf("scanReportRows over an empty result: %v", err)
	}
	return rows
}

// emptyRows is a pgx.Rows that yielded nothing and failed at nothing — a query
// whose predicate matched no row. Only Next and Err are reached; the rest of the
// interface panics rather than returning a plausible zero, so a future scan step
// that starts calling them fails loudly here instead of silently agreeing with
// whatever this stub made up.
type emptyRows struct{}

func (emptyRows) Next() bool { return false }
func (emptyRows) Err() error { return nil }
func (emptyRows) Close()     {}
func (emptyRows) Conn() *pgx.Conn {
	panic("emptyRows: Conn is not part of the row scan")
}

func (emptyRows) CommandTag() pgconn.CommandTag {
	panic("emptyRows: CommandTag is not part of the row scan")
}

func (emptyRows) FieldDescriptions() []pgconn.FieldDescription {
	panic("emptyRows: FieldDescriptions is not part of the row scan")
}

func (emptyRows) Scan(...any) error {
	panic("emptyRows: Scan is unreachable — Next reported no rows")
}

func (emptyRows) Values() ([]any, error) {
	panic("emptyRows: Values is unreachable — Next reported no rows")
}

func (emptyRows) RawValues() [][]byte {
	panic("emptyRows: RawValues is not part of the row scan")
}

var _ pgx.Rows = emptyRows{}
