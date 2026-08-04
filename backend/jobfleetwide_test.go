// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package backendarch

// A FleetWide declaration is a promise: this job enumerates and enqueues, and
// does no tenant write of its own (jobs.FleetWide). jobrole_test.go proves every
// job DECLARES a role; this gate proves the FleetWide half of that declaration
// is true of the code. Without it a pass could keep the marker, keep its null
// `args->>'workspace_id'`, and quietly do a tenant's work inside one row again —
// the shape whose per-workspace failures have nowhere durable to land.
//
// # The allowlist
//
// "Reaches an insert" is interprocedural — a dispatch travels through package
// helpers, injected inserters and store transactions — so it is not honestly
// AST-checkable, and a gate that pretended otherwise would be lying about what
// it verified. This one is a CLOSED ALLOWLIST instead: a dispatcher's Work must
// contain a call from the sanctioned fan-out set, spelled one of exactly two
// ways.
//
//  1. A compose fan-out helper: dispatchPerWorkspace or dispatchWith. Both take
//     the fleet and one argsFor closure and do a single atomic InsertMany.
//  2. A direct River insert: Insert, InsertMany or InsertManyTx, alongside a
//     resolution of the River client (river.ClientFromContext, or the Safely
//     variant). The pairing is what keeps the method names from matching any
//     store's own Insert. It is checked across the whole inspected closure, not
//     within one body, so a dispatcher that resolves the client in Work and
//     inserts in its helper still passes — deliberately, since this side only
//     grants a pass and never withholds one.
//
// A dispatcher that fans out some third way is a finding on purpose: adding a
// third spelling is then a deliberate act with a reviewer attached.
//
// # Which arm carries the weight
//
// The FAN-OUT requirement is the load-bearing one. The regression this phase
// exists to prevent is a worker that loops the fleet and calls a store method
// per tenant — the pre-conversion shape — and that worker calls none of the
// spellings above, so it fails here and nowhere else. jobbinding_test.go does
// not look at writes at all (it catches a Work body binding its own workspace),
// and the RLS lane catches only UNBOUND writes, while a fleet loop that binds
// the GUC per workspace and then writes satisfies RLS completely. Nothing
// downstream covers that shape.
//
// # Reads are fine; writes are not
//
// Several sanctioned dispatchers READ tenant tables — the due-scans that find
// which connections, builds or workspaces have work — and jobs.FleetWide
// expressly permits that. So the second arm prohibits WRITES, scoped to what is
// honestly visible: a SQL write STATEMENT in the dispatcher's own body or in a
// helper method on the same worker type that its Work calls. No dispatcher in
// the tree writes inline today, so this arm has no live subject; it is here so
// that the first inline write is a finding rather than a precedent.

import (
	"go/ast"
	"go/token"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// fleetWideDispatcherFloor guards against a vacuous pass. This gate resolves an
// association across two types — args declare FleetWide, a separate worker
// embeds river.WorkerDefaults[Args] and owns Work — so a walker that matched no
// files, or a resolution step that silently stopped associating the two, would
// inspect nothing and report green. The tree declares 23 FleetWide args types
// today, every one of them resolving to a worker; the floor sits at 20 so
// retiring a pass or two does not drag the gate along, while any wholesale
// failure to resolve still trips it.
const fleetWideDispatcherFloor = 20

// fanOutHelpers are the compose helpers that ARE a fan-out. See the allowlist
// above for why the set is closed.
var fanOutHelpers = []string{"dispatchPerWorkspace", "dispatchWith"}

// riverInsertMethods are the River client's own enqueue methods. They count as
// a fan-out only alongside riverClientResolverPrefix, so a store method that
// happens to be called Insert cannot satisfy this gate.
var riverInsertMethods = []string{"Insert", "InsertMany", "InsertManyTx"}

// riverClientResolverPrefix is how a Work body gets hold of the River client it
// inserts through. Matched as a PREFIX, because River offers two spellings and
// the tree uses both: ClientFromContext panics when there is none, so a
// dispatcher that may run without a client resolves ClientFromContextSafely
// instead — and two dispatchers carry comments steering the next author to that
// variant. Pinning the exact name would report those authors' correct code as
// "never fans out", which is the false positive that gets a gate weakened by
// the person it blocked.
const riverClientResolverPrefix = "ClientFromContext"

// fleetWideDispatcher is one resolved args→worker→Work association and what
// the gate found in it.
type fleetWideDispatcher struct {
	args    string
	worker  string
	pos     token.Position
	fansOut bool
	writes  []string
}

// fleetWideArgsTypes returns every args type declaring the FleetWide marker.
func fleetWideArgsTypes(files []*ast.File) map[string]bool {
	marked := map[string]bool{}
	for _, file := range files {
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Name.Name != "FleetWide" {
				continue
			}
			if recv := receiverTypeName(fn); recv != "" {
				marked[recv] = true
			}
		}
	}
	return marked
}

