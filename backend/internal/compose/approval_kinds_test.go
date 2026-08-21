// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// Fitness function for the approval surface (M5): every tool the
// registry admits at 🟡 (or dynamically escalates to 🟡) stages
// approvals under its own kind — and the approvals module's decidable()
// fails closed on kinds it has no decision-grant mapping for. A
// confirmation_required tool without a mapping would strand every staging in a queue no inbox
// shows and no human may decide. The tool list is derived from the live
// registry, so registering a new 🟡 tool without extending
// decisionGrants fails here, not in production.

import (
	"context"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gradionhq/margince/backend/internal/modules/activities"
	"github.com/gradionhq/margince/backend/internal/modules/agents"
	"github.com/gradionhq/margince/backend/internal/modules/approvals"
	"github.com/gradionhq/margince/backend/internal/modules/automation"
	"github.com/gradionhq/margince/backend/internal/modules/deals"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
	"github.com/gradionhq/margince/backend/internal/shared/ports/mcp"
	"github.com/gradionhq/margince/backend/internal/shared/ports/retrieval"
)

// stubApprovals satisfies the registry's staging dependency; the test
// never stages, it only reads the declared surface.
type stubApprovals struct{}

// StageQuotaRelease satisfies the seam; a step-up never reaches these tests.
func (stubApprovals) StageQuotaRelease(_ context.Context, _ agents.QuotaReleaseRequest) (ids.ApprovalID, bool, error) {
	return ids.ApprovalID{}, false, nil
}

func (stubApprovals) StageCall(_ context.Context, _ agents.StageRequest) (ids.ApprovalID, bool, error) {
	return ids.ApprovalID{}, false, nil
}

func (stubApprovals) Redeem(_ context.Context, _ ids.ApprovalID, _, _ string) (int64, bool, error) {
	return 0, false, nil
}

// stubRetriever/stubComms exist so the derived tool list covers the
// intent and comms registrations; the test only reads Specs().
type stubRetriever struct{}

func (stubRetriever) Search(context.Context, retrieval.Query) (retrieval.Result, error) {
	return retrieval.Result{}, nil
}

func (stubRetriever) AssembleContext(context.Context, datasource.EntityRef, retrieval.AssembleOptions) (retrieval.Context, error) {
	return retrieval.Context{}, nil
}

type stubComms struct{}

func (stubComms) DraftEmail(context.Context, ids.UUID, string) (string, string, error) {
	return "", "", nil
}

func (stubComms) DraftAccountEmail(context.Context, []agents.RecordLink, string) (string, string, error) {
	return "", "", nil
}

func (stubComms) SendEmail(context.Context, ids.UUID, agents.SendEmailArgs) (agents.SendEmailResult, error) {
	return agents.SendEmailResult{}, nil
}

func (stubComms) SendAccountEmail(context.Context, []agents.RecordLink, agents.SendEmailArgs) (agents.SendEmailResult, error) {
	return agents.SendEmailResult{}, nil
}

func (stubComms) SendMessage(context.Context, ids.UUID, agents.SendMessageArgs) (agents.SendMessageResult, error) {
	return agents.SendMessageResult{}, nil
}

func (stubComms) IsChannelKind(kind string) bool { return activities.IsChannelKind(kind) }

func (stubComms) CanSendOnProvider(p string) bool { return activities.CanSendOnProvider(p) }

func (stubComms) Availability(context.Context, *ids.UUID, time.Time, time.Time, int) (agents.AvailabilityResult, error) {
	return agents.AvailabilityResult{}, nil
}

func (stubComms) BookMeeting(context.Context, agents.BookMeetingArgs) (json.RawMessage, error) {
	return nil, nil
}

// stubSoR satisfies the record reader the 🟡 comms verbs stage against. This
// walk only reads Specs(), so nothing calls it — but RegisterCommsTools now
// refuses a nil provider at wiring time, because a surface that advertises
// three sends and panics on them is worse than one that will not boot.
type stubSoR struct {
	datasource.SystemOfRecordProvider
}

func TestEveryConfirmationRequiredToolHasADecisionGrantMapping(t *testing.T) {
	registry := agents.NewRegistry(stubApprovals{}, nil)
	agents.RegisterCoreTools(registry, nil, nil, nil, nil)
	agents.RegisterIntentTools(registry, stubRetriever{})
	agents.RegisterCommsTools(registry, stubComms{}, stubSoR{})

	for _, spec := range registry.Specs() {
		if spec.Tier == mcp.TierAutoExecute {
			continue // never staged, never decided
		}
		if !approvals.KindHasDecisionGrants(spec.Name) {
			t.Errorf("tool %s can stage approvals (tier %v) but approvals has no decision-grant mapping for it — its stagings would be undecidable", spec.Name, spec.Tier)
		}
	}
}

