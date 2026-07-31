// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package backendarch

// PII reach as a fitness function. tableownership_test.go proves a package
// only writes tables it owns; it says NOTHING about whether Art. 17 erasure
// reaches every table that holds a data subject. Without that guarantee the
// activity timeline and attachments survive an erasure verbatim, still
// full-text searchable. This test closes it: piiTables is the explicit
// registry of PII-bearing tables, and every entry must be a WRITE target of
// privacy/erasure.go (so erasure reaches it) and — unless it is an opaque
// derived artifact — a READ target of privacy/sar.go (so an Art. 15 SAR
// discloses it). A new PII table that skips erasure or SAR fails here instead
// of shipping a silent leak.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// piiHandling declares how erasure and SAR must reach a PII table.
type piiHandling struct {
	// erasureWrite: erasure.go must UPDATE/DELETE this table (redact or purge).
	erasureWrite bool
	// retentionErase: the nightly retention sweep is this table's ONLY
	// eraser. True only where the row carries no linkage back to a subject
	// for the Art. 17 cascade to walk — the cascade starts at a person, so a
	// table it structurally cannot reach must still be erased by SOMETHING,
	// and the sweep is that something. It is a separate field rather than a
	// second way to satisfy erasureWrite because the two are different
	// promises: "erased when the subject asks" and "erased when the clock
	// says", and only the first answers an Art. 17 request.
	retentionErase bool
	// retentionErasures names the SET assignments that CONSTITUTE the sweep's
	// erasure of this table, normalized to single spaces. Required wherever
	// retentionErase is set: "the sweep writes this table" is satisfied by any
	// statement at all, so a version bump or a metadata touch left behind after
	// the plaintext wipe was deleted would keep that claim true while the
	// content survived its window. These are the assignments the erasure IS.
	retentionErasures []string
	// sarRead: SAR assembly must read this table into the export package.
	// False only for opaque derived artifacts (vectors) that carry no
	// human-readable PII to hand back — they are purged, never exported.
	sarRead bool
	// sarForbidden: SAR assembly must NOT read this table. Its own promise,
	// not the absence of sarRead's: leaving a table merely unregistered for
	// reads means a future AssembleSAR query silently passes this gate, which
	// is the difference between an invariant in a comment and a control. Set
	// it wherever the row holds something an Art. 15 package must not carry —
	// a live credential, say — and the gate fails the moment SAR touches it.
	sarForbidden bool
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
	// The channel identity binds a human to their Telegram account: the
	// provider's user id for them plus the @username they message under. Both
	// identify the subject as directly as an address does, and the id is the
	// key a re-capture would resurrect them by — so erasure purges it, the
	// suppression list keeps holding it, and Art. 15 hands it back.
	"person_channel_identity": {erasureWrite: true, sarRead: true},
	"lead":                    {erasureWrite: true, sarRead: true},
	// Who was IN each interaction (ACT-DDL-3). It names the subject twice —
	// by person_id and by the raw address of a party who never became a
	// record — so erasure nulls both and Art. 15 hands back the fact that
	// they were a party to those conversations.
	"activity_participant": {erasureWrite: true, sarRead: true},
	// The interaction projection (CG-DDL-1): derived, but derived from data an
	// erasure removes, and it holds who corresponded with the subject and how
	// often. Purged, never exported — like the embedding, it is a machine
	// artifact rather than anything the subject supplied.
	"graph_interaction_edge": {erasureWrite: true, sarRead: false},
	"activity":               {erasureWrite: true, sarRead: true},
	"attachment":             {erasureWrite: true, sarRead: true},
	"raw_capture":            {erasureWrite: true, sarRead: true},
	"embedding":              {erasureWrite: true, sarRead: false}, // opaque vector: purged, never exported
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
	// The voice learning signal keeps the model's drafted text
	// (generated_original) in plaintext, which is correspondence about a
	// subject. It names no person, activity or subject, deliberately: the row
	// exists to say whether the owner sent the machine's words or reworded
	// them, and linking it to the recipient would put a second copy of their
	// mail behind a join Art. 17 would have to find. So the time-based sweep
	// (privacy/retention.go, 180 days) is its eraser, not the cascade.
	//
	// The SAR exclusion holds for a draft that was SENT: the subject's copy of
	// that correspondence is the activity, which AssembleSAR already exports,
	// and the sent body itself is classified and discarded, never stored. It
	// does NOT hold for a draft that was rejected or abandoned — no activity
	// exists, so generated_original is the only copy of words written about the
	// subject, and it is held for up to 180 days with no Art. 15 export path.
	// That bound is a property of storing the drafted text at all
	// (RecordDraftedSignal), not of any one outcome recorded against it, and
	// whether Art. 15 must reach an unsent draft is open against the spec.
	"voice_learning_signal": {
		retentionErase: true,
		retentionErasures: []string{
			"generated_original = NULL",
			"final_text = NULL",
			"content_erased_at = now()",
		},
		sarRead: false,
	},
	// The preference-center token (0048) is a live capability over the
	// subject's consent record — held by whoever has the emailed
	// List-Unsubscribe URL, honoured with no session at all. Registered so
	// this gate proves erasure retires it: the person row survives
	// anonymize-in-place, so the schema's ON DELETE CASCADE never fires.
	// The export side is sarFORBIDDEN, not merely not-read, and for the
	// opposite reason to embedding's: not "nothing human-readable to hand
	// back" but "a working credential", which an Art. 15 package assembled by
	// an admin must not carry into an export file — the subject already holds
	// their own copy, in the mail that delivered it. Declared so a future SAR
	// section over this table fails the gate instead of shipping.
	"preference_token": {erasureWrite: true, sarForbidden: true},
}

