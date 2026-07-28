// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package backendarch

// PII reach as a fitness function. tableownership_test.go proves a package
// only writes tables it owns; it says NOTHING about whether Art. 17 erasure
// reaches every table that holds a data subject. Without that guarantee the
// activity timeline and attachments survive an erasure verbatim, still
// full-text searchable. This test closes it: piiTables is the explicit registry of PII-bearing
// tables, and every entry must be a WRITE target of privacy/erasure.go (so
// erasure reaches it) and — unless it is an opaque derived artifact — a READ
// target of privacy/sar.go (so an Art. 15 SAR discloses it). A new PII table
// that skips erasure or SAR fails here instead of shipping a silent leak.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"regexp"
	"sort"
	"strconv"
	"testing"
)

// piiHandling declares how erasure and SAR must reach a PII table.
type piiHandling struct {
	// erasureWrite: erasure.go must UPDATE/DELETE this table (redact or purge).
	erasureWrite bool
	// sarRead: SAR assembly must read this table into the export package.
	// False only for opaque derived artifacts (vectors) that carry no
	// human-readable PII to hand back — they are purged, never exported.
	sarRead bool
}

// piiTables is the registry of every table holding data about a subject.
// "Holds a subject's PII" is a domain judgment, not a schema property —
// attachment/raw_capture/embedding carry it with no person FK, while
// person-referencing tables like relationship and the consent proof logs
// deliberately do not qualify (kept under Art. 5 accountability). So, like
// tableOwners in the ownership gate, this map IS the hand-maintained
// artifact: a table is registered here as the one act that declares it
// PII-bearing, and the test then proves erasure and SAR reach it. Keep it
// in step with the subject data in data-model §3.
var piiTables = map[string]piiHandling{
	"person":        {erasureWrite: true, sarRead: true},
	"person_email":  {erasureWrite: true, sarRead: true},
	"person_social": {erasureWrite: true, sarRead: true},
	"person_phone":  {erasureWrite: true, sarRead: true},
	"lead":          {erasureWrite: true, sarRead: true},
	"activity":      {erasureWrite: true, sarRead: true},
	"attachment":    {erasureWrite: true, sarRead: true},
	"raw_capture":   {erasureWrite: true, sarRead: true},
	"embedding":     {erasureWrite: true, sarRead: false}, // opaque vector: purged, never exported
	// Field-level provenance names who captured which of the subject's
	// fields from where — subject-linked metadata (B-E02.12).
	"field_provenance": {erasureWrite: true, sarRead: true},
	// The capture disposition ledger keys on the subject's own address and
	// keeps the display name their mail arrived with (CAP-DDL-8).
	"capture_pending_counterparty": {erasureWrite: true, sarRead: true},
	// The send log keeps a second copy of an outbound message's recipient
	// addresses, subject line and body, scrubbed with the activity it
	// transmitted and exported alongside it.
	"comms_outbound": {erasureWrite: true, sarRead: true},
}

// fromJoinRe extracts the table named by a FROM/JOIN clause — SAR reads are
// SELECTs, invisible to sqlWriteTargets.
var fromJoinRe = regexp.MustCompile(`(?is)\b(?:from|join)\s+([a-z_][a-z0-9_]*)`)

// sqlLiterals returns every Go string literal in one source file. Both the
// write-target and read-target scans run over these.
func sqlLiterals(t *testing.T, path string) []string {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	var out []string
	ast.Inspect(file, func(n ast.Node) bool {
		lit, ok := n.(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return true
		}
		if s, err := strconv.Unquote(lit.Value); err == nil {
			out = append(out, s)
		}
		return true
	})
	return out
}

// erasureCascadeFiles are the sources that make up the Art. 17 cascade — the
// files ErasePerson's own transaction executes SQL from. It is a LIST because
// the cascade spans more than one file, and a gate pinned to a single path
// silently stops covering a table the moment its scrub is extracted to a
// neighbour. It is deliberately NOT the whole privacy
// package: retention.go also writes subject tables, and letting a retention
// sweep satisfy "Art. 17 reaches this table" is exactly the confusion this test
// exists to prevent.
var erasureCascadeFiles = []string{
	"internal/modules/privacy/erasure.go",
	"internal/modules/privacy/deliveries.go",
}

func TestErasureAndSARReachEveryPIITable(t *testing.T) {
	writes := map[string]bool{}
	for _, path := range erasureCascadeFiles {
		for _, lit := range sqlLiterals(t, path) {
			for _, table := range sqlWriteTargets(lit) {
				writes[table] = true
			}
		}
	}
	reads := map[string]bool{}
	for _, lit := range sqlLiterals(t, "internal/modules/privacy/sar.go") {
		for _, m := range fromJoinRe.FindAllStringSubmatch(lit, -1) {
			reads[m[1]] = true
		}
	}

	var missing []string
	for table, h := range piiTables {
		if h.erasureWrite && !writes[table] {
			missing = append(missing, "erasure never writes PII table "+table+
				" — Art. 17 leaves it intact; redact/purge it in ErasePerson")
		}
		if h.sarRead && !reads[table] {
			missing = append(missing, "SAR never reads PII table "+table+
				" — Art. 15 export is incomplete; add a section in AssembleSAR")
		}
	}
	sort.Strings(missing)
	for _, m := range missing {
		t.Error(m)
	}
}
