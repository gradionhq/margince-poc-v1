// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package backendarch

// The write-shape obligation as a fitness function: every mutation that
// writes an audit row commits a paired outbox event in the same function
// (data-model §11, events.md §4.2 — spelled once in storekit), across
// modules AND the composition layer. A mutation that audits without
// emitting silently exempts itself from the event backbone; this test
// turns that from a reviewer memory into a gate. Exceptions are explicit,
// keyed by package path + function so a same-named function elsewhere is
// never silently waived, and each carries the decision that ratified it —
// an allow-list entry without a reason is a finding, not a pass. A waiver
// that no longer matches an audit-only function is stale and fails too.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
)

// auditOnlyWrites are the ratified audit-without-event functions, keyed
// by "package-dir:FuncName". Every entry carries the rationale for the
// waiver inline, so the gate is self-contained on a clean checkout; an
// entry without a rationale is a finding, not a pass. When the spec's
// events.md gains the missing event types, wiring storekit.Emit into
// these mutations removes the entries.
var auditOnlyWrites = map[string]string{
	"internal/modules/collections:CreateList":     "events.md defines no list.* event types yet (spec question filed as feedback/07)",
	"internal/modules/collections:ArchiveList":    "events.md defines no list.* event types yet (spec question filed as feedback/07)",
	"internal/modules/collections:AddMember":      "events.md defines no list.* event types yet (spec question filed as feedback/07)",
	"internal/modules/collections:CreateTag":      "events.md defines no tag.* event types yet (spec question filed as feedback/07)",
	"internal/modules/collections:ArchiveTag":     "events.md defines no tag.* event types yet (spec question filed as feedback/07)",
	"internal/modules/collections:ApplyTag":       "events.md defines no tag.* event types yet (spec question filed as feedback/07)",
	"internal/modules/consent:CreateDSR":          "events.md defines no dsr.* event types yet (spec question filed as feedback/07)",
	"internal/modules/consent:UpdateDSR":          "events.md defines no dsr.* event types yet (spec question filed as feedback/07)",
	"internal/modules/identity:CreateRecordGrant": "events.md defines no grant.* event types yet (spec question filed as feedback/07)",
	"internal/modules/identity:RevokeRecordGrant": "events.md defines no grant.* event types yet (spec question filed as feedback/07)",
	"internal/modules/privacy:AssembleSAR":        "events.md defines no dsr.*/export event type for a subject-access export yet (same family as the CreateDSR gap, feedback/07); the export is a read whose only write IS the audit row",
}

func TestEveryAuditedMutationEmitsAnEvent(t *testing.T) {
	for fn, rationale := range auditOnlyWrites {
		if strings.TrimSpace(rationale) == "" {
			t.Errorf("auditOnlyWrites[%s] has no rationale — a waiver must say why the event is missing", fn)
		}
	}
	used := map[string]bool{}
	fset := token.NewFileSet()
	for _, root := range []string{"internal/modules", "internal/compose"} {
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
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
				var audits, emits bool
				ast.Inspect(fn.Body, func(n ast.Node) bool {
					sel, ok := n.(*ast.SelectorExpr)
					if !ok {
						return true
					}
					if pkg, ok := sel.X.(*ast.Ident); ok && pkg.Name == "storekit" {
						switch sel.Sel.Name {
						case "Audit":
							audits = true
						case "Emit":
							emits = true
						}
					}
					return true
				})
				if audits && !emits {
					key := filepath.ToSlash(filepath.Dir(path)) + ":" + fn.Name.Name
					if _, ratified := auditOnlyWrites[key]; ratified {
						used[key] = true
						continue
					}
					t.Errorf("%s: %s calls storekit.Audit without storekit.Emit — every audited mutation ships its event, or the exception is ratified in auditOnlyWrites",
						path, fn.Name.Name)
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	for key := range auditOnlyWrites {
		if !used[key] {
			t.Errorf("auditOnlyWrites[%s] matches no audit-only function — stale waiver, remove it", key)
		}
	}
}
