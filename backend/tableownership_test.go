// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package backendarch

// Table ownership as a fitness function: the import DAG is enforced three
// ways, but nothing in the import graph stops a package from writing SQL
// against a table it does not own. This test closes that gap — it walks the
// hand-written Go under internal/modules and internal/compose, extracts every
// INSERT/UPDATE/DELETE target from SQL string literals (plus the storekit
// Patch.Apply table argument), and asserts each module only writes its own
// tables. Cross-store writes exist by design (merge relinks, GDPR erasure,
// ingest materialization); each one is ratified below with a self-contained
// rationale — an entry without a rationale is a finding, not a pass, and a
// waiver that matches no remaining write is stale and fails too. SELECTs are
// out of scope: reads are governed by RLS and the platform/auth row-scope
// clauses, not by ownership.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// storekitOwned marks the tables written ONLY through
// platform/database/storekit (Audit/Emit) or the migration runner — no
// walked package owns them, so any direct module write needs a waiver.
const storekitOwned = "internal/platform/database/storekit"

// tableOwners maps every core-migration table to the ONE module whose store
// owns its writes (module doc.go "Tables owned" declarations, kept in sync).
// This map is the hand-maintained artifact: a new table gets an owner here
// before its first write lands.
var tableOwners = map[string]string{
	// identity
	"workspace":                "internal/modules/identity",
	"app_user":                 "internal/modules/identity",
	"team":                     "internal/modules/identity",
	"team_membership":          "internal/modules/identity",
	"session":                  "internal/modules/identity",
	"passport":                 "internal/modules/identity",
	"role":                     "internal/modules/identity",
	"role_assignment":          "internal/modules/identity",
	"record_grant":             "internal/modules/identity",
	"oauth_client":             "internal/modules/identity",
	"oauth_authorization_code": "internal/modules/identity",
	// people
	"person":                     "internal/modules/people",
	"person_email":               "internal/modules/people",
	"person_phone":               "internal/modules/people",
	"organization":               "internal/modules/people",
	"organization_domain":        "internal/modules/people",
	"relationship":               "internal/modules/people",
	"partner":                    "internal/modules/people",
	"lead":                       "internal/modules/people",
	"organization_profile_field": "internal/modules/people",
	// deals (incl. the E03 offer engine: rate-card + versioned offers)
	"deal":               "internal/modules/deals",
	"pipeline":           "internal/modules/deals",
	"stage":              "internal/modules/deals",
	"deal_stage_history": "internal/modules/deals",
	"fx_rate":            "internal/modules/deals",
	"product":            "internal/modules/deals",
	"offer":              "internal/modules/deals",
	"offer_line_item":    "internal/modules/deals",
	// activities
	"activity":      "internal/modules/activities",
	"activity_link": "internal/modules/activities",
	"attachment":    "internal/modules/activities",
	"booking_page":  "internal/modules/activities",
	// approvals (workspace_signing_key backs the approval-token JWS)
	"approval":              "internal/modules/approvals",
	"workspace_signing_key": "internal/modules/approvals",
	// consent (the DSR case queue and the retention-policy catalog are
	// consent's; the engines that EXECUTE them live in privacy)
	"consent_purpose":      "internal/modules/consent",
	"person_consent":       "internal/modules/consent",
	"consent_event":        "internal/modules/consent",
	"consent_doi_token":    "internal/modules/consent",
	"data_subject_request": "internal/modules/consent",
	"retention_policy":     "internal/modules/consent",
	"preference_token":     "internal/modules/consent",
	// capture
	"raw_capture":          "internal/modules/capture",
	"connector_connection": "internal/modules/capture",
	// search
	"embedding": "internal/modules/search",
	// ai (voice DNA: the derived profile artifact + corpus manifest)
	"ai_usage":            "internal/modules/ai",
	"voice_profile":       "internal/modules/ai",
	"voice_corpus_source": "internal/modules/ai",
	// agents (incl. the runner subpackage)
	"agent_run":    "internal/modules/agents",
	"runner_job":   "internal/modules/agents",
	"workflow_run": "internal/modules/agents",
	"automation":   "internal/modules/agents",
	// signals (the warm-room signal spine + its append-only resolution log)
	"signal":            "internal/modules/signals",
	"signal_resolution": "internal/modules/signals",
	// collections
	"list":        "internal/modules/collections",
	"list_member": "internal/modules/collections",
	"tag":         "internal/modules/collections",
	"taggable":    "internal/modules/collections",
	"saved_view":  "internal/modules/collections",
	// webhooks (the outbound integration surface: subscriptions + their
	// per-attempt delivery log)
	"webhook_subscription": "internal/modules/webhooks",
	"webhook_delivery":     "internal/modules/webhooks",
	// privacy (the erasure suppression list is the module's own state;
	// its other writes are ratified waivers below)
	"erasure_suppression": "internal/modules/privacy",
	// compose (HTTP replay protection is transport plumbing, not domain;
	// the brief read model is the cross-module ranker's own snapshot —
	// deals + people strength + activities compose only here)
	"idempotency_key": "internal/compose",
	"brief_run":       "internal/compose",
	"brief_item":      "internal/compose",
	// platform: the audit+outbox pair has ONE sanctioned writer, and the
	// shared field-provenance layer (B-E02.12) is spelled once next to it
	"audit_log":        storekitOwned,
	"event_outbox":     storekitOwned,
	"field_provenance": storekitOwned,
}

