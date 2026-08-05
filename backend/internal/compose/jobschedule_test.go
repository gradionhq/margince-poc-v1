// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

import (
	"go/ast"
	"maps"
	"slices"
	"testing"
	"time"

	"github.com/gradionhq/margince/backend/internal/modules/capture"
	"github.com/gradionhq/margince/backend/internal/platform/jobs"
)

// specFor answers the declaration a test names, failing rather than returning
// a zero Spec: a zero Cadence and a zero Registration are both meaningful
// values here, so a silently missing kind would make a test pass for the
// wrong reason.
func specFor(t *testing.T, kind string) jobs.Spec {
	t.Helper()
	spec, ok := jobs.SpecFor(kind)
	if !ok {
		t.Fatalf("api/jobs.yaml does not declare %q", kind)
	}
	return spec
}

// TestScheduleIntervalReadsTheDeclaredLiteral covers three kinds with three
// different literals, so a resolver that returned one hard-coded duration
// could not pass.
func TestScheduleIntervalReadsTheDeclaredLiteral(t *testing.T) {
	cases := []struct {
		kind string
		want time.Duration
	}{
		{"idempotency_retention", time.Hour},
		{"embed_drift_sweep", 15 * time.Minute},
		{"gmail_sync", 30 * time.Second},
	}
	for _, tc := range cases {
		got, scheduled := scheduleInterval(JobRunnerConfig{}, specFor(t, tc.kind))
		if !scheduled {
			t.Errorf("%s: no schedule, want one — a literal cadence does not depend on the configuration", tc.kind)
			continue
		}
		if got != tc.want {
			t.Errorf("%s: interval = %s, want %s", tc.kind, got, tc.want)
		}
	}
}

// TestScheduleIntervalTakesTheOperatorsDial pins that an {operator: …} cadence
// reads the named field. Seven minutes is a value no declaration carries, so a
// resolver falling back to a literal cannot produce it.
func TestScheduleIntervalTakesTheOperatorsDial(t *testing.T) {
	cfg := JobRunnerConfig{CloseDateInterval: 7 * time.Minute}
	got, scheduled := scheduleInterval(cfg, specFor(t, "close_date_sweep"))
	if !scheduled {
		t.Fatal("close_date_sweep: no schedule, want one")
	}
	if got != 7*time.Minute {
		t.Errorf("interval = %s, want 7m — an {operator: …} cadence must read the config field it names", got)
	}
}

// TestTheReconcileIntervalMatchesTheStatedStalenessBound holds the graph
// projection's staleness promise to the cadence that makes it true.
func TestTheReconcileIntervalMatchesTheStatedStalenessBound(t *testing.T) {
	// The migration states that window counts may be up to 24h over-inclusive.
	// That promise is only true if this pass runs daily; loosening the declared
	// cadence without amending the migration would make the comment a lie. The
	// declaration is what River is handed, so it is what this asserts.
	spec := specFor(t, GraphEdgeReconcileArgs{}.Kind())
	if spec.Cadence.Fixed != 24*time.Hour {
		t.Errorf("declared reconcile cadence is %v, but the projection's stated bound is 24h", spec.Cadence.Fixed)
	}
}

// TestPeriodicForUsesTheDeclaredLiteralCadence proves the resolver is wired to
// the periodic entry, not merely callable.
func TestPeriodicForUsesTheDeclaredLiteralCadence(t *testing.T) {
	got := periodicFor(JobRunnerConfig{}, IdempotencyRetentionArgs{})
	if len(got) != 1 {
		t.Fatalf("got %d periodic jobs, want 1", len(got))
	}
}

func TestPeriodicForTakesTheOperatorsIntervalWhenDeclared(t *testing.T) {
	cfg := JobRunnerConfig{CloseDateInterval: 7 * time.Minute}
	got := periodicFor(cfg, CloseDateSweepArgs{})
	if len(got) != 1 {
		t.Fatalf("got %d periodic jobs, want 1 — an {operator: …} cadence must read the config field", len(got))
	}
}

func TestPeriodicForRegistersNothingWhenItsDependencyIsAbsent(t *testing.T) {
	got := periodicFor(JobRunnerConfig{}, OverlayReconcileArgs{})
	if len(got) != 0 {
		t.Errorf("got %d periodic jobs, want 0 — overlay_reconcile declares registers_nothing without OverlayVault, and a row nothing can work must never be queued", len(got))
	}
}

