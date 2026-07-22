// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package backendarch

// The concurrency-guard obligation as a fitness function: every
// single-row-by-id UPDATE of a mutable entity carries SOME guard — the
// optimistic version (storekit.ApplyWithVersion / ApplyGuarded), a held
// row lock (LockRow / LockPair + ApplyLocked), an advisory lock, an
// in-statement FOR UPDATE, or a checked conditional write (the
// RowsAffected CAS shape). An unguarded by-id UPDATE is the
// last-writer-wins bug class this repo removed from storekit; this test
// keeps raw SQL from reintroducing it. Set-based writes (relinks,
// sweeps over a WHERE that is not the primary key) are out of scope by
// construction — they are not read-modify-write on one row.
//
// Exceptions are explicit, keyed by package path + function, each with
// the rationale that ratified it; a reasonless or stale waiver fails.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// byIDUpdate matches a single-row-by-primary-key UPDATE inside one SQL
// string literal — the shape that must carry a concurrency guard. The
// obligation applies to VERSIONED tables (the schema's own declaration
// that a row is a concurrently-edited entity), derived from the
// migrations rather than maintained as a list.
var byIDUpdate = regexp.MustCompile(`(?is)\bUPDATE\s+([a-z_]+)\s.*\bSET\b.*\bWHERE\s+(?:[a-z]\.)?id\s*=\s*\$`)

var (
	// createTableLine opens a CREATE TABLE block; versionColumnLine marks
	// the block's table as optimistic-locking. Line-based on purpose:
	// column definitions nest parentheses (generated tsvector columns)
	// beyond what a block regex can pair.
	createTableLine   = regexp.MustCompile(`(?i)^\s*CREATE TABLE (?:IF NOT EXISTS )?([a-z_]+)\s*\(`)
	versionColumnLine = regexp.MustCompile(`(?i)^\s*version\s+bigint`)
)