// workerByArgs maps an args type name to the worker type that works it, read
// off the embedded river.WorkerDefaults[Args]. This is the association a
// method-name index cannot make: the marker is on the args, the behaviour is on
// the worker, and the generic embed is the only thing joining them.
func workerByArgs(files []*ast.File) map[string]string {
	byArgs := map[string]string{}
	for _, file := range files {
		ast.Inspect(file, func(n ast.Node) bool {
			spec, ok := n.(*ast.TypeSpec)
			if !ok {
				return true
			}
			structType, ok := spec.Type.(*ast.StructType)
			if !ok {
				return true
			}
			for _, field := range structType.Fields.List {
				if len(field.Names) > 0 {
					continue
				}
				index, ok := field.Type.(*ast.IndexExpr)
				if !ok {
					continue
				}
				sel, ok := index.X.(*ast.SelectorExpr)
				if !ok || sel.Sel.Name != "WorkerDefaults" {
					continue
				}
				if args, ok := index.Index.(*ast.Ident); ok {
					byArgs[args.Name] = spec.Name.Name
				}
			}
			return true
		})
	}
	return byArgs
}

// methodDeclsByType indexes every method body by its receiver type, so the gate
// can follow a Work body one hop into a helper on the same worker.
func methodDeclsByType(files []*ast.File) map[string]map[string]*ast.FuncDecl {
	byType := map[string]map[string]*ast.FuncDecl{}
	for _, file := range files {
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			recv := receiverTypeName(fn)
			if recv == "" {
				continue
			}
			if byType[recv] == nil {
				byType[recv] = map[string]*ast.FuncDecl{}
			}
			byType[recv][fn.Name.Name] = fn
		}
	}
	return byType
}

// dispatchBodies is Work's body plus the bodies of the helpers on the SAME
// worker type that it calls. One hop, and only through the receiver, because
// that is the extent to which "what this dispatcher itself does" is resolvable
// without type information — embedReindexWorker.fanOut is why the hop exists at
// all, and a deeper walk would be a reachability analysis this gate is
// deliberately not.
func dispatchBodies(work *ast.FuncDecl, methods map[string]*ast.FuncDecl) []*ast.BlockStmt {
	bodies := []*ast.BlockStmt{work.Body}
	if work.Recv == nil || len(work.Recv.List[0].Names) == 0 {
		// An unnamed receiver can call no helper on itself.
		return bodies
	}
	recv := work.Recv.List[0].Names[0].Name
	seen := map[string]bool{work.Name.Name: true}
	ast.Inspect(work.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		if id, ok := sel.X.(*ast.Ident); !ok || id.Name != recv {
			return true
		}
		helper, ok := methods[sel.Sel.Name]
		if !ok || seen[sel.Sel.Name] {
			return true
		}
		seen[sel.Sel.Name] = true
		bodies = append(bodies, helper.Body)
		return true
	})
	return bodies
}

// callNames returns the plain function names and the selector method names
// called anywhere in bodies.
func callNames(bodies []*ast.BlockStmt) (plain map[string]bool, selected map[string]bool) {
	plain, selected = map[string]bool{}, map[string]bool{}
	for _, body := range bodies {
		ast.Inspect(body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			switch fun := call.Fun.(type) {
			case *ast.Ident:
				plain[fun.Name] = true
			case *ast.SelectorExpr:
				selected[fun.Sel.Name] = true
			case *ast.IndexExpr:
				// river.ClientFromContext[pgx.Tx](ctx) — a generic
				// instantiation, whose callee is the index expression.
				if sel, ok := fun.X.(*ast.SelectorExpr); ok {
					selected[sel.Sel.Name] = true
				}
			}
			return true
		})
	}
	return plain, selected
}

// fansOut reports whether bodies contain a call from the sanctioned fan-out set.
func fansOut(bodies []*ast.BlockStmt) bool {
	plain, selected := callNames(bodies)
	for _, helper := range fanOutHelpers {
		if plain[helper] {
			return true
		}
	}
	resolved := false
	for name := range selected {
		if strings.HasPrefix(name, riverClientResolverPrefix) {
			resolved = true
			break
		}
	}
	if !resolved {
		return false
	}
	for _, method := range riverInsertMethods {
		if selected[method] {
			return true
		}
	}
	return false
}