// TestPeriodicForNeedsEveryFieldOfADeclaredConjunction covers the one kind
// whose registration names two fields. Gmail's push-watch maintenance needs
// the OAuth app AND a Pub/Sub topic, and the first alone is what the nested
// guard it replaces made easy to get wrong.
func TestPeriodicForNeedsEveryFieldOfADeclaredConjunction(t *testing.T) {
	registry := &capture.Registry{}
	watch := GmailWatchConfig{Topic: "projects/p/topics/t", Interval: time.Hour}

	if got := periodicFor(JobRunnerConfig{GmailRegistry: registry}, GmailWatchArgs{}); len(got) != 0 {
		t.Errorf("with a registry but no topic: got %d periodic jobs, want 0 — every field of a conjunction has to be supplied", len(got))
	}
	if got := periodicFor(JobRunnerConfig{GmailWatch: watch}, GmailWatchArgs{}); len(got) != 0 {
		t.Errorf("with a topic but no registry: got %d periodic jobs, want 0 — every field of a conjunction has to be supplied", len(got))
	}
	if got := periodicFor(JobRunnerConfig{GmailRegistry: registry, GmailWatch: watch}, GmailWatchArgs{}); len(got) != 1 {
		t.Errorf("with both: got %d periodic jobs, want 1", len(got))
	}
}

// TestPeriodicForOmitsTheScheduleWhenTheIntervalIsNotPositive covers the third
// posture on the one kind that declares it with no registration gate in front
// of it, so the omission can only be the interval's doing.
func TestPeriodicForOmitsTheScheduleWhenTheIntervalIsNotPositive(t *testing.T) {
	if got := periodicFor(JobRunnerConfig{}, PrivacyRetentionArgs{}); len(got) != 0 {
		t.Errorf("got %d periodic jobs, want 0 — a non-positive interval declares the schedule absent while the workers stay registered", len(got))
	}
	cfg := JobRunnerConfig{PrivacyRetention: PrivacyRetentionConfig{Interval: time.Hour}}
	if got := periodicFor(cfg, PrivacyRetentionArgs{}); len(got) != 1 {
		t.Errorf("with a positive interval: got %d periodic jobs, want 1", len(got))
	}
}

// TestPeriodicForKeepsAScheduleTheDeclarationDidNotMakeConditional is the
// other half of the posture above: schedule-when-positive is DECLARED per
// kind, not a reading applied to every operator dial. close_date_sweep does
// not declare it, so a zero interval still places its entry — which is what
// cmd/worker's boot validation exists to keep out of a real deployment.
func TestPeriodicForKeepsAScheduleTheDeclarationDidNotMakeConditional(t *testing.T) {
	if got := periodicFor(JobRunnerConfig{}, CloseDateSweepArgs{}); len(got) != 1 {
		t.Errorf("got %d periodic jobs, want 1 — close_date_sweep declares no schedule_when_positive, so the two postures must not be collapsed", len(got))
	}
}

// TestPeriodicForPlacesNoScheduleForAnOnDemandDispatcher covers the dispatcher
// a human's confirm enqueues. Its registration posture is registers_anyway, so
// nothing but the cadence can be the reason there is no entry.
func TestPeriodicForPlacesNoScheduleForAnOnDemandDispatcher(t *testing.T) {
	if got := periodicFor(JobRunnerConfig{}, EmbedReindexArgs{}); len(got) != 0 {
		t.Errorf("got %d periodic jobs, want 0 — embed_reindex declares cadence: on_demand and no clock ever enqueues it", len(got))
	}
}

// TestRegistersHonoursBothAbsentPostures pins the distinction the declaration
// exists to carry. The same missing field decides differently depending on the
// posture, so a resolver that collapsed the two would fail exactly one arm.
func TestRegistersHonoursBothAbsentPostures(t *testing.T) {
	nothing := jobs.Registration{When: []string{"Embedder"}}
	anyway := jobs.Registration{When: []string{"Embedder"}, AbsentRegistersAnyway: true}

	if registers(JobRunnerConfig{}, nothing) {
		t.Error("registers_nothing with the field absent = true, want false")
	}
	if !registers(JobRunnerConfig{}, anyway) {
		t.Error("registers_anyway with the field absent = false, want true — the worker stays so a picked-up row fails loudly instead of rotting queued")
	}
	if !registers(JobRunnerConfig{}, jobs.Registration{}) {
		t.Error("an empty registration = false, want true — a kind that names no dependency registers unconditionally")
	}
}

// declaredFieldPaths collects, over every declared kind, the JobRunnerConfig
// field paths pathsIn names — the set a lookup table in this package has to
// answer.
func declaredFieldPaths(pathsIn func(jobs.Spec) []string) []string {
	seen := map[string]struct{}{}
	for _, spec := range jobs.Declared() {
		for _, path := range pathsIn(spec) {
			if path != "" {
				seen[path] = struct{}{}
			}
		}
	}
	return slices.Sorted(maps.Keys(seen))
}