// crossStoreWrites are the ratified writes outside the writer's own tables,
// keyed "module-dir:table". Every entry carries its rationale inline so the
// gate is self-contained on a clean checkout.
var crossStoreWrites = map[string]string{
	// people's merge/promotion relink rows across aggregates inside their
	// single transaction — the ratified cross-aggregate ownership call in
	// decisions/0011: a merge that could half-commit its relinks would
	// corrupt referential history.
	"internal/modules/people:deal":           "merge/promote relink deal FK rows in the decisions/0011 single transaction",
	"internal/modules/people:activity_link":  "merge/promote relink timeline links in the decisions/0011 single transaction",
	"internal/modules/people:list_member":    "merge relinks list memberships (and archive purges them) in the decisions/0011 single transaction",
	"internal/modules/people:taggable":       "merge relinks tag rows (and archive purges them) in the decisions/0011 single transaction",
	"internal/modules/people:person_consent": "merge carries the survivor's consent state in the decisions/0011 single transaction",
	"internal/modules/people:consent_event":  "merge re-points the append-only consent proof log in the decisions/0011 single transaction",

	// activities maintains the deal-timeline denormalization where the
	// activity lands: deal.last_activity_at moves in the same transaction
	// as the activity insert or the two drift.
	"internal/modules/activities:deal": "deal.last_activity_at is denormalized from the timeline; it must move in the activity's own transaction",

	// capture is the ONE connector.Sink (interfaces.md §1): one transaction
	// per inbound record writes raw original + normalized domain row, so a
	// crash can never keep evidence without the record or vice versa.
	"internal/modules/capture:activity":      "the connector sink materializes the normalized activity in the same transaction as its raw_capture original",
	"internal/modules/capture:activity_link": "the connector sink links the materialized activity in the same ingest transaction",
	"internal/modules/capture:lead":          "the connector sink materializes inbound leads in the same transaction as their raw_capture original",

	// deals' archive purges the archived deal's collection memberships in
	// the same transaction — a dangling list/tag row would resurrect the
	// deal in segment queries.
	"internal/modules/deals:list_member":  "archiving a deal removes its list memberships in the archive transaction",
	"internal/modules/deals:taggable":     "archiving a deal removes its tag rows in the archive transaction",
	"internal/modules/deals:relationship": "archiving a deal archives its stakeholder relationships in the archive transaction — a live relationship to an archived deal would leak it into row-scope walks",

	// privacy is the module whose JOB is crossing stores: a data-subject
	// obligation (erasure Art. 17, retention ADR-0011) must reach every
	// table holding the subject in ONE transaction per record — the
	// decisions/0011 single-transaction exception; routing each purge
	// through the owning module's API would trade the atomicity that IS
	// the guarantee for boundary hygiene.
	"internal/modules/privacy:person":           "erasure/retention anonymize the person row in place in the single erasure transaction (Art. 17, decisions/0011 exception)",
	"internal/modules/privacy:person_email":     "erasure deletes the subject's email channel rows in the single erasure transaction",
	"internal/modules/privacy:person_phone":     "erasure deletes the subject's phone channel rows in the single erasure transaction",
	"internal/modules/privacy:lead":             "erasure/retention anonymize the subject's segregated lead rows in the same transaction",
	"internal/modules/privacy:activity":         "retention archives/erases over-age timeline rows, and Art. 17 erasure redacts subject-only activity subject/body, in the single erasure/per-record transaction",
	"internal/modules/privacy:attachment":       "Art. 17 erasure deletes attachments hung off the subject or a subject-only activity in the single erasure transaction",
	"internal/modules/privacy:deal":             "retention archives over-age lost deals per its audited per-record transaction",
	"internal/modules/privacy:embedding":        "erasure/retention purge the subject's vectors — a similarity probe must not reconstruct erased text",
	"internal/modules/privacy:raw_capture":      "erasure purges raw provider payloads carrying the subject's identifiers in the single erasure transaction",
	"internal/modules/privacy:field_provenance": "Art. 17 erasure deletes the subject's field-origin metadata in the single erasure transaction — provenance must not outlive the fields it annotates",

	// direct audit_log/event_outbox writers: storekit.Audit stamps
	// captured_by from an authenticated principal, which these paths do
	// not have or which need columns storekit's writer does not carry.
	"internal/modules/identity:audit_log":     "login, failed-login and passport audits fire before/without an authenticated principal for storekit.Audit to stamp; identity appends the same append-only rows itself",
	"internal/modules/approvals:audit_log":    "approval evidence stamps passport_id/on_behalf_of, columns storekit's writer does not carry; same append-only table, this module's own writer",
	"internal/modules/approvals:event_outbox": "approvals stages its events with the full envelope (passport actor fields) storekit.Emit does not carry; still outbox-only publishing",
}

