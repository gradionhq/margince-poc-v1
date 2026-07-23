// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package backendarch

// The public-events contract as a cross-cutting fitness function (A15):
// the outbound-webhook surface has three moving parts that must stay in
// lock-step, and nothing in the build forces them to. This is the ONE
// authoritative gate that pins them together, deriving from the runtime
// registry and the tree rather than any hand-kept list:
//
//  1. Coverage — every subscribable event (events.Types() minus the
//     entity-less pipeline class) carries a PublicEvent<Event> schema in
//     the generated registry. The definition of "Phase 4 done".
//  2. No-orphan — every registry key is a real catalog event; a schema for
//     a type the runtime never emits is dead contract.
//  3. Version consistency — the schema's x-version equals the catalog's
//     VersionOf; a split source of truth is how a v2 payload ships under a
//     v1 envelope.
//  4. Delivery-resolvability — every subscribable event's subject resolves,
//     inside webhooks.entityVisibleTo, to EXACTLY ONE of: a row-scope probe
//     branch, the workspace-level allow-list, or a ratified deferred-delivery
//     exception (by event type for the dynamic-object_class mirror.* family,
//     by entity type for the ownerless retention-telemetry subjects). No
//     subscribable static-entity event may be silently fail-closed-denied by
//     accident, and every dynamic-entity event must be EXPLICITLY accounted
//     for — never defaulted. This is the security-load-bearing half: a
//     subject that slips into the workspace-level branch fans out to every
//     owner (a leak); one that slips into the fail-closed default is silently
//     undeliverable (a bug). The gate forces the choice to be made in source.
//
// Points 1-3 read the real registry (internal/contracts + the events
// kernel) — the same source the runtime routes on. Point 4 reads the
// contract's x-entity-type textually (no YAML lib in the root fitness
// package, the contractrefs_test.go precedent) and the classification sets
// out of webhooks/deliveryvisibility.go by AST walk (the tableownership_test.go
// precedent), so it tracks the code, never a copy of it.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/events"
)

const (
	publicEventsPath       = "api/public-events.yaml"
	deliveryVisibilityPath = "internal/modules/webhooks/deliveryvisibility.go"
)

// subscribableTypes is the fan-out catalog the whole surface is measured
// against: every published event type minus the entity-less pipeline class,
// which validateEventTypes forbids subscribing to (BYO-EVT-4). This is the
// SAME derivation webhooks.validateEventTypes runs on, so the gate can
// never drift from what a subscription may actually select.
func subscribableTypes() []string {
	var out []string
	for _, tp := range events.Types() {
		if events.IsPipelineEvent(tp) {
			continue
		}
		out = append(out, tp)
	}
	sort.Strings(out)
	return out
}

func TestEverySubscribableEventHasAPayloadSchema(t *testing.T) {
	var uncovered []string
	for _, tp := range subscribableTypes() {
		if _, ok := crmcontracts.PublicEventVersions[tp]; !ok {
			uncovered = append(uncovered, tp)
		}
	}
	if len(uncovered) > 0 {
		t.Errorf("no PublicEvent schema for %d subscribable event(s): %v (add each to %s)",
			len(uncovered), uncovered, publicEventsPath)
	}
}

func TestNoOrphanPayloadSchema(t *testing.T) {
	catalog := map[string]bool{}
	for _, tp := range events.Types() {
		catalog[tp] = true
	}
	for tp := range crmcontracts.PublicEventVersions {
		if !catalog[tp] {
			t.Errorf("PublicEventVersions has %q, which is not a published event type — dead contract, remove the schema or add the catalog entry", tp)
		}
	}
}

func TestPayloadVersionsMatchCatalog(t *testing.T) {
	for tp, wantVersion := range crmcontracts.PublicEventVersions {
		if got := events.VersionOf(tp); got != wantVersion {
			t.Errorf("version drift for %q: events.VersionOf = %d, PublicEventVersions = %d (contract x-version and catalog.go disagree)", tp, got, wantVersion)
		}
	}
}

