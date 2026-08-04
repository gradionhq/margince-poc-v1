// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

import (
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/platform/jobs"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// TestJobHealthRefusesAnAgentPrincipal — the payload carries operational
// failure text and a fleet-wide view of the dispatchers. An admin-minted
// read-scoped passport satisfies every object grant, so human-only has to
// be asserted here rather than inferred from RBAC.
func TestJobHealthRefusesAnAgentPrincipal(t *testing.T) {
	rec := callJobHealth(t, principal.Principal{
		Type:        principal.PrincipalAgent,
		Permissions: principal.Permissions{RoleKeys: []string{"admin"}},
	})

	if rec.Code != http.StatusForbidden {
		t.Errorf("an agent principal got %d, want 403: a passport must not read this", rec.Code)
	}
}

// TestJobHealthRefusesANonAdminHuman — every seat can see its own records;
// what the background system is holding for the whole workspace is an
// administrator's view.
func TestJobHealthRefusesANonAdminHuman(t *testing.T) {
	rec := callJobHealth(t, principal.Principal{
		Type:        principal.PrincipalHuman,
		Permissions: principal.Permissions{RoleKeys: []string{"rep"}},
	})

	if rec.Code != http.StatusForbidden {
		t.Errorf("a non-admin human got %d, want 403", rec.Code)
	}
}

// TestJobHealthRefusesAnUnauthenticatedCall — no principal in context at
// all must not reach the pool.
func TestJobHealthRefusesAnUnauthenticatedCall(t *testing.T) {
	rec := httptest.NewRecorder()
	jobHealthHandlers{}.GetJobHealth(rec, httptest.NewRequest(http.MethodGet, "/v1/admin/job-health", nil))

	// 403 exactly, not merely "not 200": a 500 would also fail a != 200
	// check while meaning the handler crashed on its way to a refusal.
	// The contract's 401 is the session middleware's answer and is proved
	// over the real wire, in the integration lane.
	if rec.Code != http.StatusForbidden {
		t.Errorf("an unauthenticated call got %d, want 403 from the gate", rec.Code)
	}
}

// TestTheJobHealthHandlerIsConstructedWithAPoolNotJustEmbedded — embedding
// alone leaves the zero value's nil pool in place, and every authenticated
// request then panics on the first query. There is no nil-pool branch in
// the handler on purpose; this is what stands in its place.
func TestTheJobHealthHandlerIsConstructedWithAPoolNotJustEmbedded(t *testing.T) {
	srv := newServer(nil, quietTestLogger(), authHandlers{}, dealsHandlers{})
	if srv.jobHealthHandlers.pool != nil {
		t.Fatal("the fixture passed a pool; this test can no longer tell construction from embedding")
	}

	// The real assertion: the composed server threads ITS pool through, so
	// a handler constructed from a live pool holds that same pool.
	live := &pgxpool.Pool{}
	withPool := newServer(live, quietTestLogger(), authHandlers{}, dealsHandlers{})
	if withPool.jobHealthHandlers.pool != live {
		t.Error("newServer embedded jobHealthHandlers without constructing it; every " +
			"authenticated read would reach a nil pool")
	}
}

// TestOnlyAVettedSentenceReachesTheWire is the fail-closed half of the
// failure-text posture: a worker that bypassed jobs.Fault stored its raw
// cause in a column with no RLS and no redaction path, and this is what
// stops that text travelling.
func TestOnlyAVettedSentenceReachesTheWire(t *testing.T) {
	vetted := "the record this job names no longer exists"
	if got := reasonFor(vetted); got != vetted {
		t.Errorf("reasonFor(vetted) = %q, want it passed through", got)
	}

	for _, raw := range []string{
		`smtp: 550 5.1.1 <someone@example.com>: recipient rejected`,
		"dial tcp 10.0.0.4:5432: connect: connection refused",
		// River's own rescuer text. It must be substituted like any other
		// unvetted string — and the substitute must not tell an operator to
		// read a process log that died before writing one.
		"Stuck job rescued by JobRescuer",
	} {
		got := reasonFor(raw)
		if got == raw {
			t.Errorf("a worker's raw cause reached the wire: %q", raw)
		}
		if got != unvettedFailureReason {
			t.Errorf("reasonFor(%q) = %q, want the one fixed substitute", raw, got)
		}
		if strings.Contains(got, "process log") {
			t.Errorf("the substitute promises a process log: a rescued job's process died "+
				"mid-work and wrote none. got %q", got)
		}
	}
}

// TestARowThatRecordedNoCauseDoesNotClaimItFailed — a cancelled job that
// never ran records no attempt error. Rendering that as "the job failed for
// a reason this surface cannot vet" asserts a failure that did not happen
// and points an operator at a log line nobody wrote. Nothing recorded and
// something unreadable are different facts.
func TestARowThatRecordedNoCauseDoesNotClaimItFailed(t *testing.T) {
	got := reasonFor("")

	if got == "" {
		t.Fatal("an empty stored error must still produce a sentence")
	}
	if got == unvettedFailureReason {
		t.Error("a row with no recorded cause was reported as an unvettable failure")
	}
	if strings.Contains(got, "failed") {
		t.Errorf("reasonFor(\"\") = %q: it claims a failure that was never recorded", got)
	}
	if got != noRecordedCause {
		t.Errorf("reasonFor(\"\") = %q, want the one fixed no-cause sentence", got)
	}
}

// TestTheResponseNeverCarriesAWorkspaceOtherThanTheCallersOwn — the scope
// admits the caller's rows and untenanted ones and nothing else, so the
// mapping must not invent a third answer from a jsonb value nothing
// constrains.
func TestTheResponseNeverCarriesAWorkspaceOtherThanTheCallersOwn(t *testing.T) {
	caller := ids.NewV7()
	// A stored value that is NOT the caller's — the shape a malformed row
	// would have if one ever slipped past the scope.
	someoneElse := ids.NewV7().String()

	got := jobHealthResponse(caller, jobs.Health{Failures: []jobs.Failure{
		{Kind: "tenant_pass", WorkspaceID: &someoneElse, State: "discarded"},
		{Kind: "the_dispatcher", WorkspaceID: nil, State: "discarded"},
	}})

	if len(got.RecentFailures) != 2 {
		t.Fatalf("mapped %d failures, want 2", len(got.RecentFailures))
	}
	if got.RecentFailures[0].WorkspaceId == nil {
		t.Fatal("a tenant failure lost its workspace")
	}
	if ids.UUID(*got.RecentFailures[0].WorkspaceId) != caller {
		t.Errorf("the response named workspace %s, want the caller's %s",
			*got.RecentFailures[0].WorkspaceId, caller)
	}
	if got.RecentFailures[1].WorkspaceId != nil {
		t.Error("a dispatcher failure was given a workspace it does not have")
	}
}

// TestAnAbsentOldestAgeStaysAbsent — null and zero are different claims.
// Null means nothing of this kind is runnable; zero means something became
// runnable a moment ago, and flattening the two hides an idle kind behind
// a healthy-looking number.
func TestAnAbsentOldestAgeStaysAbsent(t *testing.T) {
	measured := 41.7
	got := jobHealthResponse(ids.NewV7(), jobs.Health{Kinds: []jobs.KindHealth{
		{Kind: "idle", OldestWaitingAgeSeconds: nil},
		{Kind: "waiting", OldestWaitingAgeSeconds: &measured},
	}})

	if got.Kinds[0].OldestWaitingAgeSeconds != nil {
		t.Errorf("an unmeasured age became %d", *got.Kinds[0].OldestWaitingAgeSeconds)
	}
	if got.Kinds[1].OldestWaitingAgeSeconds == nil {
		t.Fatal("a measured age was dropped")
	}
	if *got.Kinds[1].OldestWaitingAgeSeconds != 41 {
		t.Errorf("age = %d, want 41", *got.Kinds[1].OldestWaitingAgeSeconds)
	}
}

// TestAnIdleFleetMapsToEmptyListsNotNulls — the contract requires both
// arrays, and a JSON null where a list belongs breaks a client that
// iterates it.
func TestAnIdleFleetMapsToEmptyListsNotNulls(t *testing.T) {
	got := jobHealthResponse(ids.NewV7(), jobs.Health{})

	if got.Kinds == nil {
		t.Error("kinds serialized as null rather than []")
	}
	if got.RecentFailures == nil {
		t.Error("recent_failures serialized as null rather than []")
	}
	if got.GeneratedAt.IsZero() {
		t.Error("generated_at was never stamped")
	}
}

// TestTheUntenantedArmResolvesItsKindsFromTheDeclaredRole gates the FILTER,
// and only the filter. Both sides here read jobs.Declared(), so this cannot
// catch a kind whose role is wrong — it catches dispatcherKinds() growing a
// hard-coded entry, dropping one, or answering by some predicate other than
// the declared role, which is what would put a kind in the untenanted arm or
// leave it out for a reason api/jobs.yaml never states.
//
// What holds the ROLE itself to something outside the file is the generated
// pair of interface assertions: a declared dispatcher must satisfy
// jobs.FleetWide and a declared workspace kind jobs.WorkspaceScoped, so a role
// that disagreed with the args struct it names fails to compile. That is the
// independent binding; this is the filter in front of it.
func TestTheUntenantedArmResolvesItsKindsFromTheDeclaredRole(t *testing.T) {
	var want []string
	for kind, spec := range jobs.Declared() {
		if spec.Role == jobs.Dispatcher {
			want = append(want, kind)
		}
	}
	// A vacuous pass is the failure mode of any derived gate. The contract
	// declares 24 dispatchers today; the floor sits below that so retiring a
	// pass does not drag the gate along, while a filter that matched nothing
	// still trips it.
	if len(want) < 20 {
		t.Fatalf("the contract declares only %d dispatchers; the filter is not resolving them", len(want))
	}

	got := slices.Clone(dispatcherKinds())
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Errorf("the untenanted arm admits\n%v\nbut the contract declares\n%v", got, want)
	}
}

