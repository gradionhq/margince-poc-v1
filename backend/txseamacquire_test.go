// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package backendarch

// Code that runs on a caller's `pgx.Tx` acquires no connection of its own.
//
// The C5 shared-tx seams exist so a caller can commit a sibling module's write
// and its own in ONE transaction. A seam that reaches for the pool while
// standing inside someone else's transaction breaks that in two ways, and
// neither announces itself. The second connection commits separately, so the
// "both or neither" claim the shape is chosen for is quietly false. And if the
// caller's transaction holds a lock the second one waits on, the two block each
// other inside a single goroutine — a deadlock Postgres cannot detect and will
// not break, because it sees two unrelated sessions waiting.
//
// The rule is not new; it is written on the acquirer that breaks it most often.
// The custom-field catalog read documents "callers fetch BEFORE opening their
// write/read transaction (never inside it — a nested pool acquire under load is
// a deadlock shape)". Deriving the obligation from the tree is what makes a
// seam written next month inherit it instead of re-deciding it.
//
// Two things bound what this can prove, and both are why the pool-of-one suite
// in compose/integration exists beside it: the gate catches the class across
// the diff, that suite catches the instance in a database.
//
//   - The acquirers are NAMED rather than inferred, because "does this call
//     reach the pool" is not decidable from syntax. They are the spellings this
//     tree has: the pool itself, the transaction openers (database.DB.Tx, the
//     modules' `s.tx` helper over it, database.With*Tx, a raw Begin), and the
//     field-catalog read, which opens a transaction of its own two calls down.
//     A new spelling would slip past until it is added here.
//   - A call reaches the pool through a package-local helper rather than
//     directly, and the walk does not follow it.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/gatekit"
)

// gatekit:fixture the calls this gate reads as taking a connection, and what
// each one does — the vocabulary the walk matches on, not waived costs
//
// connectionAcquirers are the calls that take a connection from the pool,
// keyed by the selector a call site spells. Each is a call a tx-accepting
// function must have made before it was handed the transaction, or delegated
// to a caller that did.
var connectionAcquirers = map[string]string{
	"Acquire":          "takes a connection straight from the pool",
	"Begin":            "opens a transaction on a pool, which takes a connection to hold it",
	"BeginTx":          "opens a transaction on a pool, which takes a connection to hold it",
	"Tx":               "opens a transaction on the bound handle (database.DB.Tx)",
	"tx":               "opens a transaction through the store's own helper over database.DB.Tx",
	"WithWorkspaceTx":  "opens a workspace-bound transaction",
	"WithInfraTx":      "opens an unbound infrastructure transaction",
	"activeColumns":    "reads the custom-field catalog, which opens a transaction of its own",
	"activeColumnsFor": "reads the custom-field catalog, which opens a transaction of its own",
	"ActiveColumns":    "reads the custom-field catalog through the fieldcatalog seam, which opens a transaction of its own",
}

func TestATxAcceptingFunctionAcquiresNoConnectionOfItsOwn(t *testing.T) {
	exempt := gatekit.Waive(map[string]string{})
	scope := gatekit.Scope{
		Roots:   []string{"internal", "cmd"},
		Subject: func(_ string, file *ast.File) bool { return len(txBorrowingBodies(file)) > 0 },
		Exempt:  exempt,
	}
	defer exempt.AssertAllMatched(t)

	for _, parsed := range scope.Files(t) {
		for _, body := range txBorrowingBodies(parsed.File) {
			for _, found := range body.acquires() {
				t.Errorf("%s: %s runs on a caller's pgx.Tx and then %s (%s) — fetch it before the "+
					"transaction opens and thread the result in, as mergePersonTx and createDealTx do; "+
					"a second connection inside someone else's transaction commits separately and "+
					"deadlocks undetectably against a lock that transaction holds",
					parsed.Path, body.name, connectionAcquirers[found], found)
			}
		}
	}
}

// txBorrowing is one body that runs on a transaction somebody else opened: a
// named function that takes a pgx.Tx, or the callback literal a transaction
// opener is handed. Both are the same obligation. The literal matters most —
// `WithWorkspaceTx(ctx, pool, func(tx pgx.Tx) error { … })` is where this tree
// writes, so a gate that judged only named seams would miss the shape it is
// most likely to meet.
type txBorrowing struct {
	name   string
	params *ast.FieldList
	body   *ast.BlockStmt
	// pgxName is the local name the file imports pgx under, so a seam in a
	// file that aliases the import is judged rather than skipped.
	pgxName string
}