// subscribableEventTypeEnumKey / subscribableEventTypeEnumStart /
// subscribableEventTypeEnumItem match the SubscribableEventType schema's
// `enum:` block textually — the same "no YAML lib in the root fitness
// package" precedent as contractEventKey/contractEntityKey above. The block
// starts at the schema's own `enum:` line (six-space indent) and every
// subsequent eight-space-indented `- value` line is a member; anything else
// (the blank line before the next schema) ends it.
var (
	subscribableEventTypeEnumStart = regexp.MustCompile(`^      enum:\s*$`)
	subscribableEventTypeEnumItem  = regexp.MustCompile(`^        - ([A-Za-z0-9._]+)\s*$`)
)

// parseSubscribableEventTypeEnum reads the SubscribableEventType schema's
// enum list straight out of the contract text. It is scoped to the first
// `enum:` block following the `SubscribableEventType:` key so a later
// schema's enum (there is none today, but nothing stops one) never leaks in.
func parseSubscribableEventTypeEnum(t *testing.T) []string {
	t.Helper()
	raw, err := os.ReadFile(publicEventsPath)
	if err != nil {
		t.Fatalf("read %s: %v", publicEventsPath, err)
	}
	lines := strings.Split(string(raw), "\n")
	schemaIdx := -1
	for i, line := range lines {
		if strings.TrimSpace(line) == "SubscribableEventType:" {
			schemaIdx = i
			break
		}
	}
	if schemaIdx == -1 {
		t.Fatalf("%s: no SubscribableEventType schema found", publicEventsPath)
	}
	enumIdx := -1
	for i := schemaIdx; i < len(lines); i++ {
		if subscribableEventTypeEnumStart.MatchString(lines[i]) {
			enumIdx = i
			break
		}
	}
	if enumIdx == -1 {
		t.Fatalf("%s: SubscribableEventType has no enum: block", publicEventsPath)
	}
	var out []string
	for i := enumIdx + 1; i < len(lines); i++ {
		m := subscribableEventTypeEnumItem.FindStringSubmatch(lines[i])
		if m == nil {
			break
		}
		out = append(out, m[1])
	}
	if len(out) == 0 {
		t.Fatalf("%s: SubscribableEventType enum has no members", publicEventsPath)
	}
	return out
}

// TestSubscribableEventTypeEnumMatchesPayloadCatalog pins the UI event-type
// picker's source (the SubscribableEventType enum, from which
// openapi-typescript emits subscribableEventTypeValues and gen-payloads
// emits the Go consts) to the full subscribable catalog
// (crmcontracts.PublicEventVersions — every event with a payload
// schema). Nothing else binds the two together: a family that gets a
// PublicEvent<Event> schema but is never appended to the enum drifts
// silently, and the picker quietly under-offers it forever.
func TestSubscribableEventTypeEnumMatchesPayloadCatalog(t *testing.T) {
	enum := map[string]bool{}
	for _, tp := range parseSubscribableEventTypeEnum(t) {
		if enum[tp] {
			t.Errorf("%s: SubscribableEventType enum lists %q more than once", publicEventsPath, tp)
		}
		enum[tp] = true
	}

	var missingFromEnum []string
	for tp := range crmcontracts.PublicEventVersions {
		if !enum[tp] {
			missingFromEnum = append(missingFromEnum, tp)
		}
	}
	sort.Strings(missingFromEnum)
	if len(missingFromEnum) > 0 {
		t.Errorf("SubscribableEventType enum is missing %d payload-catalog event(s): %v (append each to the enum in %s)",
			len(missingFromEnum), missingFromEnum, publicEventsPath)
	}

	var missingFromCatalog []string
	for tp := range enum {
		if _, ok := crmcontracts.PublicEventVersions[tp]; !ok {
			missingFromCatalog = append(missingFromCatalog, tp)
		}
	}
	sort.Strings(missingFromCatalog)
	if len(missingFromCatalog) > 0 {
		t.Errorf("SubscribableEventType enum lists %d event(s) with no PublicEvent schema: %v (dead enum value, or a missing schema — reconcile with %s)",
			len(missingFromCatalog), missingFromCatalog, publicEventsPath)
	}

	if len(enum) != len(crmcontracts.PublicEventVersions) {
		t.Errorf("SubscribableEventType enum has %d value(s), PublicEventVersions has %d — should be equal (see the mismatches reported above)",
			len(enum), len(crmcontracts.PublicEventVersions))
	}
}

