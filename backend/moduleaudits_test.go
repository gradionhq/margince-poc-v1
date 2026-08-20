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
	"internal/modules/search": "graph_interaction_edge and embedding are PROJECTIONS folded from rows the owning modules already audited; each holds no fact of its own and is thrown away and rebuilt as the corruption remedy, so an audit trail over them would record a recomputation rather than a change. embed_store_binding is stronger than a judgement call, though not for the reason it first looks: the audit row takes its workspace from the GUC rather than from the audited table, so a table lacking the column would not by itself stop one. What stops it is that binding.go writes on the BARE POOL, deliberately outside any per-workspace transaction — it is deployment metadata, marked rls-exempt in-source — so the GUC is unset and audit_log`s NOT NULL workspace_id would take a NULL",

	// Extension-tier secrets, audited into the OTHER ledger on purpose.
	"internal/platform/extsecrets": "extension_secret is written with storekit.LogSystem rather than storekit.Audit, and the package says why in-source: a secret changing hands moves no domain row, so there is no audit_log entry to attach it to. It belongs in system_log, the non-entity operational ledger, which is the same posture the boot's extension inventory takes. This gate deliberately does not count LogSystem, so the module appears here — it is recorded, in the ledger that fits it",

	// NOT a waiver of the obligation — a different defect, filed.
	"internal/modules/approvals": "TRUE OF ONE OF ITS TWO TABLES, and the entry says so rather than rounding up. `approval` has history: approvals writes audit_log by HAND at service.go:218, bypassing storekit.Audit, so this gate cannot see it — filed as #1946 with what that writer omits. `signing_key` has NONE: the INSERT at token_jws.go:172 mints an Ed25519 private key with no audit row, no hand-rolled row and no system_log row, and the hand-rolled writer could not describe it anyway because it hardcodes entity_type to the literal 'approval'. That is a real gap this waiver does not excuse; it is recorded here so the next reader finds it instead of trusting the module-granular verdict. Which brings out this gate's own limit: it is module-granular, so `owns five tables, audits one` passes it, and approvals is the live instance",
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
func moduleWritesAuditRow(module string, subjects map[string]bool) (bool, error) {
	found := false
	fset := token.NewFileSet()
	err := filepath.WalkDir(module, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		// A subdirectory that is ITSELF a census subject answers for itself.
		// Six of them sit under internal/compose, and without this the parent
		// borrows their audit calls: compose could own seven tables, audit none
		// of them, and still read green because compose/briefs audits.
		if d.IsDir() {
			if path != module && subjects[filepath.ToSlash(path)] {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") ||
			strings.HasSuffix(path, "_test.go") || isIntegrationTagged(path) {
			return nil
		}
		file, err := parser.ParseFile(fset, filepath.ToSlash(path), nil, 0)
		if err != nil {
			return err
		}
		qualifier, imports := storekitQualifier(file)
		if !imports {
			return nil
		}
		ast.Inspect(file, func(n ast.Node) bool {
			// A CALL, not a mention. Matching the selector alone would let
			// `var auditWriter = storekit.Audit` — a reference that never runs —
			// answer for a module that audits nothing, which is the false green
			// this gate exists to refuse. And the qualifier has to resolve to the
			// IMPORTED package: a local value named storekit with an Audit method
			// would otherwise do the same.
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			if pkg, ok := sel.X.(*ast.Ident); ok && pkg.Name == qualifier && auditWriters[sel.Sel.Name] {
				found = true
			}
			return !found
		})
		return nil
	})
	return found, err
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

	subjects := make(map[string]bool, len(modules))
	for _, module := range modules {
		subjects[module] = true
	}
	for _, module := range modules {
		audits, err := moduleWritesAuditRow(module, subjects)
		if err != nil {
			// Errorf and not Fatalf: FailNow runs the deferred sweep, which then
			// reports every waiver it has not reached yet as stale and tells the
			// reader to delete ten correct ones. A module renamed without
			// updating tableOwners is exactly how this arrives.
			t.Errorf("walking %s for its audit writers: %v — tableOwners names a module path that "+
				"does not resolve, so this gate cannot judge it", module, err)
			continue
		}
		if audits {
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

// storekitQualifier answers the name storekit is reachable under in one file,
// and whether the file imports it at all. A file that does not import it can
// hold no audit call, whatever it happens to name a local variable.
func storekitQualifier(file *ast.File) (string, bool) {
	const storekitPath = `"github.com/gradionhq/margince/backend/internal/platform/database/storekit"`
	for _, imp := range file.Imports {
		if imp.Path.Value != storekitPath {
			continue
		}
		if imp.Name != nil {
			return imp.Name.Name, true
		}
		return "storekit", true
	}
	return "", false
}