// tenantWriteIn names the write verb of a SQL write statement, or "" when the
// literal is not one. Only string LITERALS are inspected: these files discuss
// inserts and updates in prose constantly, and a gate that read comments would
// fire on every one of them.
func tenantWriteIn(literal string) string {
	sql := strings.ToUpper(strings.Join(strings.Fields(literal), " "))
	// A locking read and an upsert's conflict clause both spell UPDATE without
	// being an UPDATE statement; a genuine upsert still trips INSERT INTO.
	sql = strings.ReplaceAll(sql, "FOR UPDATE", "")
	sql = strings.ReplaceAll(sql, "DO UPDATE", "")
	switch {
	case strings.Contains(sql, "INSERT INTO "):
		return "INSERT"
	case strings.Contains(sql, "DELETE FROM "):
		return "DELETE"
	case strings.Contains(sql, "TRUNCATE "):
		return "TRUNCATE"
	case strings.Contains(sql, "UPDATE ") && strings.Contains(sql, " SET "):
		// SET is what separates an UPDATE statement from the word "update" in
		// an error message.
		return "UPDATE"
	}
	return ""
}

// writesIn returns the write verbs of every SQL write statement in bodies.
func writesIn(bodies []*ast.BlockStmt) []string {
	var verbs []string
	for _, body := range bodies {
		ast.Inspect(body, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			if verb := tenantWriteIn(lit.Value); verb != "" {
				verbs = append(verbs, verb)
			}
			return true
		})
	}
	return verbs
}

// analyzeFleetWide resolves every FleetWide args type to its worker's Work
// method and reports what that dispatcher does. orphans are the marked args
// types no worker works — a dispatcher nothing runs.
func analyzeFleetWide(fset *token.FileSet, files []*ast.File) (dispatchers []fleetWideDispatcher, orphans []string) {
	marked := fleetWideArgsTypes(files)
	workers := workerByArgs(files)
	methods := methodDeclsByType(files)
	for args := range marked {
		worker, ok := workers[args]
		if !ok {
			orphans = append(orphans, args)
			continue
		}
		work, ok := methods[worker]["Work"]
		if !ok {
			orphans = append(orphans, args)
			continue
		}
		bodies := dispatchBodies(work, methods[worker])
		dispatchers = append(dispatchers, fleetWideDispatcher{
			args:    args,
			worker:  worker,
			pos:     fset.Position(work.Pos()),
			fansOut: fansOut(bodies),
			writes:  writesIn(bodies),
		})
	}
	// Map iteration order is randomized, so findings would arrive in a different
	// order every run — a diff between two runs of a red gate would be noise
	// rather than signal. Report in source order, as the neighbouring gates do.
	sort.Slice(dispatchers, func(i, j int) bool {
		if dispatchers[i].pos.Filename != dispatchers[j].pos.Filename {
			return dispatchers[i].pos.Filename < dispatchers[j].pos.Filename
		}
		return dispatchers[i].pos.Line < dispatchers[j].pos.Line
	})
	sort.Strings(orphans)
	return dispatchers, orphans
}

// checkFleetWideDispatchers runs the gate over one directory.
func checkFleetWideDispatchers(t *testing.T, dir string) {
	t.Helper()
	fset, files := parseGoFilesUnder(t, dir)
	dispatchers, orphans := analyzeFleetWide(fset, files)
	for _, args := range orphans {
		t.Errorf("%s declares FleetWide() but no worker embeds river.WorkerDefaults[%s] with a Work method: a dispatcher nothing runs enqueues nothing, and every tenant it was to fan out to is silently never swept.", args, args)
	}
	for _, d := range dispatchers {
		if !d.fansOut {
			t.Errorf("%s:%d: %s works FleetWide args %s but never fans out. A dispatcher enqueues one workspace-scoped job per tenant, through dispatchPerWorkspace, dispatchWith, or the River client's own Insert. If it does tenant work instead, it is WorkspaceScoped and its args must carry the workspace.",
				d.pos.Filename, d.pos.Line, d.worker, d.args)
		}
		for _, verb := range d.writes {
			t.Errorf("%s:%d: %s works FleetWide args %s and issues a tenant write (%s). A dispatcher may read to discover work; the write belongs to the workspace job, which succeeds or fails as its own row (jobs.FleetWide). Move the write into the workspace worker.",
				d.pos.Filename, d.pos.Line, d.worker, d.args, verb)
		}
	}
	if len(dispatchers) < fleetWideDispatcherFloor {
		t.Fatalf("resolved only %d FleetWide dispatchers, expected at least %d — the args→worker association matched almost nothing and this gate would pass vacuously", len(dispatchers), fleetWideDispatcherFloor)
	}
}

// TestEveryFleetWideJobOnlyDispatches is the gate. See the file comment for the
// allowlist and what it deliberately does not prove.
func TestEveryFleetWideJobOnlyDispatches(t *testing.T) {
	checkFleetWideDispatchers(t, filepath.Join("internal", "compose"))
}