// dynamicProbeResolved names the x-entity-type: dynamic events whose runtime
// subject IS a row-scoped record the fan-out gate probes — the subject class
// travels at runtime (person XOR lead for consent.changed;
// person/lead/deal/activity for retention.applied) rather than being fixed
// in the schema, but every value it takes hits a probe branch in
// entityVisibleTo, so delivery is authorized, not deferred. This is the
// hand-ratified half of the dynamic-event partition (the deferred half lives
// in deliveryvisibility.go's deferredDeliveryEvents); each entry carries why it is
// probe-reachable. An entry that names a non-dynamic or non-subscribable
// event is stale and fails below; a dynamic event in neither set is a silent
// default and also fails.
var dynamicProbeResolved = map[string]string{
	"consent.changed":   "subject is person XOR lead (consent/store.go stamps sub.entityType) — both hit the row-scope probe branch",
	"retention.applied": "subject is person/lead/deal/activity for policy-driven sweeps (all row-scope probed); its ownerless ai_call/ai_call_payload/voice_learning_signal telemetry subjects are the deferredDeliveryEntities half",
}

func TestEverySubscribableEventIsDeliveryResolvable(t *testing.T) {
	entityByEvent := parseContractEntityTypes(t)
	c := parseDeliveryClassification(t)

	// staticResolvable is the union of the three static-entity branches an
	// event's subject can land in inside entityVisibleTo.
	staticResolvable := map[string]string{}
	for e := range c.probeEntities {
		staticResolvable[e] = "row-scope probe"
	}
	for e := range c.workspaceEntities {
		staticResolvable[e] = "workspace-level allow-list"
	}
	for e := range c.deferredEntities {
		staticResolvable[e] = "ratified deferred-delivery entity"
	}

	for _, ev := range subscribableTypes() {
		entity, ok := entityByEvent[ev]
		if !ok {
			t.Errorf("%s: subscribable event %q has no x-entity-type in %s — cannot prove delivery-resolvability", publicEventsPath, ev, publicEventsPath)
			continue
		}
		if entity == "dynamic" {
			_, deferred := c.deferredEvents[ev]
			_, probed := dynamicProbeResolved[ev]
			switch {
			case deferred && probed:
				t.Errorf("dynamic event %q is BOTH event-deferred and probe-resolved — ambiguous classification", ev)
			case !deferred && !probed:
				t.Errorf("dynamic event %q is neither event-deferred (deferredDeliveryEvents) nor probe-resolved (dynamicProbeResolved) — a dynamic subject must be EXPLICITLY classified, never left to the fail-closed default", ev)
			}
			continue
		}
		if _, ok := staticResolvable[entity]; !ok {
			t.Errorf("static-entity event %q (x-entity-type: %q) resolves to NOTHING in webhooks.entityVisibleTo — it would be silently fail-closed-denied. Add a row-scope probe, a workspaceLevelEntities entry, or a ratified deferredDeliveryEntities entry", ev, entity)
		}
	}

	// No stale dynamicProbeResolved entry: each must name a real dynamic
	// subscribable event.
	subscribable := map[string]bool{}
	for _, ev := range subscribableTypes() {
		subscribable[ev] = true
	}
	for ev, rationale := range dynamicProbeResolved {
		if strings.TrimSpace(rationale) == "" {
			t.Errorf("dynamicProbeResolved[%q] has no rationale — a ratified classification must say why it is probe-reachable", ev)
		}
		if !subscribable[ev] {
			t.Errorf("dynamicProbeResolved[%q] is not a subscribable event — stale entry, remove it", ev)
			continue
		}
		if entityByEvent[ev] != "dynamic" {
			t.Errorf("dynamicProbeResolved[%q] is x-entity-type %q, not dynamic — stale entry, remove it", ev, entityByEvent[ev])
		}
	}

	// No stale deferredDeliveryEvents entry either: every deferred event must
	// be a real dynamic subscribable event (else the waiver is dead).
	for ev := range c.deferredEvents {
		if !subscribable[ev] {
			t.Errorf("deferredDeliveryEvents has %q, which is not a subscribable event — stale waiver, remove it from deliveryvisibility.go", ev)
			continue
		}
		if entityByEvent[ev] != "dynamic" {
			t.Errorf("deferredDeliveryEvents has %q, whose x-entity-type is %q not dynamic — a static-entity event should resolve via a probe or the allow-list, not an event-level deferral", ev, entityByEvent[ev])
		}
	}
}

