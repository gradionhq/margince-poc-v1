package backendarch

// The write-shape obligation as a fitness function: every module
// mutation that writes an audit row commits a paired outbox event in the
// same function (data-model §11, events.md §4.2 — spelled once in
// storekit). A mutation that audits without emitting silently exempts
// itself from the event backbone; this test turns that from a reviewer
// memory into a gate. Exceptions are explicit and each carries the
// decision that ratified it — an allow-list entry without a reason is a
// finding, not a pass.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
)

// auditOnlyWrites are the ratified audit-without-event functions. Every
// entry carries the rationale for the waiver inline, so the gate is
// self-contained on a clean checkout; an entry without a rationale is a
// finding, not a pass. When the spec's events.md gains the missing
// event types, wiring storekit.Emit into these mutations removes the
// entries.
var auditOnlyWrites = map[string]string{
	"CreateList":        "events.md defines no list.* event types yet (spec question filed as feedback/07)",
	"ArchiveList":       "events.md defines no list.* event types yet (spec question filed as feedback/07)",
	"AddMember":         "events.md defines no list.* event types yet (spec question filed as feedback/07)",
	"CreateTag":         "events.md defines no tag.* event types yet (spec question filed as feedback/07)",
	"ArchiveTag":        "events.md defines no tag.* event types yet (spec question filed as feedback/07)",
	"ApplyTag":          "events.md defines no tag.* event types yet (spec question filed as feedback/07)",
	"CreateDSR":         "events.md defines no dsr.* event types yet (spec question filed as feedback/07)",
	"UpdateDSR":         "events.md defines no dsr.* event types yet (spec question filed as feedback/07)",
	"CreateRecordGrant": "events.md defines no grant.* event types yet (spec question filed as feedback/07)",
	"RevokeRecordGrant": "events.md defines no grant.* event types yet (spec question filed as feedback/07)",
}

func TestEveryAuditedMutationEmitsAnEvent(t *testing.T) {
	for fn, rationale := range auditOnlyWrites {
		if strings.TrimSpace(rationale) == "" {
			t.Errorf("auditOnlyWrites[%s] has no rationale — a waiver must say why the event is missing", fn)
		}
	}
	fset := token.NewFileSet()
	err := filepath.WalkDir("internal/modules", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return err
		}
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
				if _, ratified := auditOnlyWrites[fn.Name.Name]; !ratified {
					t.Errorf("%s: %s calls storekit.Audit without storekit.Emit — every audited mutation ships its event, or the exception is ratified in auditOnlyWrites",
						path, fn.Name.Name)
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