// Every kind with a REGISTERED EFFECT must also be decidable. The tool-registry
// sweep above misses these: a compose-registered effect kind is stageable
// without being an agent tool, and requireDecisionGrants fails closed on an
// unmapped kind — so the proposals would be staged, hidden from every list, and
// undecidable, while the ledger row that pointed at them waited forever.
//
// This is the obligation stated as "every stageable kind", which is what the
// system actually guarantees, rather than "every tool", which is where the
// first version of this test happened to look.
func TestEveryRegisteredEffectKindHasADecisionGrantMapping(t *testing.T) {
	svc := approvalsServiceWithEffects(nil)
	kinds := svc.EffectKinds()
	if len(kinds) == 0 {
		t.Fatal("no effect kinds registered — the scan found nothing to check, which means it is broken")
	}
	for _, kind := range kinds {
		if !approvals.KindHasDecisionGrants(kind) {
			t.Errorf("kind %q has a registered approved-effect but no decision-grant mapping — its proposals would be staged and then be undecidable by anyone", kind)
		}
	}
}

// The kind string is not a namespace, and the two writers of it do not
// coordinate: the REST admission gate stages under the operation's TOOL
// name, while compose registers approved-effect executors under kinds its
// own proposal flows mint. "enrich" is both — the scrape proposal's kind
// and the tool behind coldStartReadback, deepReadCompany and scrapeCompany
// — so an agent could mint a staging that a human's approve click would
// feed to the compose enrichment executor.
//
// This test names the overlap rather than forbidding it, because forbidding
// it would mean either renaming stored kinds or refusing three legitimate
// confirm-first agent routes. What the system guarantees instead is that
// provenance decides: an executor runs only for a staging with no passport.
// The list below is the evidence that the guarantee is load-bearing — if it
// ever empties, the collision is gone and this test should go with it.
func TestCollidingEffectKindsAreCoveredByProvenance(t *testing.T) {
	svc := approvalsServiceWithEffects(nil)
	effects := map[string]bool{}
	for _, kind := range svc.EffectKinds() {
		effects[kind] = true
	}
	if len(effects) == 0 {
		t.Fatal("no effect kinds registered — the scan found nothing to check, which means it is broken")
	}

	colliding := map[string]string{}
	for route, pol := range agentPolicies {
		if pol.Access == accessTool && effects[pol.Tool] {
			colliding[pol.Tool] = route
		}
	}
	if len(colliding) == 0 {
		t.Error("no agent tool name collides with a registered effect kind — the provenance check in " +
			"approvals.decide now guards nothing, so delete it and delete this test rather than " +
			"leaving a control nobody can see is dead")
	}
	for tool, route := range colliding {
		t.Logf("agent route %s stages kind %q, which also has a server-side effect executor — "+
			"only the no-passport check keeps a human's approve click from running it", route, tool)
	}
}

// Every confirm-first row the CONTRACT declares has a decision mapping too —
// including a verb no tool implements.
//
// The registry sweep above walks what is registered, which is why it could not
// see #484: `connect_incumbent` was declared confirmation_required by an
// operation with no registered tool at all, so an agent's call cleared the
// admission gate, reached stageRefusal, found no mapping and answered 403. It
// was fail-closed and it was also unreachable capability the contract kept
// advertising — a shape only the generated policy table shows, since that table
// is the contract's own reading of itself.
//
// There is deliberately NO waiver. A verb that cannot honestly be staged has
// the wrong annotation, and the fix is `x-agent-access: human-only` in
// api/crm.yaml — a statement about authority, made where a reviewer sees it,
// rather than an exemption from one. That is exactly how #484's own operations
// were resolved.
func TestEveryConfirmationRequiredPolicyHasAnApprovalKind(t *testing.T) {
	checked := 0
	for route, pol := range agentPolicies {
		if pol.Access != accessTool || pol.Tier == tierAutoExecute {
			continue
		}
		checked++
		if approvals.KindHasDecisionGrants(pol.Tool) {
			continue
		}
		t.Errorf("%s declares %s at %v, and approvals has no decision-grant mapping for that kind. "+
			"Every call to it clears admission and is then refused at staging, so the contract "+
			"advertises a governed verb no agent can ever reach. Map the kind in "+
			"approvals.decisionGrants, or annotate the operation x-agent-access: human-only.",
			route, pol.Tool, pol.Tier)
	}
	if checked == 0 {
		t.Fatal("no confirm-first tool routes in the generated policy — the gate covers nothing")
	}
}

