// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package capture

// The two agreements between the stage vocabulary and the database.
//
// Both guard a MEMBER-FACING claim, and both are two literals that can drift
// apart with nothing failing — which is the only reason they are tests rather
// than comments. A previous draft of this change asserted each in prose and
// neither was true.

import (
	"context"
	"os"
	"sort"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/pipelinetrace"
)

func ownerConn(t *testing.T) *pgx.Conn {
	t.Helper()
	dsn := os.Getenv("MARGINCE_TEST_DSN")
	if dsn == "" {
		t.Fatal("MARGINCE_TEST_DSN not set — run `make db-up` " +
			"(integration tests fail loudly, they never skip)")
	}
	conn, err := pgx.Connect(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := conn.Close(context.Background()); err != nil {
			t.Errorf("closing owner connection: %v", err)
		}
	})
	return conn
}

func TestTheStageCheckAdmitsExactlyTheStoredStages(t *testing.T) {
	// The registry says which stages capture writes; the column's CHECK says
	// which it will accept. A stage in one and not the other is either a write
	// that fails at runtime — and, on the capture transaction, fails the whole
	// capture — or a column admitting a value no writer produces.
	var def string
	err := ownerConn(t).QueryRow(context.Background(), `
		SELECT pg_get_constraintdef(oid) FROM pg_constraint
		 WHERE conrelid = 'capture_trace'::regclass
		   AND conname = 'capture_trace_stage_outcome_check'`).Scan(&def)
	if err != nil {
		t.Fatalf("reading the stage constraint: %v", err)
	}
	for _, stage := range pipelinetrace.StoredStages() {
		if !strings.Contains(def, string(stage)) {
			t.Errorf("the registry stores %q but the CHECK does not admit it:\n%s", stage, def)
		}
	}
	// And the other way: every stage the constraint names must be registered.
	for _, named := range stageLiteralsIn(def) {
		if !pipelinetrace.CanStore(pipelinetrace.Stage(named)) {
			t.Errorf("the CHECK admits stage %q, which the registry does not store", named)
		}
	}
}

func TestTheRetentionHoursAgreeWithTheSweep(t *testing.T) {
	// The vocabulary reports RetentionHours to a client, and the sweep deletes
	// on TraceWindowHours. If they disagree, the surface tells a member their
	// record is kept for longer than it is — and the rung that says "no longer
	// kept" fires at the wrong moment.
	if pipelinetrace.RetentionHours != TraceWindowHours {
		t.Errorf("pipelinetrace.RetentionHours = %d, capture.TraceWindowHours = %d — "+
			"the number a member is shown must be the number the sweep uses",
			pipelinetrace.RetentionHours, TraceWindowHours)
	}
}

// stageLiteralsIn pulls the quoted stage names out of a constraint definition.
// Postgres renders them as `stage = 'internal_drop'::text`, so the marker is the
// assignment rather than any quoted literal — the outcome values are quoted too.
func stageLiteralsIn(def string) []string {
	var out []string
	const marker = "stage = '"
	for i := 0; i < len(def); {
		at := strings.Index(def[i:], marker)
		if at < 0 {
			break
		}
		start := i + at + len(marker)
		end := start
		for end < len(def) && def[end] != '\'' {
			end++
		}
		out = append(out, def[start:end])
		i = end
	}
	sort.Strings(out)
	return out
}
