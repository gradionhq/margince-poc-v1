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
//  2. A direct River insert: Insert, InsertMany or InsertManyTx, in a body that
//     also resolves the River client (river.ClientFromContext). The pairing is
//     what keeps the method names from matching any store's own Insert.
//
// A dispatcher that fans out some third way is a finding on purpose: adding a
// third spelling is then a deliberate act with a reviewer attached.
//
// # Reads are fine; writes are not
//
// Several sanctioned dispatchers READ tenant tables — the due-scans that find
// which connections, builds or workspaces have work — and jobs.FleetWide
// expressly permits that. So the prohibition is on WRITES, scoped to what is
// honestly visible: a SQL write STATEMENT in the dispatcher's own body or in a
// helper method on the same worker type that its Work calls. A write behind a
// store method is out of reach here and is left to jobbinding_test.go and the
// RLS lane, which catch an unbound tenant write wherever it lives.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
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
// a fan-out only alongside riverClientResolver, so a store method that happens
// to be called Insert cannot satisfy this gate.
var riverInsertMethods = []string{"Insert", "InsertMany", "InsertManyTx"}

// riverClientResolver is how a Work body gets hold of the River client it
// inserts through.
const riverClientResolver = "ClientFromContext"

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
	if !selected[riverClientResolver] {
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

// parseFleetWideSource parses one synthetic compose file.
func parseFleetWideSource(t *testing.T, src string) (*token.FileSet, []*ast.File) {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "synthetic.go", src, parser.ParseComments)
	if err != nil {
		t.Fatalf("parsing the synthetic source: %v", err)
	}
	return fset, []*ast.File{file}
}

// TestTheFleetWideGateAcceptsEveryDispatchShapeInTheTree pins the gate against
// its own false positives. A gate that rejected a legitimate dispatcher would
// be fixed by weakening it, and the weakening is what the next sweep loop walks
// back in through — so every shape the tree actually uses is a test.
func TestTheFleetWideGateAcceptsEveryDispatchShapeInTheTree(t *testing.T) {
	shapes := map[string]string{
		"dispatchPerWorkspace over the live fleet": `
			func (w *sweepWorker) Work(ctx context.Context, _ *river.Job[SweepArgs]) error {
				return jobs.FaultContext(ctx, dispatchPerWorkspace(ctx, w.pool,
					workspaceSweepOpts(river.QueueDefault, sweepWorkspaceMaxAttempts),
					func(ws ids.UUID) river.JobArgs { return SweepWorkspaceArgs{Workspace: ws} }))
			}`,
		"dispatchWith over an archived-inclusive enumeration": `
			func (w *sweepWorker) Work(ctx context.Context, _ *river.Job[SweepArgs]) error {
				workspaces, err := enumerateEveryWorkspace(ctx, w.pool)
				if err != nil {
					return jobs.FaultContext(ctx, err)
				}
				return jobs.FaultContext(ctx, dispatchWith(ctx, workspaces, clientInsertMany(ctx),
					workspaceSweepOpts(river.QueueDefault, sweepWorkspaceMaxAttempts),
					func(ws ids.UUID) river.JobArgs { return SweepWorkspaceArgs{Workspace: ws} }))
			}`,
		"a due-scan fanning out one client.Insert per due row": `
			func (w *sweepWorker) Work(ctx context.Context, _ *river.Job[SweepArgs]) error {
				due, enumErr := w.registry.DueConnections(ctx)
				if len(due) == 0 {
					return jobs.FaultContext(ctx, enumErr)
				}
				client := river.ClientFromContext[pgx.Tx](ctx)
				for _, d := range due {
					if _, err := client.Insert(ctx, SweepWorkspaceArgs{Workspace: d.Workspace}, nil); err != nil {
						enumErr = errors.Join(enumErr, err)
					}
				}
				return jobs.FaultContext(ctx, enumErr)
			}`,
		"a helper on the same worker that holds the fan-out": `
			func (w *sweepWorker) Work(ctx context.Context, job *river.Job[SweepArgs]) error {
				return jobs.FaultContext(ctx, w.fanOut(ctx, job.Args))
			}

			func (w *sweepWorker) fanOut(ctx context.Context, args SweepArgs) error {
				workspaces, err := enumerateWorkspaces(ctx, w.pool)
				if err != nil {
					return err
				}
				return w.store.SeedFleet(ctx, args.Run, func(tx pgx.Tx) error {
					return dispatchWith(ctx, workspaces, txInsertMany(tx),
						workspaceSweepOpts(aiCaptureQueue, embedReindexMaxAttempts),
						func(ws ids.UUID) river.JobArgs { return SweepWorkspaceArgs{Workspace: ws} })
				})
			}`,
		"a due-scan that reads a tenant table before fanning out": `
			func (w *sweepWorker) Work(ctx context.Context, _ *river.Job[SweepArgs]) error {
				rows, err := w.pool.Query(ctx, ` + "`SELECT workspace_id FROM connection WHERE next_sweep_at <= now() FOR UPDATE`" + `)
				if err != nil {
					return jobs.FaultContext(ctx, err)
				}
				defer rows.Close()
				return jobs.FaultContext(ctx, dispatchPerWorkspace(ctx, w.pool,
					workspaceSweepOpts(river.QueueDefault, sweepWorkspaceMaxAttempts),
					func(ws ids.UUID) river.JobArgs { return SweepWorkspaceArgs{Workspace: ws} }))
			}`,
	}
	for name, work := range shapes {
		t.Run(name, func(t *testing.T) {
			fset, files := parseFleetWideSource(t, fleetWideFixture(work))
			dispatchers, orphans := analyzeFleetWide(fset, files)
			if len(orphans) != 0 {
				t.Fatalf("the args→worker association failed: %v", orphans)
			}
			if len(dispatchers) != 1 {
				t.Fatalf("resolved %d dispatchers, want exactly 1", len(dispatchers))
			}
			if !dispatchers[0].fansOut {
				t.Errorf("the gate does not recognize this dispatch shape as a fan-out; it is in the tree and must be in the allowlist")
			}
			if len(dispatchers[0].writes) != 0 {
				t.Errorf("the gate reads a tenant write into a dispatcher that makes none: %v", dispatchers[0].writes)
			}
		})
	}
}

