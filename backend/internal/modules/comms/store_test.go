// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package comms

// Hermetic unit lane: the pieces of Store that need no database. The
// real-Postgres behaviour (staging, attempt counting, ErrTerminal, the
// status transitions) is proven in store_integration_test.go, gated
// //go:build integration — a unit test in this file must never dial
// Postgres (scripts/check-test-lanes.sh enforces it).

import (
	"go/ast"
	"go/parser"
	"go/token"
	"maps"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// A nil clock is a caller forgetting to inject one, not a caller who wants
// no delivery timestamps. NewStore must still hand back a usable clock
// rather than a nil func that panics on first call.
func TestNewStoreDefaultsANilClockToTheRealOne(t *testing.T) {
	s := NewStore(nil, nil)
	before := time.Now()
	got := s.now()
	after := time.Now()
	if got.Before(before) || got.After(after) {
		t.Fatalf("now() = %s, want a value between %s and %s (the real clock)", got, before, after)
	}
}

// An injected clock is used as given, never silently replaced.
func TestNewStoreKeepsAnInjectedClock(t *testing.T) {
	fixed := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	s := NewStore(nil, func() time.Time { return fixed })
	if got := s.now(); !got.Equal(fixed) {
		t.Fatalf("now() = %s, want the injected %s", got, fixed)
	}
}

// The status vocabulary is a closed set pinned to the comms_outbound CHECK
// constraint (migrations/core/0136_comms_outbound.up.sql); a typo here would
// silently desync the Go constants from what the database actually accepts.
func TestStatusConstantsMatchTheSchemaCheck(t *testing.T) {
	want := map[string]string{"pending": StatusPending, "sent": StatusSent, "parked": StatusParked}
	for lit, constant := range want {
		if lit != constant {
			t.Errorf("status constant %q, want %q", constant, lit)
		}
	}
}

// The status vocabulary is CLOSED, and its closure is the invariant: there is
// no in-flight status, because a crash mid-send would strand a row in one and a
// guard keyed on it would turn River's redelivery into a silent skip — the very
// crash the connector's retransmission check exists for.
//
// The declared set is read out of this package's own source rather than listed
// here, so a FOURTH constant fails this test whatever it is named. That is the
// regression worth catching: an in-flight claim arrives as a new status, not as
// a rename of an old one. Widening the set is then a deliberate act — prove the
// new status cannot be reached by a crashed attempt, widen the comms_outbound
// CHECK constraint, and only then name it here.
func TestNoStatusIsAnInFlightClaim(t *testing.T) {
	declared := declaredStatusConstants(t)
	want := []string{"StatusParked", "StatusPending", "StatusSent"}
	if got := slices.Sorted(maps.Keys(declared)); !slices.Equal(got, want) {
		t.Fatalf("declared status constants = %v, want exactly %v — a status outside that set is a claim on a delivery in flight until proven otherwise", got, want)
	}
}

// declaredStatusConstants parses this package's own non-test sources and
// returns every exported Status* string constant it declares, keyed by name.
// Deriving the set is the whole point: a list maintained inside the test can
// only ever describe the statuses its author already knew about.
func declaredStatusConstants(t *testing.T) map[string]string {
	t.Helper()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("reading the comms package directory: %v", err)
	}
	fset := token.NewFileSet()
	declared := map[string]string{}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatalf("parsing %s: %v", name, err)
		}
		collectStatusConstants(t, file, declared)
	}
	if len(declared) == 0 {
		t.Fatal("no Status* constant found in the comms sources; the scan is broken, not the invariant")
	}
	return declared
}

// collectStatusConstants records every `Status… = "literal"` const spec in one
// parsed file. A Status* constant that is not a plain string literal is a
// failure rather than a skip: the scan must never silently stop seeing one.
func collectStatusConstants(t *testing.T, file *ast.File, into map[string]string) {
	t.Helper()
	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.CONST {
			continue
		}
		for _, spec := range gen.Specs {
			value, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			for i, ident := range value.Names {
				if !strings.HasPrefix(ident.Name, "Status") {
					continue
				}
				if i >= len(value.Values) {
					t.Fatalf("%s carries no value of its own; this scan reads explicit literals only", ident.Name)
				}
				lit, ok := value.Values[i].(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					t.Fatalf("%s is not a string literal; this scan reads explicit literals only", ident.Name)
				}
				unquoted, err := strconv.Unquote(lit.Value)
				if err != nil {
					t.Fatalf("unquoting %s = %s: %v", ident.Name, lit.Value, err)
				}
				into[ident.Name] = unquoted
			}
		}
	}
}