// sqlWriteTargets extracts write-statement table names from one SQL (or
// SQL-carrying format) string. UPDATE requires a SET clause so prose and
// `DO UPDATE SET`/`FOR UPDATE` never match; INSERT/DELETE are unambiguous.
var (
	insertRe = regexp.MustCompile(`(?is)\binsert\s+into\s+([a-z_][a-z0-9_]*)`)
	deleteRe = regexp.MustCompile(`(?is)\bdelete\s+from\s+([a-z_][a-z0-9_]*)`)
	updateRe = regexp.MustCompile(`(?is)\b(do\s+|for\s+)?update\s+([a-z_][a-z0-9_]*)\s+(?:[a-z_][a-z0-9_]*\s+)?set\b`)
)

func sqlWriteTargets(literal string) []string {
	var tables []string
	for _, m := range insertRe.FindAllStringSubmatch(literal, -1) {
		tables = append(tables, strings.ToLower(m[1]))
	}
	for _, m := range deleteRe.FindAllStringSubmatch(literal, -1) {
		tables = append(tables, strings.ToLower(m[1]))
	}
	for _, m := range updateRe.FindAllStringSubmatch(literal, -1) {
		if m[1] != "" { // ON CONFLICT … DO UPDATE / SELECT … FOR UPDATE — not a new target
			continue
		}
		tables = append(tables, strings.ToLower(m[2]))
	}
	return tables
}