// TestTheFleetWideGateRejectsADispatcherThatDoesTenantWork is the falsification
// the gate exists for: both plants declare FleetWide, both would keep a null
// `args->>'workspace_id'`, and both do a tenant's work inside one row.
func TestTheFleetWideGateRejectsADispatcherThatDoesTenantWork(t *testing.T) {
	plants := map[string]string{
		"a write in the dispatcher's own body": `
			func (w *sweepWorker) Work(ctx context.Context, _ *river.Job[SweepArgs]) error {
				for _, ws := range w.fleet {
					if _, err := w.pool.Exec(ctx, ` + "`UPDATE deal SET stage = 'stale' WHERE workspace_id = $1`" + `, ws); err != nil {
						w.log.WarnContext(ctx, "sweep failed", "workspace", ws)
					}
				}
				return nil
			}`,
		"a write behind a helper on the same worker": `
			func (w *sweepWorker) Work(ctx context.Context, _ *river.Job[SweepArgs]) error {
				return jobs.FaultContext(ctx, w.sweepEveryTenant(ctx))
			}

			func (w *sweepWorker) sweepEveryTenant(ctx context.Context) error {
				_, err := w.pool.Exec(ctx, ` + "`DELETE FROM idempotency_key WHERE expires_at < now()`" + `)
				return err
			}`,
	}
	for name, work := range plants {
		t.Run(name, func(t *testing.T) {
			fset, files := parseFleetWideSource(t, fleetWideFixture(work))
			dispatchers, _ := analyzeFleetWide(fset, files)
			if len(dispatchers) != 1 {
				t.Fatalf("resolved %d dispatchers, want exactly 1", len(dispatchers))
			}
			if len(dispatchers[0].writes) == 0 {
				t.Errorf("the gate sees no tenant write in a dispatcher that issues one — it would let this shape back into the tree")
			}
			if dispatchers[0].fansOut {
				t.Errorf("the gate reads a fan-out into a dispatcher that enqueues nothing")
			}
		})
	}
}

// fleetWideFixture wraps one Work method in the smallest file that declares a
// FleetWide args type and a worker embedding river.WorkerDefaults for it, so a
// shape can be tested without a package to compile it in.
func fleetWideFixture(work string) string {
	return `package compose

type SweepArgs struct{}

func (SweepArgs) Kind() string { return "sweep" }
func (SweepArgs) FleetWide()   {}

type sweepWorker struct {
	river.WorkerDefaults[SweepArgs]
	pool *pgxpool.Pool
}
` + work + "\n"
}
