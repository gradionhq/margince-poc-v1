// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package backendarch

// A module that owns tables writes their history.
//
// TestEveryAuditedMutationEmitsAnEvent holds half of the write-shape
// obligation — audit implies event — and it only starts counting once it has
// seen a call named Audit. A module that calls NEITHER has no audit row to pair,
// so it is not merely unwaived: it is outside that gate's universe entirely. It
// could own five tables, write every one of them, and record nothing, and both
// halves of the write shape would report green over it.
//
// That is not a hypothetical. It was true of the finance mirror until #1795: six
// write statements on four tables, no audit_log row anywhere, and a comment in
// the module saying the write shape was "the house one".
//
// The rule, stated rather than cited: every mutation commits its domain row and
// its audit_log row in ONE transaction. An audit row cannot be written after the
// fact, so a module that never writes one has no history that can be recovered —
// and the erasure and retention reasoning that reads audit_log is blind to
// everything it owns.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"maps"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/gatekit"
)

// modulesThatWriteNoHistory are the table-owning modules that call no audit
// writer at all, each with the reason that is correct for it.
//
// An entry here is a much stronger claim than one in auditOnlyWrites. That set
// says "this mutation is recorded but not announced"; this one says "this
// module's writes are not recorded at all", so the rationale has to explain
// where the history actually lives.
var modulesThatWriteNoHistory = gatekit.Waive(map[string]string{
	// The audit writer itself. It owns audit_log, event_outbox, system_log and
	// field_provenance, and auditing its own writes is circular — the audit row
	// would need an audit row. Nothing above it is exempt: every caller of
	// storekit.Audit is judged by this gate under its own module.
	"internal/platform/database/storekit": "storekit IS the audit writer; the four tables it owns are the ledgers themselves",

	// Per-READER derived state. Every one of these tables carries a user_id and
	// holds an assembly generated FOR one person — a brief, a dossier, a view
	// cursor, a dismissal. None is a shared record fact, so none has a record
	// history to write: regenerating one for a different reader produces
	// different rows legitimately, and an audit trail over that would record
	// reading rather than changing. Verified against the DDL, not the package
	// name: user_record_view, suggestion_dismissal, person_moment_dismissal,
	// org_brief, person_brief, org_dossier and org_growth_fit all key on user_id.
	"internal/compose/org360":      "user_record_view and suggestion_dismissal are per-reader: one person's last-seen cursor and one person's dismissals",
	"internal/compose/person360":   "person_moment_dismissal, the same per-reader shape",
	"internal/compose/orgbrief":    "org_brief is an assembly generated for one reader and never served to another",
	"internal/compose/personbrief": "person_brief, the same",
	"internal/compose/orgdossier":  "org_dossier and org_growth_fit, the same — the DDL says outright that an assembly generated for one reader is never served to another, and growth fit folds seat-dependent context on top",

	// Operational state whose DOMAIN writes are audited elsewhere.
	"internal/modules/agents": "agent_run and runner_job are runner lifecycle bookkeeping. The domain writes a run performs happen inside the TOOLS it calls, and those carry the full audit and outbox shape — auditing the run row as well would file the same change twice under two entity types",
	"internal/modules/comms":  "comms_outbound is delivery machinery, not the message. The user-visible fact of an outbound email is the ACTIVITY row, which activities owns and audits; StageTx runs inside that same transaction, so the send already has its history. comms does write a ledger row for the one thing activities cannot describe — a reconcile failure — through storekit.LogSystem, which is system_log and deliberately not counted here",

	// Rebuildable projections, and one table that could not carry an audit row.
	"internal/modules/search": "graph_interaction_edge and embedding are PROJECTIONS folded from rows the owning modules already audited; each holds no fact of its own and is thrown away and rebuilt as the corruption remedy, so an audit trail over them would record a recomputation rather than a change. embed_store_binding is stronger than a judgement call: it is a SINGLETON with no workspace_id column at all, and storekit.Audit derives its workspace from the GUC, so it could not write a valid row for that table if it tried",

	// Extension-tier secrets, audited into the OTHER ledger on purpose.
	"internal/platform/extsecrets": "extension_secret is written with storekit.LogSystem rather than storekit.Audit, and the package says why in-source: a secret changing hands moves no domain row, so there is no audit_log entry to attach it to. It belongs in system_log, the non-entity operational ledger, which is the same posture the boot's extension inventory takes. This gate deliberately does not count LogSystem, so the module appears here — it is recorded, in the ledger that fits it",

	// NOT a waiver of the obligation — a different defect, filed.
	"internal/modules/approvals": "approvals DOES write audit_log, and this entry exists because it writes it by HAND: `INSERT INTO audit_log …` at service.go:218, bypassing storekit.Audit entirely, with the same shape hand-rolled for event_outbox at :268. So the module has history and this gate cannot see it. That hand-rolled writer omits before, after and authorization_rule, and its envelope drops CausationID — filed as #1946, including that both waivers ratifying the writer state a reason the code contradicts. When approvals routes through storekit, this entry goes",
})