// fromJoinRe extracts the table named by a FROM/JOIN clause — SAR reads are
// SELECTs, invisible to sqlWriteTargets.
var fromJoinRe = regexp.MustCompile(`(?is)\b(?:from|join)\s+([a-z_][a-z0-9_]*)`)

// sqlWhitespaceRe collapses the indentation of a raw-string SQL literal so an
// assignment can be matched as the one-line clause it reads as in the registry.
var sqlWhitespaceRe = regexp.MustCompile(`\s+`)

func collapsedSQL(literal string) string {
	return strings.ToLower(strings.TrimSpace(sqlWhitespaceRe.ReplaceAllString(literal, " ")))
}

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
	// The subject's TIMELINE and everything derived from it — split out of
	// erasure.go when that file crossed the size cap. It is the same Art. 17
	// transaction, so it counts here; leaving it off would let a table look
	// uncovered the moment its purge moved file.
	"internal/modules/privacy/erasuretimeline.go",
	"internal/modules/privacy/erasure_attachments.go",
	"internal/modules/privacy/erasure_channels.go",
	"internal/modules/privacy/erasure_rivals.go",
	"internal/modules/privacy/deliveries.go",
}

// retentionSweepFile is the nightly time-based evaluator — the only eraser a
// subject-unlinked PII table has. Kept apart from the cascade above so a
// retention sweep can never be mistaken for an answer to an Art. 17 request.
const retentionSweepFile = "internal/modules/privacy/retention.go"

func TestErasureAndSARReachEveryPIITable(t *testing.T) {
	writes := map[string]bool{}
	for _, path := range erasureCascadeFiles {
		for _, lit := range sqlLiterals(t, path) {
			for _, table := range sqlWriteTargets(lit) {
				writes[table] = true
			}
		}
	}
	// Each swept table keeps the text of the sweep statements that write it, so
	// the declared erasure assignments are checked against the statement that
	// erases THIS table rather than against retention.go as a whole.
	sweeps := map[string]string{}
	for _, lit := range sqlLiterals(t, retentionSweepFile) {
		for _, table := range sqlWriteTargets(lit) {
			sweeps[table] += " " + collapsedSQL(lit)
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
		if !h.erasureWrite && !h.retentionErase {
			missing = append(missing, "PII table "+table+
				" is registered with no eraser at all — declare erasureWrite (the Art. 17 cascade) or retentionErase (the time-based sweep)")
		}
		if h.erasureWrite && !writes[table] {
			missing = append(missing, "erasure never writes PII table "+table+
				" — Art. 17 leaves it intact; redact/purge it in ErasePerson")
		}
		if h.retentionErase && sweeps[table] == "" {
			missing = append(missing, "the retention sweep never writes PII table "+table+
				" — its only eraser is gone; erase it in the nightly evaluator or move it onto the Art. 17 cascade")
		}
		if h.retentionErase && len(h.retentionErasures) == 0 {
			missing = append(missing, "PII table "+table+
				" names the retention sweep as its eraser but declares no erasure assignments — list the SET clauses that ARE the wipe, so a metadata-only write cannot satisfy this gate")
		}
		for _, assignment := range h.retentionErasures {
			if !strings.Contains(sweeps[table], strings.ToLower(assignment)) {
				missing = append(missing, "the retention sweep no longer assigns `"+assignment+"` on PII table "+table+
					" — the content it was written to erase now outlives its window; restore the assignment or amend the declared erasure")
			}
		}
		if h.sarRead && !reads[table] {
			missing = append(missing, "SAR never reads PII table "+table+
				" — Art. 15 export is incomplete; add a section in AssembleSAR")
		}
		if h.sarForbidden && reads[table] {
			missing = append(missing, "SAR reads PII table "+table+
				" — it is registered sarForbidden because its rows must never leave in an Art. 15 package; drop the section in AssembleSAR")
		}
		if h.sarRead && h.sarForbidden {
			missing = append(missing, "PII table "+table+
				" is registered both sarRead and sarForbidden — the export cannot both require and refuse it")
		}
	}
	sort.Strings(missing)
	for _, m := range missing {
		t.Error(m)
	}
}