// TestEveryKindComposeStagesHasADecisionGrantMapping closes the gap the three
// tests above leave: they derive their kinds from the TOOL registry and the
// effect registry, so a kind staged by a WORKER — no tool, no effect — is
// invisible to all of them.
//
// That gap is not hypothetical. The held-scheduled-send item (ADR-0104 §5) was
// staged by the fire path, landed in the approval table, and appeared in nobody's
// inbox: decidable() fails closed on an unmapped kind, so every row was written
// and then filtered out of the one surface that exists to show it. It looked
// exactly like a notifier that never ran.
//
// Derived from the SOURCE rather than a list: every approvals.StageInput literal
// in this package names its kind, and each one has to be decidable by somebody.
func TestEveryKindComposeStagesHasADecisionGrantMapping(t *testing.T) {
	fset := token.NewFileSet()
	files := parsePackageFiles(t, fset, ".")
	// Every string constant this package declares, so a Kind named by an
	// identifier resolves to the value it will carry at run time.
	consts := map[string]string{}
	for _, file := range files {
		collectStringConsts(file, consts)
	}
	kinds := map[string]token.Position{}
	var unresolved []string
	for _, file := range files {
		{
			ast.Inspect(file, func(n ast.Node) bool {
				lit, ok := n.(*ast.CompositeLit)
				if !ok || !isStageInput(lit) {
					return true
				}
				kind, pos, outcome := stagedKind(lit, consts)
				switch outcome {
				case kindResolved:
					kinds[kind] = fset.Position(pos)
				case kindUnreadable:
					// A Kind this gate cannot read is a stager it cannot judge,
					// and silence there would be the same invisibility the gate
					// exists to catch — one level up.
					unresolved = append(unresolved, fset.Position(lit.Pos()).String())
				case kindAbsent:
					// A literal with no Kind field at all is a zero value or a
					// partial build — an error return, a struct assembled
					// field-by-field elsewhere. There is no kind here to judge.
				}
				return true
			})
		}
	}
	if len(kinds) == 0 {
		t.Fatal("no approvals.StageInput literals found in compose — this gate resolved nothing and would demand nothing")
	}
	for _, where := range unresolved {
		t.Errorf("%s stages an approval whose Kind this gate cannot resolve — teach stagedKind to read it, or the stager rides in unjudged",
			where)
	}
	for kind, pos := range kinds {
		// The target type is what a target-RESOLVED kind derives its second
		// grant from. Passing "activity" resolves those without pinning this
		// gate to any one kind's target: what it asks is whether the kind is
		// MAPPED at all, and an unmapped one errors before the type is read.
		if _, err := approvals.DecisionGrantObjects(kind, "activity"); err != nil {
			t.Errorf("%s stages approval kind %q, which approvals has no decision-grant mapping for — every such row is written and then filtered out of the inbox, so it reads as a stager that never ran (%v)",
				pos, kind, err)
		}
	}
}

