// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package backendarch

// Every consumer group the catalog declares is subscribed by some process
// role — or is a reserved placeholder that says so.
//
// A group with no subscriber is the quietest failure the bus has. Nothing
// errors: the relay ships every event onto the stream, the group simply never
// reads it, and the feature it was meant to drive is not broken so much as
// absent. cmd/worker's runSubscriber already refuses a name NO catalog group
// answers to; this is the other direction, which nothing checked — and the
// other direction is the one that costs a working feature rather than a boot.
//
// Both sides are derived: the declared set from events.Groups(), the subscribed
// set from the string literals the role wiring actually names. A literal is the
// right unit here because there is more than one way to start a lane — worker's
// runSubscriber takes the name, api's inline webhook delivery matches on it —
// and a walk keyed on one of those helpers would report the other's group as
// unsubscribed.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/gatekit"
	kevents "github.com/gradionhq/margince/backend/internal/shared/kernel/events"
)

// reservedGroups are declared with no subscriber ON PURPOSE — placeholders the
// catalog holds so the name and its stream set are settled before the consumer
// that will read them exists. docs/explanation/write-backbone.md §5 carries the
// same four in its status table, and an entry here is a claim about that table
// rather than a judgement made in this file.
var reservedGroups = gatekit.Waive(map[string]string{
	"cg:capture":      "reserved placeholder: declared with no subscriber, recorded as such in docs/explanation/write-backbone.md §5",
	"cg:flow-bridge":  "reserved placeholder: the same",
	"cg:read-model":   "reserved placeholder for read-model projections: the same",
	"cg:audit-stream": "reserved placeholder for the agent-action audit slice: the same",
})

func TestEveryDeclaredConsumerGroupIsSubscribedSomewhere(t *testing.T) {
	subscribed := groupNamesTheRolesName(t)
	// A walk that found no group at all reports exactly like a tree where every
	// group has a lane, which is the failure this gate is closing elsewhere.
	if len(subscribed) == 0 {
		t.Fatal("found no consumer-group name in the role wiring at all; this gate would pass vacuously")
	}
	t.Logf("the role wiring names %d consumer group(s)", len(subscribed))
	defer reservedGroups.AssertAllMatched(t)

	for _, g := range kevents.Groups() {
		if subscribed[g.Name] != "" || reservedGroups.Waived(t, g.Name) {
			continue
		}
		t.Errorf("consumer group %q is declared in the catalog and subscribed by no process role — "+
			"the relay will ship every event on %v onto a stream nothing reads, and nothing anywhere "+
			"will say so. Start a lane for it, or record it in reservedGroups with the reason it is "+
			"declared ahead of its consumer", g.Name, g.Streams)
	}
}

// groupNamesTheRolesName maps each cg: literal in the role wiring to where it
// was found, so a finding can name the file rather than leave the reader to
// grep for a string.
func groupNamesTheRolesName(t *testing.T) map[string]string {
	t.Helper()
	out := map[string]string{}
	fset := token.NewFileSet()
	for _, root := range []string{"cmd", "internal/compose"} {
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") ||
				strings.HasSuffix(path, "_test.go") || strings.HasSuffix(path, "_gen.go") {
				return err
			}
			file, parseErr := parser.ParseFile(fset, path, nil, 0)
			if parseErr != nil {
				return parseErr
			}
			collectGroupLiterals(file, fset, filepath.ToSlash(path), out)
			return nil
		})
		if err != nil {
			t.Fatalf("walking %s for consumer-group names: %v", root, err)
		}
	}
	return out
}

// collectGroupLiterals records every "cg:…" string literal in one file.
func collectGroupLiterals(file *ast.File, fset *token.FileSet, path string, out map[string]string) {
	ast.Inspect(file, func(n ast.Node) bool {
		lit, ok := n.(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return true
		}
		value, err := strconv.Unquote(lit.Value)
		if err != nil || !strings.HasPrefix(value, "cg:") {
			return true
		}
		if _, seen := out[value]; !seen {
			out[value] = path + ":" + strconv.Itoa(fset.Position(lit.Pos()).Line)
		}
		return true
	})
}

// TestTheLaneCensusReadsTheWholeCatalog is the floor: the gate above passes
// vacuously if Groups() ever answers a set this file cannot see, and a gate
// whose subject list has collapsed reports exactly like a tree with no gap.
func TestTheLaneCensusReadsTheWholeCatalog(t *testing.T) {
	names := make([]string, 0, len(kevents.Groups()))
	for _, g := range kevents.Groups() {
		names = append(names, g.Name)
	}
	// Two of these are load-bearing in opposite directions: cg:ai-activity is
	// the newest lane and cg:capture is the oldest reserved one, so a census
	// that lost either end would fail here rather than pass above.
	for _, want := range []string{"cg:ai-activity", "cg:capture", "cg:workflows"} {
		if !slices.Contains(names, want) {
			t.Fatalf("the catalog no longer declares %q; the lane census is asserting over %v", want, slices.Sorted(slices.Values(names)))
		}
	}
}
