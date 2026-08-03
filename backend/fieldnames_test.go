// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package backendarch

// A field name published to a caller has to BE a field name.
//
// The defect this closes, found in seven places at once: a typed refusal put
// prose in the slot both surfaces publish as the machine-readable field —
// `RequiredFieldError{Field: "to (must follow from)"}`,
// `{Field: "ended_at: must not precede started_at"}`,
// `{Field: "kind: " + kind + " endpoint shape"}`. REST renders that slot as
// `details.errors[].field` and the MCP dispatcher renders it as the field token
// in `validation_error <field>=<code>`, so the answer came out garbled and
// nothing downstream could branch on it. Worse, every one of them arrived under
// the code `required` while the value was in fact supplied and merely
// inconsistent — so a caller acting on the code would add a field it had
// already sent.
//
// The rule: a string literal in a FieldFault's field position is a contract
// field path — lowercase, underscores, dots for nesting. Prose is a message, and
// FieldFault has a message parameter for it. A condition that names no single
// fixable argument is a MessageFault instead, which publishes no field at all;
// that is the honest answer when the mismatch is between two arguments rather
// than in one.
//
// Scope is DERIVED: the types policed are those implementing FieldFault or
// FieldFaults anywhere under internal/, so a new refusal type inherits this
// without being listed.
//
// MessageFault implementors are out of scope because the taxonomy publishes no
// field for them at all — their whole answer is a code and a message. That is a
// claim about the taxonomy, not about the types: a MessageFault type may still
// keep a Field member for its own message (compose's FieldNotAllowedError holds
// the rejected token that way), and a transport branch that lifted such a member
// into a wire `field` would be outside this walk. The report transport did
// exactly that until it was deleted in favour of the fault.

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

// wireFieldName is what a contract field path may look like: a lowercase segment,
// optionally dotted for nesting, optionally indexed by an ACTUAL index. Every
// legitimate field literal in the tree satisfies it, so anything that does not is
// prose. `[0-9]+` rather than `[0-9]*` because `field_keys[]` points a client at no
// element in particular, which is the same unactionable answer as prose.
var wireFieldName = regexp.MustCompile(`^[a-z][a-z0-9_]*(\[[0-9]+\])?(\.[a-z][a-z0-9_]*(\[[0-9]+\])?)*$`)

// fieldFaultMethods are the two forms that publish a field name to callers.
// MessageFault is absent on purpose: it publishes a code and a message only.
var fieldFaultMethods = map[string]bool{"FieldFault": true, "FieldFaults": true}

// internalTree is every hand-written package the product ships.
const internalTree = "internal"

type parsedFile struct {
	path string
	file *ast.File
}

func parseInternalTree(t *testing.T) []parsedFile {
	t.Helper()
	var out []parsedFile
	fset := token.NewFileSet()
	err := filepath.WalkDir(internalTree, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		path = filepath.ToSlash(path)
		if !strings.HasSuffix(path, ".go") ||
			strings.HasSuffix(path, "_test.go") ||
			strings.HasSuffix(path, "_gen.go") ||
			// Generated from crm.yaml and frozen; it declares no refusal types.
			strings.HasPrefix(path, internalTree+"/contracts/") {
			return nil
		}
		file, parseErr := parser.ParseFile(fset, path, nil, parser.ParseComments)
		if parseErr != nil {
			return parseErr
		}
		out = append(out, parsedFile{path: path, file: file})
		return nil
	})
	if err != nil {
		t.Fatalf("walking %s: %v", internalTree, err)
	}
	if len(out) == 0 {
		t.Fatalf("no source found under %s — the gate is reading the wrong tree", internalTree)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].path < out[j].path })
	return out
}

// fieldMemberIndex maps each struct type to the DECLARED position of its `Field`
// member, so a positional composite literal can be judged at the right operand.
//
// Assuming position 0 is what a first draft of this gate did, and it is only
// correct when Field happens to be declared first: for any other type, prose sat
// in a later operand while the walk judged an unrelated one and reported green.
func fieldMemberIndex(files []parsedFile) map[string]int {
	out := map[string]int{}
	for _, pf := range files {
		ast.Inspect(pf.file, func(n ast.Node) bool {
			spec, ok := n.(*ast.TypeSpec)
			if !ok {
				return true
			}
			structType, isStruct := spec.Type.(*ast.StructType)
			if !isStruct {
				return true
			}
			position := 0
			for _, member := range structType.Fields.List {
				for _, memberName := range member.Names {
					if memberName.Name == "Field" {
						out[spec.Name.Name] = position
					}
					position++
				}
			}
			return true
		})
	}
	return out
}

// typesPublishingAFieldName collects the type names that implement FieldFault or
// FieldFaults. Keyed by name alone: a literal names its type unqualified inside
// its own package, which is where these are all constructed.
func typesPublishingAFieldName(files []parsedFile) map[string]bool {
	out := map[string]bool{}
	for _, pf := range files {
		for _, decl := range pf.file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || !fieldFaultMethods[fn.Name.Name] {
				continue
			}
			if name := receiverTypeName(fn); name != "" {
				out[name] = true
			}
		}
	}
	return out
}

