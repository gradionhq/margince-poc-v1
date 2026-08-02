// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package backendarch

// Job args carry REFERENCES, never content. The erasure engine neutralizes
// an in-flight job by scrubbing the row the job names — comms_outbound goes
// to `parked` and the waking job finds nothing to send. That only works
// while the job holds an id and not a copy: args carrying a body or an
// address would be a second store of subject data that Art. 17 never
// reaches, sitting in a table with no workspace column and no RLS.
//
// This gate is a HABIT GUARD, not a proof of that property. It matches field
// NAMES against a word list, so it catches the shapes someone reaches for
// without thinking (Body, RecipientEmail, Subject) and would miss a field
// named Snippet, Note or Domain carrying the same thing. A positive assertion
// — every args field is an id, or an explicitly waived scalar — would be the
// proof; this is the cheap version that stops the common case.

import (
	"go/ast"
	"path/filepath"
	"strings"
	"testing"
)

// contentWords name a payload rather than a pointer to one. Matched as a
// case-insensitive substring of the field name, so `RecipientEmail` and
// `Body` both trip.
var contentWords = []string{
	"address", "body", "content", "email", "message",
	"name", "payload", "phone", "subject", "text",
}

// contentFieldWaivers are ratified exceptions, keyed "Type.Field". EMPTY is
// the expected steady state — a job names a row and the worker reads it, so a
// real exception should be rare enough to argue about. The validation below
// exists so that when one does appear it cannot arrive without a rationale, or
// outlive the field it was written for.
var contentFieldWaivers = map[string]string{}

func TestJobArgsCarryReferencesNotContent(t *testing.T) {
	dir := filepath.Join("internal", "compose")
	byType := methodsByType(t, dir)
	fields := structFields(t, dir)
	used := map[string]bool{}
	checked := 0

	for typeName, methods := range byType {
		if !methods["Kind"] {
			continue
		}
		checked++
		for _, field := range fields[typeName] {
			key := typeName + "." + field
			lower := strings.ToLower(field)
			for _, word := range contentWords {
				if !strings.Contains(lower, word) {
					continue
				}
				if _, waived := contentFieldWaivers[key]; waived {
					used[key] = true
					break
				}
				t.Errorf("%s looks like content, not a reference (matched %q). A job names a row; the worker reads it. If this really is a reference, waive it in contentFieldWaivers with a rationale.", key, word)
				break
			}
		}
	}
	if checked < jobArgsFloor {
		t.Fatalf("found only %d job args types, expected at least %d — the walker matched nothing", checked, jobArgsFloor)
	}
	for key, reason := range contentFieldWaivers {
		if reason == "" {
			t.Errorf("%s: waiver without a rationale", key)
		}
		if !used[key] {
			t.Errorf("%s: stale waiver — no such field trips the gate any more; delete it", key)
		}
	}
}

// structFields returns the declared field names of every struct type in dir.
func structFields(t *testing.T, dir string) map[string][]string {
	t.Helper()
	_, files := parseGoFilesUnder(t, dir)
	byType := map[string][]string{}
	for _, file := range files {
		ast.Inspect(file, func(n ast.Node) bool {
			spec, ok := n.(*ast.TypeSpec)
			if !ok {
				return true
			}
			st, ok := spec.Type.(*ast.StructType)
			if !ok || st.Fields == nil {
				return true
			}
			for _, f := range st.Fields.List {
				for _, name := range f.Names {
					byType[spec.Name.Name] = append(byType[spec.Name.Name], name.Name)
				}
			}
			return true
		})
	}
	return byType
}