// TestEveryDispatcherKindIsSpelledOnce — a duplicate would widen the
// untenanted arm with a redundant bind, and an empty kind would widen it
// with a bind that matches nothing while looking like one that does.
func TestEveryDispatcherKindIsSpelledOnce(t *testing.T) {
	kinds := dispatcherKinds()
	sorted := slices.Clone(kinds)
	slices.Sort(sorted)
	if len(slices.Compact(sorted)) != len(kinds) {
		t.Errorf("dispatcherKinds repeats a kind: %v", kinds)
	}
	for _, k := range kinds {
		if k == "" {
			t.Error("a dispatcher declared an empty kind")
		}
	}
}

// TestNoWorkspaceKindReachesTheUntenantedArm is the other direction, and
// the one with a security shape: every row of a kind named here is admitted
// without a workspace test, so a workspace kind on this list would hand one
// tenant's rows to another's admin.
func TestNoWorkspaceKindReachesTheUntenantedArm(t *testing.T) {
	for _, kind := range dispatcherKinds() {
		spec, ok := jobs.SpecFor(kind)
		if !ok {
			t.Errorf("%s is admitted untenanted but the contract declares no such kind", kind)
			continue
		}
		if spec.Role != jobs.Dispatcher {
			t.Errorf("%s carries one workspace's pass but is admitted as untenanted, so its "+
				"rows reach an admin of a workspace that does not own them", kind)
		}
	}
}