// contractEntityKey / contractEventKey match the STRUCTURED extension lines
// only (exactly six-space indent, the schema-property depth), so the prose
// mentions of `x-entity-type: dynamic` inside description blocks — indented
// deeper and wrapped in backticks — never register as real declarations.
var (
	contractEventKey  = regexp.MustCompile(`^      x-event-type: ([A-Za-z0-9._]+)\s*$`)
	contractEntityKey = regexp.MustCompile(`^      x-entity-type: ([A-Za-z0-9._]+)\s*$`)
)

// parseContractEntityTypes reads the contract textually and returns
// event-type → x-entity-type. The two extensions are adjacent (event then
// entity) in every schema; an entity line binds to the most recent event
// line, and an event with no following entity is reported as a contract bug.
func parseContractEntityTypes(t *testing.T) map[string]string {
	t.Helper()
	raw, err := os.ReadFile(publicEventsPath)
	if err != nil {
		t.Fatalf("read %s: %v", publicEventsPath, err)
	}
	out := map[string]string{}
	var pendingEvent string
	for _, line := range strings.Split(string(raw), "\n") {
		if m := contractEventKey.FindStringSubmatch(line); m != nil {
			if pendingEvent != "" {
				t.Errorf("%s: x-event-type %q has no x-entity-type before the next event", publicEventsPath, pendingEvent)
			}
			pendingEvent = m[1]
			continue
		}
		if m := contractEntityKey.FindStringSubmatch(line); m != nil {
			if pendingEvent == "" {
				t.Errorf("%s: x-entity-type %q has no preceding x-event-type", publicEventsPath, m[1])
				continue
			}
			out[pendingEvent] = m[1]
			pendingEvent = ""
		}
	}
	if pendingEvent != "" {
		t.Errorf("%s: x-event-type %q has no x-entity-type", publicEventsPath, pendingEvent)
	}
	return out
}

// deliveryClassification is the set of entity/event keys entityVisibleTo
// dispatches on, extracted straight from the source so the gate tracks the
// code. probeEntities are the switch-case labels (the row-scope branches);
// the three maps are the allow-list and the two deferred exceptions.
type deliveryClassification struct {
	probeEntities     map[string]bool
	workspaceEntities map[string]bool
	deferredEntities  map[string]bool
	deferredEvents    map[string]bool
}

func parseDeliveryClassification(t *testing.T) deliveryClassification {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, deliveryVisibilityPath, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", deliveryVisibilityPath, err)
	}
	return deliveryClassification{
		probeEntities:     switchCaseStrings(t, file, "entityVisibleTo"),
		workspaceEntities: mapLiteralKeys(t, file, "workspaceLevelEntities"),
		deferredEntities:  mapLiteralKeys(t, file, "deferredDeliveryEntities"),
		deferredEvents:    mapLiteralKeys(t, file, "deferredDeliveryEvents"),
	}
}

// mapLiteralKeys returns the string keys of a top-level `var name = map[...]
// {...}` composite literal. A missing var, or a non-string key, is a test
// failure — the gate must not silently pass on a renamed map.
func mapLiteralKeys(t *testing.T, file *ast.File, name string) map[string]bool {
	t.Helper()
	keys := map[string]bool{}
	found := false
	for _, decl := range file.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok || gd.Tok != token.VAR {
			continue
		}
		for _, spec := range gd.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			for i, id := range vs.Names {
				if id.Name != name || i >= len(vs.Values) {
					continue
				}
				cl, ok := vs.Values[i].(*ast.CompositeLit)
				if !ok {
					t.Fatalf("%s: var %s is not a composite literal", deliveryVisibilityPath, name)
				}
				found = true
				for _, elt := range cl.Elts {
					kv, ok := elt.(*ast.KeyValueExpr)
					if !ok {
						t.Fatalf("%s: var %s has a non key/value element", deliveryVisibilityPath, name)
					}
					keys[stringLit(t, kv.Key)] = true
				}
			}
		}
	}
	if !found {
		t.Fatalf("%s: could not find var %s — the delivery classifier was renamed; update this gate", deliveryVisibilityPath, name)
	}
	return keys
}

