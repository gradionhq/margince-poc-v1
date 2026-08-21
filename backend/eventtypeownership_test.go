// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package backendarch

// One module owns an event type.
//
// The rule, written out rather than cited: a bounded capability owns the names
// it publishes under, so exactly one module decides what a given event type
// MEANS. Two modules emitting one type is how a subscriber ends up receiving
// the same name for two different facts — and nothing in the envelope tells it
// which happened, because the type IS the discriminator. The tables have a gate
// saying this (TestEveryPackageOnlyWritesTablesItOwns); the event types did not.
//
// Sharing is not always wrong, and the ratified set below is the proof: an
// overlay write-back announces the NATIVE module's event on purpose, because a
// subscriber to person.updated must hear about a person changing however the
// write arrived. What this gate refuses is a NEW sharer arriving unnoticed.
//
// It walks composite literals of the generated payload structs rather than the
// emit calls, which is what makes it sound over both outbox writers (storekit's
// and the hand-rolled INSERT in approvals) and over the six helpers that return
// one of several payloads by branch.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/shared/gatekit"
)

// payloadEventTypes maps each generated payload struct to the event type it
// declares, read from the EventType() methods in the contract package.
//
// The METHOD and not the name is the test, and the difference is not academic:
// the generated file declares 94 PublicEvent* types and only 83 of them are
// event payloads. The rest are nested field structs —
// PublicEventActivityChangedFields among them — and a name-prefix census counts
// one of those as an extra shared type emitted by two modules, a duplicate that
// does not exist.
//
// BOTH generated families are walked. The public one is the webhook contract;
// the internal one carries the payloads that ride the bus without being
// subscribable, and one module owns an event type whether or not an outside
// consumer may name it. Walking the public file alone was right when it was the
// only family and became a hole the moment a second one existed.
func payloadEventTypes(t *testing.T) map[string]string {
	t.Helper()
	generated := []string{
		"internal/contracts/publicevents_gen.go",
		"internal/contracts/internalevents_gen.go",
	}
	out := map[string]string{}
	for _, path := range generated {
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			t.Fatalf("reading the generated payloads to derive their event types: %v", err)
		}
		collectEventTypeMethods(file, out)
	}
	assertEveryPublicTypeWasDerived(t, out, generated)
	assertEveryInternalTypeWasDerived(t, out)
	return out
}

// collectEventTypeMethods reads one generated file's EventType() methods into
// the shared census.
func collectEventTypeMethods(file *ast.File, out map[string]string) {
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "EventType" || fn.Recv == nil || len(fn.Recv.List) != 1 {
			continue
		}
		ident, ok := fn.Recv.List[0].Type.(*ast.Ident)
		if !ok || fn.Body == nil || len(fn.Body.List) != 1 {
			continue
		}
		ret, ok := fn.Body.List[0].(*ast.ReturnStmt)
		if !ok || len(ret.Results) != 1 {
			continue
		}
		lit, ok := ret.Results[0].(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			continue
		}
		if literal, err := strconv.Unquote(lit.Value); err == nil {
			out[ident.Name] = literal
		}
	}
}

// assertEveryPublicTypeWasDerived is the floor, and it has to be a SET
// comparison rather than a count.
//
// A bare len(out) == 0 catches only a derivation that collapses completely.
// One that quietly loses a few — a pointer receiver, a two-statement body, a
// return of a named constant instead of a literal, all of which are generator
// shape changes this gate is meant to survive — drops those payloads out of
// BOTH gates: they vanish from the emit census and from the orphan sweep at
// the same time, so nothing is left to notice. PublicEventVersions is
// generated from the same contract and keyed on the event type, so a
// disagreement between the two is exactly the collapse.
func assertEveryPublicTypeWasDerived(t *testing.T, out map[string]string, generated []string) {
	t.Helper()
	missing := make([]string, 0)
	for eventType := range crmcontracts.PublicEventVersions {
		if !slices.Contains(slices.Collect(maps.Values(out)), eventType) {
			missing = append(missing, eventType)
		}
	}
	if len(missing) > 0 {
		t.Fatalf("derived %d event types from %v but PublicEventVersions carries %d; %v have a "+
			"contract entry and no EventType() this walk could read. The walk has stopped seeing "+
			"payloads, so both gates would pass over every type it lost",
			len(out), generated, len(crmcontracts.PublicEventVersions), slices.Sorted(slices.Values(missing)))
	}
}