// owningDir normalizes a package dir to its ownership unit: the module root
// under internal/modules (subpackages share their module's ownership), or
// internal/compose.
func owningDir(pkgDir string) string {
	if strings.HasPrefix(pkgDir, "internal/modules/") {
		parts := strings.SplitN(pkgDir, "/", 4)
		return strings.Join(parts[:3], "/")
	}
	return pkgDir
}

type tableWrite struct {
	pos   string // file:line for the finding
	table string
}

// collectTableWrites walks every non-test module/compose source file and
// records each SQL write target (string literals plus storekit's
// Patch.Apply table argument) under its owning directory.
func collectTableWrites(t *testing.T) map[string][]tableWrite {
	t.Helper()
	writes := map[string][]tableWrite{} // owning dir → writes
	fset := token.NewFileSet()
	for _, root := range []string{"internal/modules", "internal/compose"} {
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") ||
				strings.HasSuffix(path, "_test.go") || strings.HasSuffix(path, "_gen.go") {
				return err
			}
			path = filepath.ToSlash(path)
			file, err := parser.ParseFile(fset, path, nil, 0)
			if err != nil {
				return err
			}
			owner := owningDir(filepath.ToSlash(filepath.Dir(path)))
			record := func(pos token.Pos, tables []string) {
				for _, table := range tables {
					writes[owner] = append(writes[owner], tableWrite{pos: fset.Position(pos).String(), table: table})
				}
			}
			ast.Inspect(file, func(n ast.Node) bool {
				switch node := n.(type) {
				case *ast.BasicLit:
					if node.Kind != token.STRING {
						return true
					}
					text, err := strconv.Unquote(node.Value)
					if err != nil {
						return true
					}
					record(node.Pos(), sqlWriteTargets(text))
				case *ast.CallExpr:
					// storekit's versioned patch: Patch.Apply(ctx, tx, table,
					// id, ifVersion) issues the UPDATE — the table rides as
					// the third argument.
					sel, ok := node.Fun.(*ast.SelectorExpr)
					if !ok || sel.Sel.Name != "Apply" || len(node.Args) < 4 {
						return true
					}
					if lit, ok := node.Args[2].(*ast.BasicLit); ok && lit.Kind == token.STRING {
						if table, err := strconv.Unquote(lit.Value); err == nil {
							record(node.Pos(), []string{strings.ToLower(table)})
						}
					}
				}
				return true
			})
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	return writes
}

func TestEveryPackageOnlyWritesTablesItOwns(t *testing.T) {
	for key, rationale := range crossStoreWrites {
		if strings.TrimSpace(rationale) == "" {
			t.Errorf("crossStoreWrites[%s] has no rationale — a cross-store write must say why it cannot go through the owning module", key)
		}
	}

	writes := collectTableWrites(t)

	usedWaivers := map[string]bool{}
	for owner, ws := range writes {
		for _, w := range ws {
			declared, known := tableOwners[w.table]
			if !known {
				t.Errorf("%s: %s writes table %q which has no declared owner — add it to tableOwners in %s",
					w.pos, owner, w.table, "backend/tableownership_test.go")
				continue
			}
			if declared == owner {
				continue
			}
			key := owner + ":" + w.table
			if _, ratified := crossStoreWrites[key]; ratified {
				usedWaivers[key] = true
				continue
			}
			t.Errorf("%s: %s writes table %q owned by %s — move the write into the owning module, or ratify it in crossStoreWrites[%q] with a self-contained rationale",
				w.pos, owner, w.table, declared, key)
		}
	}

	var stale []string
	for key := range crossStoreWrites {
		if !usedWaivers[key] {
			stale = append(stale, key)
		}
	}
	sort.Strings(stale)
	for _, key := range stale {
		t.Errorf("crossStoreWrites[%s] matches no remaining write — stale waiver, remove it", key)
	}
}
