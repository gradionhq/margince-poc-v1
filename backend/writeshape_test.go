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

// auditOnlyWrites are the ratified audit-without-event functions.
var auditOnlyWrites = map[string]string{
	// TEMPORARY: the spec now defines pipeline.*/stage.* events
	// (events.md §5.3b) — pipeline/stage mutations must emit them and
	// this entry must be removed with that change (STATUS.md pickup item).
	"createPipelineTx": "pipeline config emission pending (events.md §5.3b)",
	// The events.md closed catalog defines no list.*/tag.* types (filed
	// in feedback/) — organizational metadata rides the audit-only lane
	// until the spec adds them.
	"CreateList":  "no list.* event types in the events.md closed catalog",
	"ArchiveList": "no list.* event types in the events.md closed catalog",
	"AddMember":   "no list.* event types in the events.md closed catalog",
	"CreateTag":   "no tag.* event types in the events.md closed catalog",
	"ArchiveTag":  "no tag.* event types in the events.md closed catalog",
	"ApplyTag":    "no tag.* event types in the events.md closed catalog",
}

func TestEveryAuditedMutationEmitsAnEvent(t *testing.T) {
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