// callJobHealth runs the handler under one principal and answers the
// recorded response. The pool is nil on purpose: every case here is
// refused before the read, and a case that reached the pool would panic
// rather than pass quietly.
func callJobHealth(t *testing.T, p principal.Principal) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/v1/admin/job-health", nil)
	ctx := principal.WithActor(req.Context(), p)
	rec := httptest.NewRecorder()
	jobHealthHandlers{pool: nil}.GetJobHealth(rec, req.WithContext(ctx))
	return rec
}

// TestTheJobHealthReadTimeoutIsABudgetNotAnAbsentBound checks the CONSTANT,
// not that the handler applies it.
//
// The name says so because the distinction matters: a refactor that dropped
// the context.WithTimeout call while keeping the constant would leave this
// test green and the property false. Proving the application at runtime
// needs the read behind an injectable seam, which this handler has no
// production reason to grow — so what guards it is that the call sits one
// line from the constant, and that GetJobHealth's own doc comment says why
// it is there.
//
// What this DOES catch is the constant being zeroed or widened into
// meaninglessness, which is the realistic regression: the exposition
// endpoint bounds its read of this same unindexed table at 2s precisely to
// stop a scan holding a request thread and a pool connection, and a bound
// that is not a bound would let the two readers drift apart again.
func TestTheJobHealthReadTimeoutIsABudgetNotAnAbsentBound(t *testing.T) {
	if jobHealthReadTimeout <= 0 {
		t.Fatalf("jobHealthReadTimeout = %v; an unbounded read is what the budget exists "+
			"to prevent", jobHealthReadTimeout)
	}
	// Generous enough for an interactive page, but still a budget: a read
	// that cannot finish inside it is a signal, not something to wait out.
	if jobHealthReadTimeout > 30*time.Second {
		t.Errorf("jobHealthReadTimeout = %v, which is long enough to be indistinguishable "+
			"from no bound at all", jobHealthReadTimeout)
	}
}