// assertEveryInternalTypeWasDerived is the same floor for the internal family,
// and it cannot lean on the same source.
//
// gen-payloads emits no versions map for that family on purpose — every family
// compiles into ONE Go package, and only the public contract has gates that
// read one — so there is no generated set to compare against. The CONTRACT is
// the set instead: every schema in api/internal-events.yaml carrying an
// x-event-type must have produced an EventType() this walk could read. Without
// it a generator shape change drops the whole internal family out of both gates
// silently, which is the collapse the public floor exists to catch.
func assertEveryInternalTypeWasDerived(t *testing.T, out map[string]string) {
	t.Helper()
	declared := internalContractEventTypes(t)
	if len(declared) == 0 {
		t.Fatal("api/internal-events.yaml declares no x-event-type at all; this floor would pass vacuously")
	}
	derived := slices.Collect(maps.Values(out))
	missing := make([]string, 0)
	for _, eventType := range declared {
		if !slices.Contains(derived, eventType) {
			missing = append(missing, eventType)
		}
	}
	if len(missing) > 0 {
		t.Fatalf("api/internal-events.yaml declares %v with no EventType() this walk could read; "+
			"the internal family has dropped out of the ownership census and the orphan sweep at once",
			slices.Sorted(slices.Values(missing)))
	}
}

// internalContractEventTypes reads the internal family's event types from the
// contract itself, which is the only place that set is written down.
func internalContractEventTypes(t *testing.T) []string {
	t.Helper()
	// Decoded loosely rather than into a tagged struct: the key is an OpenAPI
	// extension, `x-event-type`, and a yaml tag naming it fails the repo's
	// snake_case tag rule — a rule about OUR wire shapes, which this is not.
	var doc struct {
		Components struct {
			Schemas map[string]map[string]any `yaml:"schemas"`
		} `yaml:"components"`
	}
	raw, err := os.ReadFile("api/internal-events.yaml")
	if err != nil {
		t.Fatalf("reading the internal payload contract: %v", err)
	}
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("parsing the internal payload contract: %v", err)
	}
	out := make([]string, 0, len(doc.Components.Schemas))
	for _, schema := range doc.Components.Schemas {
		if eventType, ok := schema["x-event-type"].(string); ok && eventType != "" {
			out = append(out, eventType)
		}
	}
	return out
}

// emitSite is one place a module builds an event payload.
type emitSite struct {
	pos    string
	module string
}