// txBorrowingBodies answers every tx-borrowing body in one file, outermost
// first.
func txBorrowingBodies(file *ast.File) []txBorrowing {
	pgxName, imported := pgxLocalName(file)
	if !imported {
		return nil
	}
	var out []txBorrowing
	add := func(name string, params *ast.FieldList, body *ast.BlockStmt) bool {
		if body == nil || !takesPgxTx(params, pgxName) {
			return false
		}
		out = append(out, txBorrowing{name: name, params: params, body: body, pgxName: pgxName})
		return true
	}
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		add(fn.Name.Name, fn.Type.Params, fn.Body)
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			lit, ok := n.(*ast.FuncLit)
			if !ok {
				return true
			}
			// A tx-taking literal is judged as its own body, so the walk does
			// not descend into it twice.
			return !add(fn.Name.Name+"'s transaction callback", lit.Type.Params, lit.Body)
		})
	}
	return out
}

// pgxLocalName answers the name this file spells the pgx package under, and
// whether it imports it at all.
func pgxLocalName(file *ast.File) (string, bool) {
	for _, spec := range file.Imports {
		if spec.Path.Value != `"github.com/jackc/pgx/v5"` {
			continue
		}
		if spec.Name != nil {
			return spec.Name.Name, spec.Name.Name != "_"
		}
		return "pgx", true
	}
	return "", false
}

func takesPgxTx(params *ast.FieldList, pgxName string) bool {
	if params == nil {
		return false
	}
	for _, param := range params.List {
		if isPgxTx(param.Type, pgxName) {
			return true
		}
	}
	return false
}