// switchCaseStrings collects every string literal appearing in a case clause
// inside the named function's body — the row-scope probe branches of
// entityVisibleTo's `switch entityType`.
func switchCaseStrings(t *testing.T, file *ast.File, funcName string) map[string]bool {
	t.Helper()
	out := map[string]bool{}
	found := false
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name.Name != funcName || fn.Body == nil {
			continue
		}
		found = true
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			cc, ok := n.(*ast.CaseClause)
			if !ok {
				return true
			}
			for _, expr := range cc.List { // empty for the default clause
				if lit, ok := expr.(*ast.BasicLit); ok && lit.Kind == token.STRING {
					out[strings.Trim(lit.Value, `"`)] = true
				}
			}
			return true
		})
	}
	if !found {
		t.Fatalf("%s: could not find func %s — the delivery classifier was renamed; update this gate", deliveryVisibilityPath, funcName)
	}
	if len(out) == 0 {
		t.Fatalf("%s: func %s has no case-clause string literals — the row-scope switch was restructured; update this gate", deliveryVisibilityPath, funcName)
	}
	return out
}

func stringLit(t *testing.T, expr ast.Expr) string {
	t.Helper()
	lit, ok := expr.(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		t.Fatalf("%s: expected a string-literal map key, got %T", deliveryVisibilityPath, expr)
	}
	return strings.Trim(lit.Value, `"`)
}

// TestNoRawEmitForSubscribableEvent is the invariant that catches a missed
// emit-site migration BEFORE it ships a schema-violating body. A
// subscribable event (one with a PublicEvent<Event> payload schema in
// PublicEventVersions) MUST be staged through storekit.EmitEvent /
// EmitEventForEntity — the typed seam that makes "wrong payload for an
// event" impossible to express — never the untyped storekit.Emit(..., type,
// entityType, id, map[string]any{...}) path, which lets a hand-built map
// silently omit a required field or carry a forbidden one (exactly the
// voice.draft_outcome_recorded and retention.applied-over-voice defects
// this gate was written to prevent). It scans every non-test .go file under
// internal/modules and fails on any storekit.Emit call whose event-type
// argument is a subscribable-event string literal. Entity-less pipeline
// events (capture.* via EmitPipeline, a distinct function) are exempt by
// construction: they carry no PublicEvent schema and use a different call.
func TestNoRawEmitForSubscribableEvent(t *testing.T) {
	subscribable := map[string]bool{}
	for tp := range crmcontracts.PublicEventVersions {
		subscribable[tp] = true
	}
	fset := token.NewFileSet()
	err := filepath.WalkDir("internal/modules", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return err
		}
		path = filepath.ToSlash(path)
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return err
		}
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "Emit" {
				return true
			}
			if pkg, ok := sel.X.(*ast.Ident); !ok || pkg.Name != "storekit" {
				return true
			}
			// storekit.Emit(ctx, tx, auditID, eventType, entityType, entityID, payload)
			// — the event type is the fourth argument (index 3).
			if len(call.Args) < 4 {
				return true
			}
			lit, ok := call.Args[3].(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			eventType := strings.Trim(lit.Value, `"`)
			if subscribable[eventType] {
				t.Errorf("%s: storekit.Emit(..., %q, ...) stages a SUBSCRIBABLE event through the untyped seam — route it through storekit.EmitEvent / EmitEventForEntity with its PublicEvent%s payload builder so the schema is enforced at the call site (BYO-EVT-4 / schema conformance)",
					path, eventType, eventType)
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