// collectEmitSites walks the hand-written module and compose sources and
// records every payload struct literal under its owning module.
//
// Composite literals rather than emit CALLS, deliberately. There are two outbox
// writers — storekit's, and a hand-rolled INSERT in approvals — so a walk keyed
// on storekit.Emit* would miss the whole approval family and report four live
// types as never emitted. Six helpers also return one of several payloads by
// branch; the literal is inside the helper either way.
func collectEmitSites(t *testing.T) map[string][]emitSite {
	t.Helper()
	types := payloadEventTypes(t)
	sites := map[string][]emitSite{} // event type → where it is built
	fset := token.NewFileSet()
	// Every hand-written tree that could hold a payload literal, and the list is
	// the whole list rather than the obvious half: cmd and pkg can import
	// internal/contracts as legally as internal can, so an emitter added under
	// cmd/api defeats the rule while being invisible to a modules-only walk.
	for _, root := range []string{"internal", "cmd", "pkg", "../extensions"} {
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") ||
				strings.HasSuffix(path, "_test.go") || strings.HasSuffix(path, "_gen.go") ||
				isIntegrationTagged(path) {
				return err
			}
			path = filepath.ToSlash(path)
			file, err := parser.ParseFile(fset, path, nil, 0)
			if err != nil {
				return err
			}
			// The qualifier the contracts package is reachable under IN THIS FILE.
			// Matching only a selector's terminal name would count an unrelated
			// package's PublicEventPersonUpdated as the CRM payload, and a file
			// that does not import contracts at all can hold no emit.
			qualifier, imports := contractsQualifier(file)
			if !imports {
				return nil
			}
			module := owningDir(filepath.ToSlash(filepath.Dir(path)))
			ast.Inspect(file, func(n ast.Node) bool {
				// A SelectorExpr covers `pkg.Type{…}` and, via the type of a
				// slice or map literal, the ELIDED inner `{…}` elements whose own
				// Type node is nil. Without the second, a module batch-building
				// payloads from a slice literal is invisible here.
				name, ok := payloadTypeName(n, qualifier)
				if !ok {
					return true
				}
				if eventType, ok := types[name]; ok {
					sites[eventType] = append(sites[eventType],
						emitSite{pos: fset.Position(n.Pos()).String(), module: module})
				}
				return true
			})
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	return sites
}

// modulesEmitting answers the distinct modules that build one event type.
func modulesEmitting(sites []emitSite) []string {
	seen := map[string]bool{}
	for _, site := range sites {
		seen[site.module] = true
	}
	return slices.Sorted(maps.Keys(seen))
}

// sharedEventTypes ratifies each MODULE that builds a shared event type, keyed
// "<event type> <- <module>".
//
// Keyed on the PAIR and not on the type alone, and that is not a stylistic
// choice — it was a hole. A type-keyed waiver ratifies the sharing once and
// then admits any number of further modules under the same entry: planting an
// emit of person.updated inside deals passed a type-keyed version of this gate
// silently, which is the failure this whole gate exists to refuse, reproduced
// inside its own exception list.
//
// Three structures account for every entry, and in all of them the second
// module announces a fact that IS the first module's fact. None is a second
// meaning for one name, which is what the rule protects.
var sharedEventTypes = gatekit.Waive(map[string]string{
	// Structure 1 — the overlay write-back announces the NATIVE module's event.
	// overlay/writeaudit.go switches on datasource.EntityRef and emits the
	// system-of-record type for the entity it just wrote. That is the point: a
	// subscriber to person.updated is subscribed to A PERSON CHANGING, and must
	// hear about one whether the write arrived natively or through the write-back.
	// An overlay.* type instead would make every consumer subscribe twice to
	// learn one fact, and would leak which path a write took into a contract
	// that deliberately does not say.
	"person.updated <- internal/modules/overlay":        "the write-back's update path: a person changed, and the overlay wrote it",
	"person.archived <- internal/modules/overlay":       "the write-back's archive path, one of the three archivable types",
	"organization.updated <- internal/modules/overlay":  "the write-back's update path",
	"organization.archived <- internal/modules/overlay": "the write-back's archive path",
	"deal.updated <- internal/modules/overlay":          "the write-back's update path",
	"deal.archived <- internal/modules/overlay":         "the write-back's archive path",
	"lead.updated <- internal/modules/overlay":          "the write-back's update path; lead is one of its five updatable types",
	"activity.updated <- internal/modules/overlay":      "the write-back's update path, and the one that NARROWS rather than passes through: activity.updated's changed_fields is a bounded typed key set where the other four carry open maps, so the patch is projected onto it in activityChangedFields",

	// The native side of those seven, listed so the pair map is complete and a
	// module losing its own event is as visible as one gaining somebody else's.
	"person.updated <- internal/modules/people":        "the record's own module, natively and for a relationship anchored on a person",
	"person.archived <- internal/modules/people":       "the record's own module",
	"organization.updated <- internal/modules/people":  "the record's own module, natively and for a relationship anchored on an organization",
	"organization.archived <- internal/modules/people": "the record's own module",
	"deal.updated <- internal/modules/deals":           "the record's own module",
	"deal.archived <- internal/modules/deals":          "the record's own module",
	"lead.updated <- internal/modules/people":          "the record's own module",
	"activity.updated <- internal/modules/activities":  "the record's own module",

	// Structure 2 — a relationship emits its ANCHOR's event.
	// people/relationshipUpdatedPayload wraps one delta in whichever anchor's
	// envelope the edge points at. An employment edge changing IS a change to
	// the person and to the organization it joins; there is no relationship.*
	// type, and inventing one would make every consumer of the anchor subscribe
	// to a second name to learn that their record moved.
	"deal.updated <- internal/modules/people":    "a relationship anchored on a deal moved, so the deal changed",
	"project.updated <- internal/modules/people": "a relationship anchored on a project moved, so the project changed — the same anchor rule",
	"project.updated <- internal/modules/deals":  "the record's own module: deals owns project",

	// Structure 3 — capture announces what it captured, as the RECORD's event.
	// The capture path creates real leads and real activities, so the type is
	// the record's own, with source_system set so a consumer can tell an
	// inferred record from one somebody typed. A capture.* type would announce
	// that a pipeline ran, which no consumer of the record wants.
	"lead.created <- internal/modules/people":          "a lead somebody created",
	"lead.created <- internal/modules/capture":         "a lead an inbound message created; source_system names where it came from. Both are a lead existing that did not before",
	"activity.captured <- internal/modules/activities": "an activity logged through the product",
	"activity.captured <- internal/modules/capture":    "an activity an inbound message produced, again with source_system. The verb is already `captured` for both — it is the record's arrival, not the pipeline's run",
})

func TestEveryEventTypeHasOneEmittingModule(t *testing.T) {
	sites := collectEmitSites(t)
	// A walk that found no emit at all reports exactly like a tree where every
	// type has one owner, which is the failure mode this gate is closing in a
	// different place.
	if len(sites) == 0 {
		t.Fatal("found no event payload literals at all; this gate would pass vacuously")
	}
	t.Logf("examined %d event types built across the module and compose trees", len(sites))
	// Armed after the vacuity fatal, for the reason spelled out in the orphan
	// gate below: a sweep deferred above it drowns the real message.
	defer sharedEventTypes.AssertAllMatched(t)

	for _, eventType := range slices.Sorted(maps.Keys(sites)) {
		modules := modulesEmitting(sites[eventType])
		if len(modules) < 2 {
			continue
		}
		for _, module := range modules {
			if sharedEventTypes.Waived(t, eventType+" <- "+module) {
				continue
			}
			t.Errorf("event type %q is built at %s, and by %d modules in all (%s) — one module owns "+
				"a type, so exactly one decides what the name MEANS. If %s is not that module, move "+
				"the emit into the one that is; if it IS, the new emitter is one of the others. "+
				"Ratify a correct sharing in sharedEventTypes[%q]",
				eventType, strings.Join(positionsFor(sites[eventType], module), ", "),
				len(modules), strings.Join(modules, ", "), module, eventType+" <- "+module)
		}
	}
}

// unemittedEventTypes are the payload types with NO emit site anywhere, each
// with the reason nothing publishes it.
//
// This set is also the gate's floor. The vacuity check above only catches a
// walk that finds NOTHING; a walk that quietly found half of what it should —
// a renamed payload package, a changed literal shape — would still report every
// remaining type as singly-owned and pass. Every type the walk stops seeing
// lands here instead, so a partial collapse fails on the entries it cannot
// explain rather than passing on the ones it still can.
var unemittedEventTypes = gatekit.Waive(map[string]string{
	"audit.appended":             "deliberate and documented in the contract: no emit site and none planned. It exists so the catalog is completely covered by a payload schema, never carrying a subscribable type with no contract",
	"deal.restored":              "documented in the contract as never emitted today — there is no restore path",
	"person.restored":            "the same, for the person restore path that does not exist",
	"pipeline.archived":          "documented in the contract as never emitted today — no archive path",
	"mirror.write_rejected":      "documented in the contract as never emitted today, reserved for the overlay write-back's refusal case",
	"conversation_claim.changed": "the odd one out, and filed rather than ratified quietly: unlike the four above, the contract does NOT mark this one unemitted — it describes it as published, because 'a correction is SHARED truth'. There is no correction path in people/conversationclaim.go to publish from; the module exposes RecordConversationClaim and nothing else. Waived here so the gate is green over a real state of the tree, not because the state is right",
})

// A payload type nothing emits is either deliberate or a gap, and the contract
// should say which.
//
// The four the contract already marks "Never emitted today" are the shape this
// is checking for: a type reserved so the catalog stays completely covered by
// schemas. What this refuses is a type that quietly stops being emitted — the
// contract keeps promising it, subscribers keep waiting, and nothing fails.
func TestEveryUnemittedEventTypeSaysWhyNothingEmitsIt(t *testing.T) {
	sites := collectEmitSites(t)
	// Armed after the walk, which can fatal. Deferred above it, the sweep runs on
	// the way out and buries the one true line under an entry per waiver, each
	// telling the reader to delete a correct one.
	defer unemittedEventTypes.AssertAllMatched(t)
	// Iterating the generated map rather than the derived values: it is keyed on
	// the event type, so it is deduped by construction, and payloadEventTypes has
	// already asserted the two agree.
	for _, eventType := range slices.Sorted(maps.Keys(crmcontracts.PublicEventVersions)) {
		if len(sites[eventType]) > 0 || unemittedEventTypes.Waived(t, eventType) {
			continue
		}
		t.Errorf("event type %q has a payload schema and no emit site anywhere — the contract keeps "+
			"promising it and no subscriber will ever receive one. Emit it, or record it in "+
			"unemittedEventTypes with the reason nothing does", eventType)
	}
}

// positionsFor answers where one module builds a given event type, so a finding
// names the file and line instead of leaving the reader to grep a module for a
// literal.
func positionsFor(sites []emitSite, module string) []string {
	var out []string
	for _, site := range sites {
		if site.module == module {
			out = append(out, site.pos)
		}
	}
	return slices.Sorted(slices.Values(out))
}

// payloadTypeName answers the payload struct a node CONSTRUCTS.
//
// The elided cases are the ones a naive walk misses: inside
// []crmcontracts.PublicEventX{{…}} the inner element's own Type node is nil and
// the name lives on the enclosing slice or map literal, so a module
// batch-building payloads that way would emit without this gate seeing it.
//
// What this still cannot see, named so the comment does not overclaim a second
// time: a payload built by ASSIGNMENT into a zero value, or through reflection
// or a generic builder. Those need dataflow, nothing in the tree does them, and
// a walk that guessed at them would misread the decode targets below.
//
// A bare `var p crmcontracts.PublicEventX` declaration is deliberately NOT one
// of them, and the tree says why: both such declarations here are DECODE
// targets, immediately json.Unmarshal-ed from an inbound envelope
// (compose/leadsla.go reading lead.sla_breached, compose/personautoenrich.go
// reading person.merged). Counting them made two consumers look like emitters
// and would have had this gate ratify a sharing that does not exist. A
// declaration cannot be told from a construction without dataflow, and here the
// false positives are real while the construction case is hypothetical.
func payloadTypeName(n ast.Node, qualifier string) (string, bool) {
	switch node := n.(type) {
	case *ast.CompositeLit:
		switch typ := node.Type.(type) {
		case *ast.SelectorExpr:
			return qualifiedName(typ, qualifier)
		case *ast.ArrayType:
			return qualifiedName(elementSelector(typ.Elt), qualifier)
		case *ast.MapType:
			return qualifiedName(elementSelector(typ.Value), qualifier)
		}
	case *ast.CallExpr:
		// new(crmcontracts.PublicEventX) — a pointer to the zero value, which
		// satisfies events.Payload exactly as &T{} does.
		if fn, ok := node.Fun.(*ast.Ident); ok && fn.Name == "new" && len(node.Args) == 1 {
			if sel, ok := node.Args[0].(*ast.SelectorExpr); ok {
				return qualifiedName(sel, qualifier)
			}
		}
	}
	return "", false
}

// elementSelector unwraps the pointer a slice or map of POINTERS carries, so
// []*crmcontracts.PublicEventX{{…}} is read the same as the value form. The
// elided inner element's own Type is nil either way; what differs is a StarExpr
// in the enclosing type.
func elementSelector(e ast.Expr) *ast.SelectorExpr {
	if star, ok := e.(*ast.StarExpr); ok {
		e = star.X
	}
	sel, _ := e.(*ast.SelectorExpr)
	return sel
}

// qualifiedName answers the type's name only when the selector is reached
// through the contracts package.
func qualifiedName(sel *ast.SelectorExpr, qualifier string) (string, bool) {
	if sel == nil {
		return "", false
	}
	pkg, ok := sel.X.(*ast.Ident)
	if !ok || pkg.Name != qualifier {
		return "", false
	}
	return sel.Sel.Name, true
}

// contractsQualifier answers the name internal/contracts is reachable under in
// one file, and whether the file imports it at all.
func contractsQualifier(file *ast.File) (string, bool) {
	const contractsPath = `"github.com/gradionhq/margince/backend/internal/contracts"`
	for _, imp := range file.Imports {
		if imp.Path.Value != contractsPath {
			continue
		}
		if imp.Name != nil {
			return imp.Name.Name, true
		}
		// Unaliased: the package declares itself crmcontracts, which is also the
		// alias the other 600-odd imports spell explicitly.
		return "crmcontracts", true
	}
	return "", false
}