// auditWriters are the storekit calls that put a row in audit_log.
//
// storekit.LogSystem is deliberately NOT one of them, matching the exclusion
// TestEveryAuditedMutationEmitsAnEvent already makes. It writes system_log,
// which is the ledger for an operational event that mutates no record — a
// login, a bulk export. The obligation this gate holds is about RECORD history,
// and a module whose tables' changes appear only in system_log has none. The two
// gates have to agree about what "audits" means, or the same code satisfies one
// and not the other.
var auditWriters = map[string]bool{"Audit": true, "AuditWithEvidence": true}

// modulesOwningTables inverts tableOwners into the set of modules that own at
// least one table.
//
// tableOwners and NOT each module's doc.go, which is what #1802 proposed on the
// belief that a gate already reads those. Nothing parses doc.go — tableOwners is
// the hand-maintained map, and its neighbours' "kept in sync" is prose. A
// doc.go-derived census would exempt seven modules outright: three have no
// doc.go at all, two have one with no "Tables owned" line, and two declare
// "Tables owned: none" while tableOwners assigns them tables.
func modulesOwningTables() []string {
	owners := map[string]bool{}
	for _, module := range tableOwners {
		owners[module] = true
	}
	return slices.Sorted(maps.Keys(owners))
}

// moduleWritesAuditRow reports whether any non-test file under a module calls an
// audit writer.
func moduleWritesAuditRow(t *testing.T, module string) bool {
	t.Helper()
	found := false
	fset := token.NewFileSet()
	err := filepath.WalkDir(module, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") ||
			strings.HasSuffix(path, "_test.go") || isIntegrationTagged(path) {
			return err
		}
		file, err := parser.ParseFile(fset, filepath.ToSlash(path), nil, 0)
		if err != nil {
			return err
		}
		ast.Inspect(file, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			if pkg, ok := sel.X.(*ast.Ident); ok && pkg.Name == "storekit" && auditWriters[sel.Sel.Name] {
				found = true
			}
			return !found
		})
		return nil
	})
	if err != nil {
		t.Fatalf("walking %s for its audit writers: %v", module, err)
	}
	return found
}

func TestEveryTableOwningModuleWritesAnAuditRow(t *testing.T) {
	modules := modulesOwningTables()
	// A census that examined nothing reports exactly like a tree where every
	// module audits, which is the shape of the hole this gate closes.
	if len(modules) == 0 {
		t.Fatal("tableOwners named no owning modules; this gate would pass vacuously")
	}
	// Armed only once the census is known non-empty. Deferred above the fatal,
	// the sweep runs on the way out of it and buries the one true line under an
	// entry per waiver, each telling the reader to delete a correct one.
	defer modulesThatWriteNoHistory.AssertAllMatched(t)
	t.Logf("examined %d modules that own at least one table", len(modules))

	for _, module := range modules {
		if moduleWritesAuditRow(t, module) {
			continue
		}
		if modulesThatWriteNoHistory.Waived(t, module) {
			continue
		}
		t.Errorf("%s owns tables and calls no audit writer anywhere — every mutation commits its "+
			"domain row and its audit_log row in one transaction, and an audit row cannot be "+
			"written after the fact. Wire storekit.Audit into its writes, or ratify the module in "+
			"modulesThatWriteNoHistory with a rationale saying where its history lives instead",
			module)
	}
}