// isStageInput reports whether a composite literal builds an approvals.StageInput.
func isStageInput(lit *ast.CompositeLit) bool {
	sel, ok := lit.Type.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "StageInput" {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	return ok && pkg.Name == "approvals"
}

// kindOutcome says what reading a literal's Kind field found: a value this gate
// can judge, a field it could not read, or no field at all. The three are
// deliberately distinct — collapsing "unreadable" into "absent" is how a stager
// rides in unjudged.
type kindOutcome int

const (
	kindAbsent kindOutcome = iota
	kindResolved
	kindUnreadable
)

// stagedKind resolves the Kind field of a StageInput literal: a string literal
// directly, a constant declared in this package, or an exported approvals kind.
//
// A Kind that is a plain identifier resolving to nothing is a PARAMETER — the
// stager is a helper several callers share, and its kind is whatever they pass.
// Those are reported as unreadable rather than skipped, because the gate cannot
// see which kinds actually reach them.
func stagedKind(lit *ast.CompositeLit, consts map[string]string) (string, token.Pos, kindOutcome) {
	for _, elt := range lit.Elts {
		kv, ok := elt.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		key, ok := kv.Key.(*ast.Ident)
		if !ok || key.Name != "Kind" {
			continue
		}
		switch v := kv.Value.(type) {
		case *ast.BasicLit:
			if v.Kind == token.STRING {
				return strings.Trim(v.Value, `"`), v.Pos(), kindResolved
			}
		case *ast.Ident:
			// A constant this package declares, resolved to its value.
			if value, found := consts[v.Name]; found {
				return value, v.Pos(), kindResolved
			}
			// Otherwise a function parameter: the helper is shared and its kind
			// is whatever each caller passes, which this gate cannot see from
			// the literal. Those callers are covered by the tool and effect
			// registries above — the two gates together are what make the
			// coverage total.
			return "", token.NoPos, kindAbsent
		case *ast.SelectorExpr:
			// approvals.KindX used inline, resolved through the live package —
			// the value the build compiles is the one this gate must judge.
			if pkg, ok := v.X.(*ast.Ident); ok {
				if value, found := crossPackageKinds[pkg.Name+"."+v.Sel.Name]; found {
					return value, v.Pos(), kindResolved
				}
				// A selector on something that is not a package — in.Kind on a
				// request struct — is a kind supplied by whoever CALLS this
				// helper. The gate cannot see those from here; the adapter's
				// own caller-side gate is what covers them (the tool and effect
				// registries above), which is why this is a pass rather than a
				// finding.
				if !isPackageQualifier(pkg.Name) {
					return "", token.NoPos, kindAbsent
				}
			}
		}
		return "", kv.Value.Pos(), kindUnreadable
	}
	return "", token.NoPos, kindAbsent
}

// collectStringConsts records every package-level string constant, so a Kind
// written as an identifier resolves to the value it carries at run time.
func collectStringConsts(file *ast.File, into map[string]string) {
	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.CONST {
			continue
		}
		for _, spec := range gen.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			for i, name := range vs.Names {
				if i >= len(vs.Values) {
					continue
				}
				switch v := vs.Values[i].(type) {
				case *ast.BasicLit:
					if v.Kind == token.STRING {
						into[name.Name] = strings.Trim(v.Value, `"`)
					}
				case *ast.SelectorExpr:
					// A local alias for another package's exported kind
					// (approvals.KindX). Resolved through the live package
					// rather than by re-parsing it: the value the build uses is
					// the one this gate must judge, and a second reading of the
					// source is a second chance to disagree with it.
					if pkg, ok := v.X.(*ast.Ident); ok && pkg.Name == "approvals" {
						if value, found := exportedApprovalKinds[v.Sel.Name]; found {
							into[name.Name] = value
						}
					}
				}
			}
		}
	}
}

// exportedApprovalKinds is every kind constant the approvals module exports for
// another package to stage under. Listed here rather than reflected, because a
// constant has no runtime identity to reflect on — and the list is short
// because exporting one is the deliberate act of saying "compose stages this".
//
// A kind exported there and missing here makes this gate skip a stager, so the
// companion assertion below fails when the two fall out of step.
// gatekit:fixture the value each exported approvals kind constant carries —
// resolved constant data, not a cost.
var exportedApprovalKinds = map[string]string{
	"KindQuotaRelease":      approvals.KindQuotaRelease,
	"KindScheduledSendHeld": approvals.KindScheduledSendHeld,
	"KindImportCommit":      approvals.KindImportCommit,
}

// crossPackageKinds resolves a kind another module exports and compose stages
// under, keyed as written at the call site.
// gatekit:fixture the value each cross-package kind constant carries, keyed as
// written at the call site — resolved constant data, not a cost.
var crossPackageKinds = map[string]string{
	"approvals.KindQuotaRelease":      approvals.KindQuotaRelease,
	"approvals.KindScheduledSendHeld": approvals.KindScheduledSendHeld,
	"deals.CloseDateCorrectionKind":   deals.CloseDateCorrectionKind,
	"deals.FollowUpReconcileKind":     deals.FollowUpReconcileKind,
}

// isPackageQualifier reports whether an identifier names an imported package
// rather than a local variable. Kept as the short list compose actually stages
// through, so a NEW module's kind is unreadable — and therefore reported —
// until somebody adds it here rather than silently skipped.
func isPackageQualifier(name string) bool {
	switch name {
	case "approvals", "deals", "people", "activities", "agents":
		return true
	}
	return false
}