func TestAPublishedFieldNameIsAFieldNameNotProse(t *testing.T) {
	files := parseInternalTree(t)
	policed := typesPublishingAFieldName(files)
	if len(policed) == 0 {
		t.Fatal("no type implements FieldFault — the marker is stale and this gate asserts nothing")
	}

	fieldAt := fieldMemberIndex(files)
	checked := 0
	for _, pf := range files {
		ast.Inspect(pf.file, func(n ast.Node) bool {
			lit, ok := n.(*ast.CompositeLit)
			if !ok {
				return true
			}
			name, ok := lit.Type.(*ast.Ident)
			if !ok || !policed[name.Name] {
				return true
			}
			for i, elt := range lit.Elts {
				kv, ok := elt.(*ast.KeyValueExpr)
				if !ok {
					// A POSITIONAL literal (`&RequiredFieldError{"prose"}`) carries no
					// key to match, so a keyed-only walk skips it and reads green.
					// Judge the operand at Field's DECLARED position — not operand 0,
					// which would judge the wrong slot and certify prose sitting in
					// the right one.
					at, declared := fieldAt[name.Name]
					if !declared || i != at {
						// No Field member at all means the name, if any, is returned
						// from inside the method and judged by the walk below.
						continue
					}
					text, isLiteral := stringLiteralPrefix(elt)
					if !isLiteral {
						// A computed value in a positional field slot is unproven, not
						// clean. Say so rather than pass in silence.
						t.Errorf("%s: %s{…} sets Field positionally from a computed value, which this "+
							"gate cannot judge. Use a keyed literal so the field slot is readable.",
							pf.path, name.Name)
						continue
					}
					checked++
					reportIfProse(t, pf.path, name.Name, text)
					continue
				}
				if key, ok := kv.Key.(*ast.Ident); !ok || key.Name != "Field" {
					continue
				}
				// Only a literal can be judged here. A computed value
				// (`"kind: " + kind + …`) is caught by its literal PREFIX,
				// which is how the relationship-shape case was found.
				text, isLiteral := stringLiteralPrefix(kv.Value)
				if !isLiteral {
					continue
				}
				checked++
				reportIfProse(t, pf.path, name.Name, text)
			}
			return true
		})
	}
	// The field name does not have to live in a struct member at all: a type may
	// return it as a literal straight out of its FieldFault method, which is the
	// shape this diff itself adopted for RelationshipDatesError. A walk that read
	// only composite literals could not see those.
	for _, pf := range files {
		for _, decl := range pf.file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || !fieldFaultMethods[fn.Name.Name] || fn.Body == nil {
				continue
			}
			for _, name := range returnedFieldNames(fn.Body) {
				checked++
				reportIfProse(t, pf.path, receiverTypeName(fn)+"."+fn.Name.Name, name)
			}
		}
	}
	if checked == 0 {
		t.Fatal("no field name found on any FieldFault type — the walk proved nothing")
	}
}

// reportIfProse fails when text is not a contract field path.
func reportIfProse(t *testing.T, path, where, text string) {
	t.Helper()
	if wireFieldName.MatchString(text) {
		return
	}
	t.Errorf("%s: %s publishes field %q — that is prose in the field slot, and both surfaces "+
		"publish it as the machine-readable field name (REST details.errors[].field, and the MCP "+
		"dispatcher's `<field>=<code>`). Put the explanation in the message and leave a contract "+
		"field path here — or, if no single argument is the wrong one, implement MessageFault "+
		"instead, which publishes no field.", path, where, text)
}

// returnedFieldNames collects the literal first return value of every `return`
// in a FieldFault body — the field position of `(field, code, message)`.
func returnedFieldNames(body *ast.BlockStmt) []string {
	var out []string
	ast.Inspect(body, func(n ast.Node) bool {
		ret, ok := n.(*ast.ReturnStmt)
		if !ok || len(ret.Results) == 0 {
			return true
		}
		if text, isLiteral := stringLiteralPrefix(ret.Results[0]); isLiteral {
			out = append(out, text)
		}
		return true
	})
	return out
}

// stringLiteralPrefix returns the literal text of a string expression, or the
// ACCUMULATED literal prefix of a concatenation — every literal operand from the
// left, joined, up to the first computed one.
//
// Accumulating rather than taking the leftmost operand is what makes the gate
// hold against the obvious evasion. `"kind: " + kind` fails on its space either
// way, but splitting it as `"kind" + ": " + kind` parses as
// `(("kind" + ": ") + kind)`, whose leftmost operand is the perfectly
// field-shaped `"kind"` — so a leftmost-only reading certifies the exact prose it
// exists to refuse. Joined, the prefix is `kind: ` and the space fails it again.
//
// A trailing dot is dropped so a legitimate nested path (`"edits." + key`) is not
// read as malformed.
func stringLiteralPrefix(expr ast.Expr) (string, bool) {
	switch e := expr.(type) {
	case *ast.BasicLit:
		if e.Kind != token.STRING {
			return "", false
		}
		text, err := strconv.Unquote(e.Value)
		if err != nil {
			return "", false
		}
		return text, true
	case *ast.BinaryExpr:
		if e.Op != token.ADD {
			return "", false
		}
		left, ok := stringLiteralPrefix(e.X)
		if !ok {
			return "", false
		}
		// The right operand joins the prefix only when it too is literal; a
		// computed one ends the prefix and leaves what precedes it to be judged.
		if right, literal := stringLiteralPrefix(e.Y); literal {
			left += right
		}
		return strings.TrimSuffix(left, "."), true
	}
	return "", false
}