// assertTableAnswersExactly compares a lookup table's keys with the paths the
// declaration names. Both directions matter: an unanswered path panics the
// boot, and an answer no kind names is a dead entry nobody would notice.
func assertTableAnswersExactly(t *testing.T, table string, answered, declared []string) {
	t.Helper()
	if len(declared) == 0 {
		t.Fatalf("%s: the declaration names no field path at all — this gate would pass vacuously", table)
	}
	for _, path := range declared {
		if !slices.Contains(answered, path) {
			t.Errorf("%s does not answer JobRunnerConfig.%s, which api/jobs.yaml names — the boot would panic on it", table, path)
		}
	}
	for _, path := range answered {
		if !slices.Contains(declared, path) {
			t.Errorf("%s answers JobRunnerConfig.%s, which no declaration names — a dead entry", table, path)
		}
	}
}

// TestEveryDeclaredRegistrationFieldIsAnswered derives the obligation from the
// declaration rather than maintaining it as a list: configDependencies must
// answer exactly the JobRunnerConfig fields api/jobs.yaml gates a kind on.
func TestEveryDeclaredRegistrationFieldIsAnswered(t *testing.T) {
	assertTableAnswersExactly(t, "configDependencies",
		slices.Sorted(maps.Keys(configDependencies(JobRunnerConfig{}))),
		declaredFieldPaths(func(spec jobs.Spec) []string { return spec.Registration.When }))
}

// TestEveryDeclaredCadenceFieldIsAnswered is the same obligation for the
// operator dials — both the field a cadence is read from and the field whose
// positivity decides whether there is a cadence at all.
func TestEveryDeclaredCadenceFieldIsAnswered(t *testing.T) {
	assertTableAnswersExactly(t, "operatorIntervals",
		slices.Sorted(maps.Keys(operatorIntervals(JobRunnerConfig{}))),
		declaredFieldPaths(func(spec jobs.Spec) []string {
			return []string{spec.Cadence.OperatorField, spec.Cadence.ScheduleWhenPositive}
		}))
}

// scheduledDispatcherFloor guards against a vacuous pass below. Twenty-two
// dispatchers carry a clock today; the floor sits at eighteen so retiring a
// few passes does not drag the gate along, while a walker that matched
// nothing — or a rename of periodicFor — still trips it.
const scheduledDispatcherFloor = 18

// periodicSiteKinds reads every periodicFor call in this package's own sources
// and answers the kinds they schedule, counted so a duplicate site is visible.
// Read off the source rather than executed because NewJobRunner needs a live
// pool, and the claim here is about what is wired, not about what runs.
func periodicSiteKinds(t *testing.T) map[string]int {
	t.Helper()
	byType := kindByGoType()
	wired := map[string]int{}
	_, files := parseComposeSources(t)
	for _, file := range files {
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			fn, ok := call.Fun.(*ast.Ident)
			if !ok || fn.Name != "periodicFor" || len(call.Args) != 2 {
				return true
			}
			lit, ok := call.Args[1].(*ast.CompositeLit)
			if !ok {
				t.Errorf("a periodicFor site passes %T rather than an args literal, so this gate cannot read the kind it schedules", call.Args[1])
				return true
			}
			name, ok := lit.Type.(*ast.Ident)
			if !ok {
				t.Errorf("a periodicFor site passes a composite literal of %T rather than a named args type", lit.Type)
				return true
			}
			kind, declared := byType[name.Name]
			if !declared {
				t.Errorf("a periodicFor site names %s, which no declared kind is typed as", name.Name)
				return true
			}
			wired[kind]++
			return true
		})
	}
	return wired
}

// TestEveryScheduledDispatcherIsWiredExactlyOnce derives the wiring obligation
// from the contract instead of keeping it as a list: a dispatcher whose
// declaration carries a clock must have exactly one periodicFor site, and a
// site must not name a kind with no schedule to place. A dispatcher that lost
// its site is otherwise silent — nothing fails, the pass simply never ticks —
// which is the failure mode of splitting twenty schedules across five files.
func TestEveryScheduledDispatcherIsWiredExactlyOnce(t *testing.T) {
	wired := periodicSiteKinds(t)

	scheduled := 0
	for kind, spec := range jobs.Declared() {
		wantSite := spec.Role == jobs.Dispatcher && !spec.Cadence.OnDemand
		if !wantSite {
			if wired[kind] > 0 {
				t.Errorf("%s has %d periodicFor site(s) but declares no schedule to place", kind, wired[kind])
			}
			continue
		}
		scheduled++
		if wired[kind] != 1 {
			t.Errorf("%s has %d periodicFor site(s), want exactly 1 — a declared cadence nothing wires never ticks", kind, wired[kind])
		}
	}
	if scheduled < scheduledDispatcherFloor {
		t.Fatalf("only %d dispatchers declare a cadence, under the floor of %d — this gate is no longer reading the contract",
			scheduled, scheduledDispatcherFloor)
	}
}