// TestTheExportedApprovalKindMapIsComplete is the companion the gate above
// depends on: exportedApprovalKinds is written by hand, and a kind exported by
// approvals but missing from it makes that gate skip a stager silently.
//
// Derived from the approvals SOURCE rather than a second list, so exporting a
// new kind fails here until the map catches up.
func TestTheExportedApprovalKindMapIsComplete(t *testing.T) {
	fset := token.NewFileSet()
	exported := map[string]bool{}
	for _, file := range parsePackageFiles(t, fset, "../modules/approvals") {
		consts := map[string]string{}
		collectStringConsts(file, consts)
		for name := range consts {
			if strings.HasPrefix(name, "Kind") {
				exported[name] = true
			}
		}
	}
	if len(exported) == 0 {
		t.Fatal("no exported Kind constants found in approvals — this gate resolved nothing")
	}
	for name := range exported {
		if _, mapped := exportedApprovalKinds[name]; !mapped {
			t.Errorf("approvals exports %s and exportedApprovalKinds does not carry it — every compose stager using it would be skipped by the decision-grant gate", name)
		}
	}
	for name := range exportedApprovalKinds {
		if !exported[name] {
			t.Errorf("exportedApprovalKinds carries %s, which approvals no longer exports — stale entry", name)
		}
	}
}

// parsePackageFiles parses every non-test Go file in a directory.
//
// Explicit rather than parser.ParseDir, which is deprecated for ignoring build
// tags: this gate wants every file whatever tags it carries, because a stager
// behind a tag stages just as really as one without.
func parsePackageFiles(t *testing.T, fset *token.FileSet, dir string) []*ast.File {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("reading %s: %v", dir, err)
	}
	var files []*ast.File
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, filepath.Join(dir, name), nil, parser.ParseComments)
		if err != nil {
			t.Fatalf("parsing %s: %v", name, err)
		}
		files = append(files, file)
	}
	if len(files) == 0 {
		t.Fatalf("no Go files parsed from %s — this gate would resolve nothing", dir)
	}
	return files
}

// Every kind an AUTOMATION can stage must be decidable, which is a strictly
// wider obligation than the one above and was not being checked.
//
// The test above holds kinds with a REGISTERED EFFECT to a grant mapping. An
// automation stages kinds that have no compose-registered effect at all — it
// parks its run and waits for a human — so every one of those was invisible to
// it. Two of them, assign_owner and emit_flow_event, had no mapping: their
// stagings were written, hidden from every inbox by requireDecisionGrants
// failing closed, and could not be decided by anybody, while the run that
// raised them stayed in requires_approval permanently.
//
// The list comes from the automation package rather than being restated here,
// so a kind that starts staging is caught by this gate rather than by whoever
// eventually notices a run that never finished.
func TestEveryKindAnAutomationCanStageIsDecidable(t *testing.T) {
	kinds := automation.StageableKinds()
	if len(kinds) == 0 {
		t.Fatal("the automation package reports no stageable kinds — the scan found nothing to check, which means it is broken")
	}
	for _, kind := range kinds {
		if !approvals.KindHasDecisionGrants(kind) {
			t.Errorf("an automation can stage %q but no decision-grant mapping exists for it — its stagings are hidden from every inbox and undecidable, and the run behind each one waits forever", kind)
		}
	}
}

// Every stageable kind must EXECUTE on approval, one way or the other.
//
// Decidable is not the same as actionable. A kind can have a grant mapping,
// display in the inbox, take a human's yes — and then run nothing, because
// release executors are registered per kind and it has none. That is the worse
// half of the same defect: the human believes they authorized the work, the
// approval is decided, and no owner moved.
//
// So each stageable kind must be in exactly one bucket. Either its whole effect
// is the asking (request_approval, whose yes IS the outcome — the run completes
// off the decision event), or a release executor performs its write and
// completes the run inside that write's transaction. Anything in neither bucket
// is a card that lies about what approving it does.
func TestEveryStageableKindEitherAsksOnlyOrHasAReleaseEffect(t *testing.T) {
	svc := approvalsServiceWithEffects(nil)
	registered := map[string]bool{}
	for _, kind := range svc.EffectKinds() {
		registered[kind] = true
	}
	// The send-path kinds register later than this service is built (see
	// lateApprovalEffects) — they are effects nonetheless, and a gate that
	// could not see them would read a held draft as unactionable.
	for kind := range lateApprovalEffects {
		registered[kind] = true
	}
	asksOnly := map[string]bool{}
	for _, kind := range automation.AskingOnlyKinds() {
		asksOnly[kind] = true
	}
	for _, kind := range automation.StageableKinds() {
		if asksOnly[kind] == registered[kind] {
			complaint := "in neither bucket: no release executor, and not declared asking-only"
			if asksOnly[kind] {
				complaint = "declared asking-only AND has a release executor — two answers to which one finishes its run"
			}
			t.Errorf("an automation can stage %q, and it is %s — a human approving it is told work was authorized that nothing performs, or is told twice",
				kind, complaint)
		}
	}
}
