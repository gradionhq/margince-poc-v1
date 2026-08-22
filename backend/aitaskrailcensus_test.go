// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package backendarch

// Every AI task this build can run reports into the AI-activity projection.
//
// The rail's claim is that it says what the AI is doing for one person. A task
// that reports nothing is AI work the product performed and then denied — and
// for seventeen of nineteen shipped tasks that was the state of the tree, with
// every gate green. The gates in place could not see it: they compared the
// contract's kind enum against the two producers already wired, which is a
// question whose answer can never be an absentee.
//
// So this gate asks the other question. It starts from the CONTRACT's task
// table — the one place a new AI task must be declared — and requires each task
// to name the source that reports it. Derived from the generated table rather
// than a list here, because a list is exactly what went stale to produce the
// defect.
//
// It lives at the root because no module can ask it: `ai` owns the task
// vocabulary, the carriers are its siblings, and a module never imports one.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/modules/activities"
	"github.com/gradionhq/margince/backend/internal/modules/agents/runner"
	"github.com/gradionhq/margince/backend/internal/modules/ai"
)

// gatekit:fixture the carriers' own source constants, read from the packages that emit them
//
// carrierSources is every non-router source in the registry, mapped to the
// constant the carrier really emits. The VALUE is what makes this more than a
// spelling check: it is read from the emitting package, so a carrier that
// renames or retires its source breaks the pairing instead of leaving the
// router silenced in favour of a source nothing writes.
var carrierSources = map[string]string{
	"agent_runner":          runner.ActivitySource,
	"attachment_extraction": activities.ExtractionActivitySource,
}

func TestEveryAITaskNamesTheSourceThatReportsIt(t *testing.T) {
	tasks := everyRoutableTask(t)
	if len(tasks) == 0 {
		t.Fatal("derived no tasks at all; this gate would pass vacuously")
	}
	for _, task := range tasks {
		if ai.RailOwner(task) == "" {
			t.Errorf("task %q names no source that reports it, so its work never reaches ai_task_run and "+
				"the rail denies AI the product really ran. Answer it in ai.railOwners: ai.SourceRouter when "+
				"the router's post-hoc report is the whole truth, or a carrier's source when a durable row "+
				"can say queued and running", task)
			continue
		}
		if ai.RailOwner(task) == ai.SourceNoOccurrence && strings.TrimSpace(ai.NoOccurrenceReason(task)) == "" {
			t.Errorf("task %q reports no occurrence of its own and says nothing about why. "+
				"\"Reported by nobody\" is one keystroke from the silence this registry exists to end, "+
				"so it costs a sentence in ai.railNoOccurrenceReasons that somebody can defend — and if "+
				"the real reason is that no reader wants to see it, that is the CLIENT's decision, not this map's", task)
		}
	}
}

// everyRoutableTask is every ai.Task this build can route, contract-declared or
// not.
//
// The generated table alone is NOT the set, and that gap is exactly the shape
// of the defect this whole gate exists for: `embeddings` is declared by hand in
// router.go, routes on the embed lane, writes ai_call rows — and is invisible
// to api/ai-tasks.yaml, so a census that read only the contract would report
// full coverage over a task it had never heard of.
//
// So the hand-declared half is read from the SOURCE, by walking the ai package
// for `Xxx Task = "..."` constants. A list here would need maintaining, and the
// maintenance is the thing that gets forgotten.
func everyRoutableTask(t *testing.T) []ai.Task {
	t.Helper()
	seen := map[string]bool{}
	out := []ai.Task{}
	for _, task := range ai.AllTasks() {
		seen[string(task)] = true
		out = append(out, task)
	}
	contractDeclared := len(out)
	for _, name := range taskConstantsDeclaredInSource(t) {
		if seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, ai.Task(name))
	}
	if contractDeclared == 0 {
		t.Fatal("derived no tasks from the generated contract table; the source walk alone would understate the set")
	}
	return out
}

// taskConstantsDeclaredInSource reads every `Task = "..."` constant value out of
// the ai package's own files, generated and hand-written alike.
func taskConstantsDeclaredInSource(t *testing.T) []string {
	t.Helper()
	dir := filepath.Join("internal", "modules", "ai")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("reading the ai package: %v", err)
	}
	var names []string
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		file, parseErr := parser.ParseFile(token.NewFileSet(), filepath.Join(dir, entry.Name()), nil, 0)
		if parseErr != nil {
			t.Fatalf("parsing %s: %v", entry.Name(), parseErr)
		}
		names = append(names, taskConstantValues(file)...)
	}
	if len(names) == 0 {
		t.Fatal("found no Task constants in the ai package source; the walk is looking in the wrong place")
	}
	return names
}

// taskConstantValues pulls the string literals off one file's `= Task("x")` or
// `Xxx Task = "x"` constant declarations.
func taskConstantValues(file *ast.File) []string {
	var out []string
	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.CONST {
			continue
		}
		for _, spec := range gen.Specs {
			value, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			ident, ok := value.Type.(*ast.Ident)
			if !ok || ident.Name != "Task" {
				continue
			}
			for _, expr := range value.Values {
				lit, ok := expr.(*ast.BasicLit)
				if ok && lit.Kind == token.STRING {
					out = append(out, strings.Trim(lit.Value, `"`))
				}
			}
		}
	}
	return out
}

// The reverse: a registry entry for a task the contract dropped is a silencing
// nobody can act on, and it would keep the router quiet about a name that no
// longer routes anywhere.
func TestTheRailRegistryNamesNoTaskTheContractDropped(t *testing.T) {
	declared := map[string]bool{}
	for _, task := range everyRoutableTask(t) {
		declared[string(task)] = true
	}
	for task := range ai.RailOwners() {
		if !declared[task] {
			t.Errorf("the rail registry answers for task %q and api/ai-tasks.yaml does not declare it — "+
				"either the task was retired and its entry kept, or the entry is a typo that silently "+
				"leaves the real task unanswered", task)
		}
	}
}

// A carrier override silences the router for that task. If the source it names
// is one no module writes, the task is reported by NOBODY — which reads exactly
// like a task that is wired.
func TestEveryCarrierOverrideNamesASourceThatIsReallyEmitted(t *testing.T) {
	for task, source := range ai.RailOwners() {
		if source == ai.SourceRouter || source == ai.SourceNoOccurrence {
			continue
		}
		emitted, known := carrierSources[source]
		if !known {
			t.Errorf("task %q is reported by carrier source %q, and this gate knows no emitter for it — "+
				"the router is silenced for a task nothing announces. Add the carrier's exported source "+
				"constant to carrierSources, or point the task at ai.SourceRouter", task, source)
			continue
		}
		if emitted != source {
			t.Errorf("task %q is registered against source %q but its carrier emits %q — the projection "+
				"would hold the occurrence under a name the registry does not know", task, source, emitted)
		}
	}
}