// versionedTables derives the set of optimistic-locking tables from the
// migration sources: any CREATE TABLE whose columns include "version".
func versionedTables(t *testing.T) map[string]bool {
	t.Helper()
	tables := map[string]bool{}
	for _, root := range []string{"migrations/core", "migrations/custom"} {
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(path, ".up.sql") {
				return err
			}
			raw, err := os.ReadFile(path) // #nosec G304 G122 -- path is a *.up.sql file from walking the trusted migrations tree
			if err != nil {
				return err
			}
			current := ""
			for _, line := range strings.Split(string(raw), "\n") {
				if m := createTableLine.FindStringSubmatch(line); m != nil {
					current = m[1]
					continue
				}
				if strings.HasPrefix(line, ");") {
					current = ""
					continue
				}
				if current != "" && versionColumnLine.MatchString(line) {
					tables[current] = true
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if len(tables) < 10 {
		t.Fatalf("derived only %d versioned tables from migrations — the derivation is broken, not the schema", len(tables))
	}
	return tables
}

// unguardedByIDUpdates are the ratified guard-free by-id updates, keyed
// by "package-dir:FuncName". Every entry carries its rationale inline;
// an entry without one is a finding, and one matching no function is
// stale and fails.
var unguardedByIDUpdates = map[string]string{
	// Archive is an absolute idempotent transition: the write sets
	// archived_at unconditionally (no state derived from a pre-read),
	// so concurrent archives converge on the same terminal row and the
	// in-transaction visibility read supplies the NotFound.
	"internal/modules/automation:Archive":           "absolute idempotent archive transition; concurrent archives converge, the visibility pre-read only feeds the audit before-image",
	"internal/modules/collections:ArchiveList":      "absolute idempotent archive transition; the RETURNING + archived_at IS NULL predicate makes a lost race read as already archived",
	"internal/modules/collections:ArchiveSavedView": "absolute idempotent archive transition; the RETURNING + archived_at IS NULL predicate makes a lost race read as already archived",
	"internal/modules/collections:ArchiveTag":       "absolute idempotent archive transition; the RETURNING + archived_at IS NULL predicate makes a lost race read as already archived",
	"internal/modules/deals:ArchiveDeal":            "absolute idempotent archive transition (deal + its edges); concurrent archives converge, the visibility pre-read only feeds the response",
	"internal/modules/deals:ArchiveProduct":         "absolute idempotent archive transition; concurrent archives converge, the visibility pre-read only feeds the response",
	"internal/modules/deals:ArchiveOfferTemplate":   "absolute idempotent archive transition; concurrent archives converge, the visibility pre-read only feeds the response",
	"internal/modules/people:ArchivePerson":         "absolute idempotent archive transition (person + child rows); concurrent archives converge, the visibility pre-read only feeds the response",
	"internal/modules/people:ArchiveOrganization":   "absolute idempotent archive transition (org + child rows); concurrent archives converge, the visibility pre-read only feeds the response",
	"internal/modules/people:ArchiveRelationship":   "absolute idempotent archive transition; the RETURNING + archived_at IS NULL predicate makes a lost race read as already archived",
	"internal/modules/quotas:ArchiveQuota":          "absolute idempotent archive transition; concurrent archives converge, the visibility pre-read only feeds the response",
	"internal/modules/signals:ArchiveSignal":        "absolute idempotent archive transition; concurrent archives converge, the visibility pre-read only feeds the response",

	// Writes that run UNDER a lock taken by their caller (or a lock the
	// function's own helper mints) — the guard exists, one frame up.
	"internal/modules/approvals:applyEditedPayload": "runs only inside decideInTx, after its FOR UPDATE lock on the approval row",
	"internal/modules/customfields:Rename":          "runs under the catalog row lock minted by lockField (FOR UPDATE before every decision read), with the If-Match version checked under that lock",
	"internal/modules/customfields:Retire":          "runs under the catalog row lock minted by lockField (FOR UPDATE before every decision read); the flip is an absolute idempotent transition besides",
	"internal/modules/customfields:setOptionsInTx":  "runs under the catalog row lock minted by lockPicklistField (FOR UPDATE before every decision read), plus the per-table advisory lock serializeSchemaChange mints",
	"internal/modules/people:resolveOrCreateAnchor": "mints its own lock: anchorOrganization(lock=true) takes FOR UPDATE on the anchor row before the name update, so concurrent company-form saves serialize (the form carries no version — the company is one standing record, not an optimistically concurrent one)",
	"internal/modules/deals:ArchiveOffer":           "runs under the offer row lock taken by visibleOfferLocked, and the write itself is an absolute archive transition",
	"internal/modules/deals:UpdateOfferLineItem":    "runs under the parent offer's row lock taken by visibleOfferLocked, which serializes every line edit",
	"internal/modules/deals:recomputeOfferTotals":   "every caller holds the offer row lock via visibleOfferLocked, except createOfferTx where the offer row was inserted in the same transaction",
	"internal/modules/people:absorbOrgReferences":   "runs under the merge pair lock (storekit.LockPair on both organization rows) taken by MergeOrganization",
	"internal/modules/signals:dropUnattributable":   "runs only inside resolveTx, under its signal row lock (storekit.LockRow before the terminal-state pre-read)",
	"internal/modules/signals:resolveToOrg":         "runs only inside resolveTx, under its signal row lock (storekit.LockRow before the terminal-state pre-read)",
	"internal/modules/signals:flagAmbiguous":        "runs only inside resolveTx, under its signal row lock (storekit.LockRow before the terminal-state pre-read)",
	"internal/modules/ai:SetBuildStage":             "stage is display-only forward progress; the status=running predicate makes a raced write a harmless no-op",
	"internal/modules/ai:DeferBuild":                "the status=running predicate is the CAS: a build already finished or re-claimed matches zero rows and the deferral is dropped",
	"internal/modules/ai:finishBuildTx":             "runs only under its callers' row lock — ClaimBuild's claim UPDATE or the FOR UPDATE pre-read in FailBuild/CompleteBuild, same transaction",
	"internal/modules/ai:persistBuildVersion":       "runs only inside CompleteBuild's transaction, under its voice_profile row lock (storekit.LockRow before the pre-read)",

	// Writes that are race-free by their own shape.
	"internal/modules/activities:insertActivityLinks": "deal.last_activity_at is a single-statement monotone max (greatest of stored and new) — the value is computed inside the UPDATE, never from a pre-read, so concurrent writers converge on the maximum",
	"internal/modules/people:recomputeLeadScoreTx":    "derived-score write recomputed from committed facts; a lost race is last-writer-wins on a value the next recompute re-derives identically",
	"internal/modules/privacy:anonymizeSubjectRows":   "terminal absolute write: the erasure overwrites the PII columns regardless of concurrent state, by design",
	"internal/modules/privacy:apply":                  "terminal absolute writes: the retention sweep archives/anonymizes regardless of concurrent state, by design",
}

// guardMarkers are the identifiers whose presence in the same function
// witnesses a concurrency guard: the storekit guarded-apply family, the
// lock mints, and the RowsAffected conditional-write (CAS) check.
var guardMarkers = map[string]bool{
	"ApplyWithVersion": true,
	"ApplyGuarded":     true,
	"ApplyLocked":      true,
	"LockRow":          true,
	"LockPair":         true,
	"RowsAffected":     true,
}

func TestEveryByIDUpdateCarriesAConcurrencyGuard(t *testing.T) {
	for fn, rationale := range unguardedByIDUpdates {
		if strings.TrimSpace(rationale) == "" {
			t.Errorf("unguardedByIDUpdates[%s] has no rationale — a waiver must say why no guard is needed", fn)
		}
	}
	versioned := versionedTables(t)
	used := map[string]bool{}
	fset := token.NewFileSet()
	for _, root := range []string{"internal/modules", "internal/compose", "internal/platform"} {
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") ||
				isIntegrationTagged(path) {
				return err
			}
			path = filepath.ToSlash(path)
			file, err := parser.ParseFile(fset, path, nil, 0)
			if err != nil {
				return err
			}
			for _, decl := range file.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if !ok || fn.Body == nil {
					continue
				}
				var updatesByID, guarded bool
				ast.Inspect(fn.Body, func(n ast.Node) bool {
					switch node := n.(type) {
					case *ast.BasicLit:
						if node.Kind != token.STRING {
							return true
						}
						lit, err := strconv.Unquote(node.Value)
						if err != nil {
							return true
						}
						if m := byIDUpdate.FindStringSubmatch(lit); m != nil && versioned[m[1]] {
							updatesByID = true
						}
						if strings.Contains(lit, "FOR UPDATE") || strings.Contains(lit, "pg_advisory_xact_lock") {
							guarded = true
						}
					case *ast.SelectorExpr:
						if guardMarkers[node.Sel.Name] {
							guarded = true
						}
					}
					return true
				})
				if updatesByID && !guarded {
					key := filepath.ToSlash(filepath.Dir(path)) + ":" + fn.Name.Name
					if _, ratified := unguardedByIDUpdates[key]; ratified {
						used[key] = true
						continue
					}
					t.Errorf("%s: %s runs a by-id UPDATE with no concurrency guard — use storekit.ApplyGuarded/ApplyWithVersion, lock the row first (LockRow/LockPair/FOR UPDATE/advisory lock), or check RowsAffected as a CAS; a real exception is ratified in unguardedByIDUpdates",
						path, fn.Name.Name)
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	for key := range unguardedByIDUpdates {
		if !used[key] {
			t.Errorf("unguardedByIDUpdates[%s] matches no unguarded by-id update — stale waiver, remove it", key)
		}
	}
}