func isPgxTx(expr ast.Expr, pgxName string) bool {
	sel, ok := expr.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "Tx" {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	return ok && pkg.Name == pgxName
}

// acquires answers the acquirer selectors called in this body, in source
// order, excluding the tx-borrowing literals inside it — each of those is
// judged as a body of its own.
//
// A call ON the borrowed transaction is never an acquirer: `tx.Begin` opens a
// savepoint on the connection the body was handed, which is the opposite of
// reaching for a second.
func (b txBorrowing) acquires() []string {
	var found []string
	ast.Inspect(b.body, func(n ast.Node) bool {
		if lit, ok := n.(*ast.FuncLit); ok {
			return !takesPgxTx(lit.Type.Params, b.pgxName)
		}
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		if _, isAcquirer := connectionAcquirers[sel.Sel.Name]; !isAcquirer {
			return true
		}
		if b.receiverIsTheBorrowedTx(sel.X) {
			return true
		}
		found = append(found, sel.Sel.Name)
		return true
	})
	return found
}

// receiverIsTheBorrowedTx reports whether a call's receiver is one of this
// body's own pgx.Tx parameters.
func (b txBorrowing) receiverIsTheBorrowedTx(recv ast.Expr) bool {
	ident, ok := recv.(*ast.Ident)
	if !ok {
		return false
	}
	for _, param := range b.params.List {
		if !isPgxTx(param.Type, b.pgxName) {
			continue
		}
		for _, name := range param.Names {
			if name.Name == ident.Name {
				return true
			}
		}
	}
	return false
}

// Guarding the gate: the walk above is only as good as its ability to see a
// violation, and a fitness function that has never failed is a claim, not a
// gate. Each case below runs it over source holding one defect it exists for,
// and over the repair that must read clean.
func TestTheGateSeesASeamThatReachesForTheCatalogInsideTheCallersTransaction(t *testing.T) {
	const seam = fixtureImports + `
func (s *Store) GetPersonTx(ctx context.Context, tx pgx.Tx, id ids.PersonID) (Person, error) {
	active, err := s.activeColumns(ctx, "person")
	if err != nil {
		return Person{}, err
	}
	return readPerson(ctx, tx, id, active)
}
`
	assertGateReads(t, seam, "GetPersonTx", "activeColumns")

	const repaired = fixtureImports + `
func (s *Store) GetPersonTx(ctx context.Context, tx pgx.Tx, id ids.PersonID, active []fieldcatalog.Column) (Person, error) {
	if _, err := tx.Begin(ctx); err != nil {
		return Person{}, err
	}
	return readPerson(ctx, tx, id, active)
}
`
	// Nothing, and one of the two reasons is the borrowed transaction: Begin
	// IS an acquirer on a pool, so this arm is what proves the exemption for a
	// savepoint on the connection the seam was handed.
	assertGateReads(t, repaired, "GetPersonTx")
}

// The shape the gate exists to catch most: not a named seam at all, but the
// callback a transaction opener is handed.
func TestTheGateSeesAnAcquireInsideATransactionCallback(t *testing.T) {
	const assembler = fixtureImports + `
func (s *Service) Assemble(ctx context.Context, id ids.PersonID) (Person, error) {
	var out Person
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		active, err := s.people.ActiveColumns(ctx, "person")
		if err != nil {
			return err
		}
		out, err = s.people.GetPersonTx(ctx, tx, id, active)
		return err
	})
	return out, err
}
`
	assertGateReads(t, assembler, "Assemble's transaction callback", "ActiveColumns")

	const repaired = fixtureImports + `
func (s *Service) Assemble(ctx context.Context, id ids.PersonID) (Person, error) {
	active, err := s.people.ActiveColumns(ctx, "person")
	if err != nil {
		return Person{}, err
	}
	var out Person
	err = database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		out, err = s.people.GetPersonTx(ctx, tx, id, active)
		return err
	})
	return out, err
}
`
	// The prefetch moved above the opener, so the callback is clean — and
	// Assemble itself borrows no transaction, so the WithWorkspaceTx call it
	// makes is not the gate's business.
	assertGateReads(t, repaired, "Assemble's transaction callback")
}

// A file that spells the import under another name is judged, not skipped.
func TestTheGateFollowsAnAliasedPgxImport(t *testing.T) {
	const aliased = `package people

import pg "github.com/jackc/pgx/v5"

func (s *Store) GetPersonTx(ctx context.Context, tx pg.Tx, id ids.PersonID) (Person, error) {
	active, err := s.activeColumns(ctx, "person")
	if err != nil {
		return Person{}, err
	}
	return readPerson(ctx, tx, id, active)
}
`
	assertGateReads(t, aliased, "GetPersonTx", "activeColumns")
}

// fixtureImports is the header every fixture above shares: the gate reads the
// import table to learn what "pgx" means in a file, so a fixture without it is
// not the code the gate judges.
const fixtureImports = `package people

import "github.com/jackc/pgx/v5"
`

// assertGateReads runs the walk over one fixture and asserts the acquirers it
// reports for the named body, so a case states the whole answer rather than a
// count.
func assertGateReads(t *testing.T, src, body string, want ...string) {
	t.Helper()
	bodies := txBorrowingBodies(parseGateFixture(t, src))
	for _, b := range bodies {
		if b.name != body {
			continue
		}
		got := b.acquires()
		if len(got) != len(want) {
			t.Fatalf("the gate read %v from %q, want %v", got, body, want)
		}
		for i := range got {
			if got[i] != want[i] {
				t.Fatalf("the gate read %v from %q, want %v", got, body, want)
			}
		}
		return
	}
	t.Fatalf("the gate found no body named %q in the fixture — it judged %d others", body, len(bodies))
}

// A pgx.Tx inside a func-TYPED parameter is a callback this function hands
// out, not a transaction it borrows: whoever supplies the literal is judged
// where they wrote it.
func TestTheGateReadsAFuncTypedParameterAsItsSuppliersBusiness(t *testing.T) {
	const callbackTaker = fixtureImports + `
func (s *Store) ClaimAndEnqueue(ctx context.Context, enqueue func(tx pgx.Tx) error) error {
	return s.claim(ctx, enqueue)
}
`
	for _, b := range txBorrowingBodies(parseGateFixture(t, callbackTaker)) {
		if b.name == "ClaimAndEnqueue" {
			t.Fatal("a function whose only pgx.Tx is the type of a callback it accepts was judged as borrowing a transaction")
		}
	}
}

func parseGateFixture(t *testing.T, src string) *ast.File {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), "gatefixture.go", src, 0)
	if err != nil {
		t.Fatalf("parsing the gate fixture: %v\n%s", err, strings.TrimSpace(src))
	}
	return file
}
