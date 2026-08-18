// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// GET /admin/job-health over the real wire. river_job has no workspace
// column and therefore no RLS, so the scope is the handler's own — which
// means a test that only ever creates one workspace cannot tell a working
// filter from a missing one. Every case here seeds a SECOND workspace's
// rows and asserts their absence.

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"strings"
	"testing"
	"time"

	"github.com/gradionhq/margince/backend/internal/compose/integration/apptest"
	"github.com/gradionhq/margince/backend/internal/platform/jobs"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

type jobHealthDTO struct {
	GeneratedAt string `json:"generated_at"`
	Kinds       []struct {
		Kind                    string `json:"kind"`
		Queue                   string `json:"queue"`
		FleetWide               bool   `json:"fleet_wide"`
		Waiting                 int    `json:"waiting"`
		Running                 int    `json:"running"`
		Retrying                int    `json:"retrying"`
		Dead                    int    `json:"dead"`
		OldestWaitingAgeSeconds *int   `json:"oldest_waiting_age_seconds"`
	} `json:"kinds"`
	RecentFailures []struct {
		Kind          string  `json:"kind"`
		State         string  `json:"state"`
		Attempt       int     `json:"attempt"`
		MaxAttempts   int     `json:"max_attempts"`
		FailedAt      string  `json:"failed_at"`
		Reason        string  `json:"reason"`
		JobID         *int64  `json:"job_id"`
		FirstFailedAt *string `json:"first_failed_at"`
		FailureClass  *string `json:"failure_class"`
		Remedy        *string `json:"remedy"`
	} `json:"recent_failures"`
}

// riverRow is one raw river_job row a scenario needs in a state or with
// stored error text that a real enqueue cannot produce on demand.
type riverRow struct {
	kind      string
	queue     string
	state     string
	workspace string // empty writes NO workspace_id key — a dispatcher
	attempt   int
	errorText string // the newest attempt's stored message; empty writes no errors
	trace     string // a panic stack, so a test can prove it never travels
	// firstErrorAt, when set, prepends an EARLIER attempt error stamped at
	// that moment. It exists because errors is append-ordered and the read
	// takes its "failing since" from element 1 — a fixture with a single
	// element cannot tell a correct read from one that took the newest.
	firstErrorAt time.Time
}

// seedRiverRow writes one row through the OWNER connection: river_job has
// no RLS, and this fixture deliberately writes rows the app would refuse to
// create, including another workspace's.
func seedRiverRow(t *testing.T, e *apptest.AppEnv, r riverRow) {
	t.Helper()
	ctx := context.Background()

	queue := r.queue
	if queue == "" {
		queue = "default"
	}
	args := map[string]any{}
	if r.workspace != "" {
		args["workspace_id"] = r.workspace
	}
	encodedArgs, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("encoding fixture args: %v", err)
	}

	encodedErrors := [][]byte{}
	if !r.firstErrorAt.IsZero() {
		encodedErrors = append(encodedErrors,
			encodeAttemptError(t, r.firstErrorAt, 1, r.errorText, r.trace))
	}
	if r.errorText != "" {
		encodedErrors = append(encodedErrors,
			encodeAttemptError(t, time.Now().UTC(), r.attempt, r.errorText, r.trace))
	}

	// river_job's finalized_or_finalized_at_null CHECK refuses a finalized
	// state with a null finalized_at, so the fixture stamps it rather than
	// failing at the database instead of at the assertion.
	var finalizedAt *time.Time
	if r.state == "completed" || r.state == "cancelled" || r.state == "discarded" {
		now := time.Now()
		finalizedAt = &now
	}

	if _, err := e.Owner.Exec(ctx, `
		INSERT INTO river_job
		    (state, kind, queue, args, tags, errors, max_attempts, attempt,
		     created_at, scheduled_at, finalized_at)
		VALUES ($1::river_job_state, $2, $3, $4::jsonb, '{}'::varchar(255)[],
		        $5::jsonb[], 3, $6, now(), now() - interval '10 minutes', $7)`,
		r.state, r.kind, queue, encodedArgs, encodedErrors, r.attempt, finalizedAt); err != nil {
		t.Fatalf("seeding a %s %s row: %v", r.state, r.kind, err)
	}
}

// encodeAttemptError writes one element of river_job.errors in River's own
// AttemptError shape, so the read under test parses what production parses
// rather than what a fixture found convenient.
func encodeAttemptError(t *testing.T, at time.Time, attempt int, text, trace string) []byte {
	t.Helper()
	element, err := json.Marshal(map[string]any{
		"at": at.UTC().Format(time.RFC3339Nano), "attempt": attempt,
		"error": text, "trace": trace,
	})
	if err != nil {
		t.Fatalf("encoding fixture attempt error: %v", err)
	}
	return element
}

// callerWorkspace answers the id of the workspace the bootstrapped admin
// session belongs to — the one the endpoint must scope itself to.
func callerWorkspace(t *testing.T, e *apptest.AppEnv) string {
	t.Helper()
	var id string
	if err := e.Owner.QueryRow(context.Background(),
		`SELECT id FROM workspace WHERE slug = $1`, e.Slug).Scan(&id); err != nil {
		t.Fatalf("resolving the caller's workspace: %v", err)
	}
	return id
}

// kindOf finds one reported kind, and says so when it is absent — the
// absence is the assertion in half these cases.
func kindOf(report jobHealthDTO, kind string) (int, bool) {
	for i, k := range report.Kinds {
		if k.Kind == kind {
			return i, true
		}
	}
	return 0, false
}

// TestJobHealthReportsThisWorkspacesDeadWorkAndNotAnotherWorkspacesAnything
// is the case that matters most. The scope is imposed by the handler
// because river_job has no RLS to impose it, so a second workspace has to
// be present or the test proves nothing.
func TestJobHealthReportsThisWorkspacesDeadWorkAndNotAnotherWorkspacesAnything(t *testing.T) {
	e := apptest.SetupApp(t)
	e.BootstrapWorkspace(t)
	mine := callerWorkspace(t, e)
	theirs := ids.NewV7().String()

	seedRiverRow(t, e, riverRow{
		kind: "idempotency_retention_workspace", state: "discarded",
		workspace: mine, attempt: 3, errorText: "the record this job names no longer exists",
	})
	seedRiverRow(t, e, riverRow{
		kind: "idempotency_retention_workspace", state: "available",
		workspace: mine,
	})
	// Another tenant's dead work, of a DIFFERENT kind so its presence would
	// be unmistakable rather than folded into a count.
	seedRiverRow(t, e, riverRow{
		kind: "someone_elses_pass", state: "discarded",
		workspace: theirs, attempt: 3, errorText: "another tenant's failure",
	})
	// A dispatcher: no workspace at all, and a kind the closed untenanted
	// arm declares.
	seedRiverRow(t, e, riverRow{
		kind: "idempotency_retention", state: "available",
	})
	// An untenanted row whose kind is NOT a declared dispatcher. The arm is
	// fail-closed, so it must be omitted rather than shared with every admin.
	seedRiverRow(t, e, riverRow{kind: "not_a_declared_dispatcher", state: "discarded", attempt: 3})

	var report jobHealthDTO
	if status := e.Call(t, "GET", "/v1/admin/job-health", nil, nil, &report); status != http.StatusOK {
		t.Fatalf("GET /admin/job-health → %d, want 200", status)
	}

	i, ok := kindOf(report, "idempotency_retention_workspace")
	if !ok {
		t.Fatalf("the caller's own failing kind is missing from %+v", report.Kinds)
	}
	if report.Kinds[i].Dead != 1 {
		t.Errorf("dead = %d, want 1", report.Kinds[i].Dead)
	}
	if report.Kinds[i].Waiting != 1 {
		t.Errorf("waiting = %d, want 1", report.Kinds[i].Waiting)
	}
	if report.Kinds[i].FleetWide {
		t.Error("a workspace-scoped kind was reported as fleet-wide")
	}

	if _, ok := kindOf(report, "someone_elses_pass"); ok {
		t.Error("another workspace's job kind reached this workspace's admin — river_job has " +
			"no RLS, so nothing but this handler was ever going to stop that")
	}
	if _, ok := kindOf(report, "not_a_declared_dispatcher"); ok {
		t.Error("an untenanted row of an undeclared kind was admitted; the arm must be " +
			"closed against the declared dispatcher kinds, not open to every null")
	}

	d, ok := kindOf(report, "idempotency_retention")
	if !ok {
		t.Fatal("the dispatcher is missing: a dispatcher that is not running means no " +
			"workspace is being swept at all, which is what an admin comes here to see")
	}
	if !report.Kinds[d].FleetWide {
		t.Error("the dispatcher was not reported as fleet-wide")
	}

	for _, f := range report.RecentFailures {
		if f.Kind == "someone_elses_pass" {
			t.Error("another workspace's failure reached the failure list")
		}
	}
}

// TestJobHealthReportsAVettedSentenceForAFaultedWorkerAndSubstitutesForARawOne
// — the column is fleet-visible with no RLS and no redaction path, so a raw
// provider cause must not travel, and the panic stack River stores beside
// it must not either.
func TestJobHealthReportsAVettedSentenceForAFaultedWorkerAndSubstitutesForARawOne(t *testing.T) {
	e := apptest.SetupApp(t)
	e.BootstrapWorkspace(t)
	mine := callerWorkspace(t, e)

	vetted := "the record this job names no longer exists"
	raw := `smtp: 550 5.1.1 <someone@example.com>: recipient rejected`
	stack := "goroutine 1 [running]:\nmain.secretInternals(0xdeadbeef)"

	seedRiverRow(t, e, riverRow{
		kind: "faulted_pass", state: "discarded",
		workspace: mine, attempt: 3, errorText: vetted, trace: stack,
	})
	seedRiverRow(t, e, riverRow{
		kind: "raw_pass", state: "discarded",
		workspace: mine, attempt: 3, errorText: raw, trace: stack,
	})

	var report jobHealthDTO
	if status := e.Call(t, "GET", "/v1/admin/job-health", nil, nil, &report); status != http.StatusOK {
		t.Fatalf("GET /admin/job-health → %d, want 200", status)
	}

	var sawVetted, sawRawKind bool
	for _, f := range report.RecentFailures {
		switch f.Kind {
		case "faulted_pass":
			sawVetted = true
			if f.Reason != vetted {
				t.Errorf("a vetted sentence was substituted away: got %q, want %q", f.Reason, vetted)
			}
		case "raw_pass":
			sawRawKind = true
			if f.Reason == raw {
				t.Error("a worker's raw cause reached the wire, naming the address a " +
					"provider refused")
			}
		}
		if f.Reason == stack {
			t.Error("River's stored panic trace reached the wire")
		}
	}
	if !sawVetted || !sawRawKind {
		t.Fatalf("expected both failures in the report, got %+v", report.RecentFailures)
	}

	// The bytes the server actually sent, NOT a re-marshalled DTO. Encoding
	// the parsed struct would prove only that the fields this test declares
	// are clean — every field it does not declare was silently dropped at
	// decode, which is exactly where an unnoticed leak would live.
	body := rawGet(t, e, "/v1/admin/job-health")
	for _, forbidden := range []string{"goroutine", "secretInternals", "someone@example.com", "trace"} {
		if containsIgnoringCase(body, forbidden) {
			t.Errorf("the response body carries %q; only the attempt error's MESSAGE may be "+
				"read, never the element, and only when vetted.\nbody: %s", forbidden, body)
		}
	}
}

// TestJobHealthCountsACancelledRowThatCarriesNoAttemptErrorAtAll — River's
// cancel path appends nothing to errors, so a terminal row can have no
// attempt error while the contract still requires failed_at and reason.
// Scanning a null into either turns the endpoint into a 500.
func TestJobHealthCountsACancelledRowThatCarriesNoAttemptErrorAtAll(t *testing.T) {
	e := apptest.SetupApp(t)
	e.BootstrapWorkspace(t)
	mine := callerWorkspace(t, e)

	seedRiverRow(t, e, riverRow{
		kind: "cancelled_pass", state: "cancelled", workspace: mine,
	})
	var report jobHealthDTO
	if status := e.Call(t, "GET", "/v1/admin/job-health", nil, nil, &report); status != http.StatusOK {
		t.Fatalf("GET /admin/job-health → %d, want 200: a cancelled row with no attempt "+
			"error must not fail the read", status)
	}

	i, ok := kindOf(report, "cancelled_pass")
	if !ok {
		t.Fatalf("the cancelled kind is missing from %+v", report.Kinds)
	}
	if report.Kinds[i].Dead != 1 {
		t.Errorf("dead = %d, want 1: a cancelled pass did not run", report.Kinds[i].Dead)
	}

	var found bool
	for _, f := range report.RecentFailures {
		if f.Kind != "cancelled_pass" {
			continue
		}
		found = true
		if f.FailedAt == "" {
			t.Error("failed_at is empty; finalized_at is the fallback when no attempt error exists")
		}
		if f.Reason == "" {
			t.Error("reason is empty; a row with no stored text still needs a sentence")
		}
	}
	if !found {
		t.Error("the cancelled row is absent from the failure list")
	}
}

// TestJobHealthRefusesAPassportOverHTTP proves the wire, not just the gate
// function: the payload carries operational failure text and a fleet-wide
// view of the dispatchers, and a read-scoped passport satisfies every
// object grant an admin could mint.
func TestJobHealthRefusesAPassportOverHTTP(t *testing.T) {
	e := apptest.SetupApp(t)
	e.BootstrapWorkspace(t)

	var minted struct {
		Token string `json:"token"`
	}
	if status := e.Call(t, "POST", "/v1/passports", apptest.AnyMap{
		"label": "job health probe", "scopes": []string{"read"},
	}, nil, &minted); status != http.StatusCreated {
		t.Fatalf("issue passport → %d", status)
	}

	var problem struct {
		Code string `json:"code"`
	}
	status := e.Call(t, "GET", "/v1/admin/job-health", nil,
		map[string]string{"Authorization": "Bearer " + minted.Token}, &problem)
	if status != http.StatusForbidden {
		t.Errorf("agent GET /admin/job-health → %d, want 403 (the contract declares it human-only)", status)
	}
	if problem.Code != "permission_denied" {
		t.Errorf("code %q, want permission_denied", problem.Code)
	}
}

// TestJobHealthOnAnIdleFleetIsAnEmptyReportNotAnError — the honest empty
// case. An installation with nothing queued is a real state, and a client
// must be able to tell it from a failure.
func TestJobHealthOnAnIdleFleetIsAnEmptyReportNotAnError(t *testing.T) {
	e := apptest.SetupApp(t)
	e.BootstrapWorkspace(t)

	var report jobHealthDTO
	if status := e.Call(t, "GET", "/v1/admin/job-health", nil, nil, &report); status != http.StatusOK {
		t.Fatalf("GET /admin/job-health on an idle fleet → %d, want 200", status)
	}
	if report.GeneratedAt == "" {
		t.Error("an idle report still says when it was generated")
	}
	for _, k := range report.Kinds {
		if k.FleetWide {
			continue
		}
		t.Errorf("an idle fleet reported tenant work: %+v", k)
	}
}

// TestJobHealthIgnoresTheSweepTagWhenScopingRows — the sweep tag is the
// metric read's filter, never this one's. The two surfaces read the same
// table through different scopes, and a tagged row must reach the admin
// read exactly as an untagged one does.
func TestJobHealthIgnoresTheSweepTagWhenScopingRows(t *testing.T) {
	e := apptest.SetupApp(t)
	e.BootstrapWorkspace(t)
	mine := callerWorkspace(t, e)

	seedRiverRow(t, e, riverRow{kind: "tagged_pass", state: "discarded", workspace: mine, attempt: 3})
	if _, err := e.Owner.Exec(context.Background(),
		`UPDATE river_job SET tags = ARRAY[$1]::varchar(255)[] WHERE kind = 'tagged_pass'`,
		jobs.SweepTag); err != nil {
		t.Fatalf("tagging the fixture row: %v", err)
	}

	var report jobHealthDTO
	if status := e.Call(t, "GET", "/v1/admin/job-health", nil, nil, &report); status != http.StatusOK {
		t.Fatalf("GET /admin/job-health → %d, want 200", status)
	}
	i, ok := kindOf(report, "tagged_pass")
	if !ok {
		t.Fatal("a sweep-tagged row vanished from the admin read; the endpoint scopes by " +
			"workspace, never by tag")
	}
	if report.Kinds[i].Dead != 1 {
		t.Errorf("dead = %d, want 1", report.Kinds[i].Dead)
	}
}

// rawGet answers the response body VERBATIM, so a leak assertion reads
// what the server sent rather than what a test DTO chose to keep. env.call
// decodes into a typed struct, which drops every field the struct does not
// declare — and an undeclared field is precisely where an unnoticed leak
// would sit.
func rawGet(t *testing.T, e *apptest.AppEnv, path string) string {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, e.TS.URL+path, nil)
	if err != nil {
		t.Fatalf("building request: %v", err)
	}
	resp, err := e.Client.Do(req) //nolint:bodyclose // closed by apptest.CloseBody below; bodyclose only recognises a Close in the same package
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	defer apptest.CloseBody(t, resp)
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading response: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s → %d, want 200", path, resp.StatusCode)
	}
	return string(raw)
}

// containsIgnoringCase keeps the leak assertions above from passing on a
// difference of case alone.
func containsIgnoringCase(haystack, needle string) bool {
	return strings.Contains(strings.ToLower(haystack), strings.ToLower(needle))
}

// TestJobHealthCapsTheFailureListAndOrdersItNewestFirst — recent_failures
// is a bounded view, not a log. Without the cap an installation with a
// week of discarded rows would serve all of them; without the order the
// "recent" in the field name is a lie, and the 50 an operator sees would
// be an arbitrary 50 rather than the newest.
func TestJobHealthCapsTheFailureListAndOrdersItNewestFirst(t *testing.T) {
	e := apptest.SetupApp(t)
	e.BootstrapWorkspace(t)
	mine := callerWorkspace(t, e)

	// 60 failures, finalized at ascending times, so the newest are the
	// highest-numbered and a correct read returns exactly those.
	const seeded = 60
	for i := range seeded {
		seedRiverRow(t, e, riverRow{
			kind: "capped_pass", state: "discarded", workspace: mine, attempt: 3,
		})
		if _, err := e.Owner.Exec(context.Background(),
			`UPDATE river_job SET finalized_at = now() - make_interval(mins => $1)
			 WHERE kind = 'capped_pass' AND finalized_at IS NOT NULL
			   AND id = (SELECT max(id) FROM river_job WHERE kind = 'capped_pass')`,
			seeded-i); err != nil {
			t.Fatalf("ageing fixture row %d: %v", i, err)
		}
	}

	var report jobHealthDTO
	if status := e.Call(t, "GET", "/v1/admin/job-health", nil, nil, &report); status != http.StatusOK {
		t.Fatalf("GET /admin/job-health → %d, want 200", status)
	}

	if len(report.RecentFailures) != 50 {
		t.Errorf("recent_failures has %d entries, want the declared cap of 50 out of %d seeded",
			len(report.RecentFailures), seeded)
	}
	// Compared as TIMES, never as strings. Go serializes time.Time with a
	// variable-width fractional part and trims trailing zeros, so a value
	// landing on a whole second carries no fraction at all — and '.' sorts
	// before 'Z', which makes lexicographic order disagree with chronological
	// order for exactly those pairs. The test would then fail on correct
	// ordering, or pass on wrong ordering.
	for i := 1; i < len(report.RecentFailures); i++ {
		prev := parseFailedAt(t, report.RecentFailures[i-1].FailedAt)
		cur := parseFailedAt(t, report.RecentFailures[i].FailedAt)
		if prev.Before(cur) {
			t.Fatalf("failure %d (%s) is newer than failure %d (%s): the list must be "+
				"newest-first, or the 50 an operator sees are an arbitrary 50",
				i, cur, i-1, prev)
		}
	}
	// The newest seeded row is one minute old; the oldest is an hour. A
	// correct cap keeps the newest end, so the oldest ten must be absent.
	oldest := parseFailedAt(t, report.RecentFailures[len(report.RecentFailures)-1].FailedAt)
	cutoff := time.Now().Add(-51 * time.Minute)
	if oldest.Before(cutoff) {
		t.Errorf("the retained window reaches back to %s, past %s: the cap kept the OLDEST "+
			"rows rather than the newest", oldest, cutoff)
	}
}

// parseFailedAt reads a wire timestamp as a time, so ordering assertions
// compare instants rather than spellings.
func parseFailedAt(t *testing.T, raw string) time.Time {
	t.Helper()
	at, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		t.Fatalf("failed_at %q is not RFC 3339: %v", raw, err)
	}
	return at
}

// TestJobHealthRefusesAnUnauthenticatedCallOverHTTP — the contract declares
// 401 for a call with no session, and that answer comes from the session
// middleware rather than from the handler, so only the real wire proves it.
func TestJobHealthRefusesAnUnauthenticatedCallOverHTTP(t *testing.T) {
	e := apptest.SetupApp(t)
	// Bootstrapped, so the installation EXISTS — an unbootstrapped one
	// answers 503 for every route and would prove nothing about this
	// endpoint — and then stripped of its session cookie, so the request
	// carries no credential of any kind.
	e.BootstrapWorkspace(t)
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatalf("fresh cookie jar: %v", err)
	}
	e.Client.Jar = jar

	var problem struct {
		Code string `json:"code"`
	}
	status := e.Call(t, "GET", "/v1/admin/job-health", nil, nil, &problem)
	if status != http.StatusUnauthorized {
		t.Errorf("unauthenticated GET /admin/job-health → %d, want 401", status)
	}
}

// failureOf finds one reported failure by kind, and says so when it is absent —
// an assertion loop that never enters its arm reports green on a dropped row.
func failureOf(t *testing.T, report jobHealthDTO, kind string) int {
	t.Helper()
	for i, f := range report.RecentFailures {
		if f.Kind == kind {
			return i
		}
	}
	t.Fatalf("no failure of kind %s in %+v", kind, report.RecentFailures)
	return 0
}

// TestJobHealthNamesTheRowItIsTalkingAboutAndWhenItFirstFailed — the surface
// told an operator to read the process log while naming no line to find. River
// logs job_id, and every psql follow-up is keyed by it, so the id is what makes
// the rest of the row actionable. "Failing since 21:08" is the other half: the
// attempt counter says which rung of the ladder the job is on, never how long it
// has been on it.
func TestJobHealthNamesTheRowItIsTalkingAboutAndWhenItFirstFailed(t *testing.T) {
	e := apptest.SetupApp(t)
	e.BootstrapWorkspace(t)
	mine := callerWorkspace(t, e)
	firstFailure := time.Now().UTC().Add(-40 * time.Minute)

	seedRiverRow(t, e, riverRow{
		kind: "twice_failed_pass", state: "retryable", workspace: mine, attempt: 2,
		errorText: "the record this job names no longer exists", firstErrorAt: firstFailure,
	})

	var report jobHealthDTO
	if status := e.Call(t, "GET", "/v1/admin/job-health", nil, nil, &report); status != http.StatusOK {
		t.Fatalf("GET /admin/job-health → %d, want 200", status)
	}
	failure := report.RecentFailures[failureOf(t, report, "twice_failed_pass")]

	// The id is compared against the row's OWN id, not merely asserted
	// non-zero: a mapping that carried the attempt count or an array index
	// would satisfy "present" while naming a line that does not exist.
	var stored int64
	if err := e.Owner.QueryRow(context.Background(),
		`SELECT id FROM river_job WHERE kind = 'twice_failed_pass'`).Scan(&stored); err != nil {
		t.Fatalf("reading the fixture row's id: %v", err)
	}
	if failure.JobID == nil {
		t.Fatal("job_id is absent; the failure names no row, so the process log it points at names no line")
	}
	if *failure.JobID != stored {
		t.Errorf("job_id = %d, want river_job.id %d", *failure.JobID, stored)
	}

	if failure.FirstFailedAt == nil {
		t.Fatal("first_failed_at is absent for a row that recorded two attempt errors")
	}
	firstAt := parseFailedAt(t, *failure.FirstFailedAt)
	// Within a minute of the seeded moment, and materially BEFORE the latest
	// failure: a read that took the newest element instead of the first would
	// report the two as the same instant.
	if firstAt.Sub(firstFailure).Abs() > time.Minute {
		t.Errorf("first_failed_at = %s, want the FIRST attempt error's own moment %s — errors is "+
			"append-ordered, so element 1 is the oldest", firstAt, firstFailure)
	}
	if !firstAt.Before(parseFailedAt(t, failure.FailedAt).Add(-time.Minute)) {
		t.Errorf("first_failed_at %s is not meaningfully before failed_at %s; the surface cannot "+
			"tell failing-since from failed-once", firstAt, failure.FailedAt)
	}
}

// TestJobHealthCarriesTheClassAndRemedyForAClassifiedFailure — a sentence alone
// leaves an operator to decide whether to retry, wait, or go fix something. The
// class is what an alert matches; the remedy is the half a failure list is
// useless without.
func TestJobHealthCarriesTheClassAndRemedyForAClassifiedFailure(t *testing.T) {
	e := apptest.SetupApp(t)
	e.BootstrapWorkspace(t)
	mine := callerWorkspace(t, e)

	seedRiverRow(t, e, riverRow{
		kind: "classified_pass", state: "discarded", workspace: mine, attempt: 3,
		errorText: "the record this job names no longer exists",
	})
	// A raw cause the surface cannot vet, and a cancelled row that recorded no
	// cause at all: neither may be given a class, and inventing one would key
	// an operator's alert on a guess.
	seedRiverRow(t, e, riverRow{
		kind: "unvetted_pass", state: "discarded", workspace: mine, attempt: 3,
		errorText: `smtp: 550 5.1.1 <someone@example.com>: recipient rejected`,
	})
	seedRiverRow(t, e, riverRow{kind: "no_cause_pass", state: "cancelled", workspace: mine})

	var report jobHealthDTO
	if status := e.Call(t, "GET", "/v1/admin/job-health", nil, nil, &report); status != http.StatusOK {
		t.Fatalf("GET /admin/job-health → %d, want 200", status)
	}

	classified := report.RecentFailures[failureOf(t, report, "classified_pass")]
	if classified.FailureClass == nil || *classified.FailureClass != "record_gone" {
		t.Errorf("failure_class = %v, want record_gone", classified.FailureClass)
	}
	if classified.Remedy == nil || *classified.Remedy == "" {
		t.Error("a classified failure reached the wire with no remedy")
	}

	for _, kind := range []string{"unvetted_pass", "no_cause_pass"} {
		unclassified := report.RecentFailures[failureOf(t, report, kind)]
		if unclassified.FailureClass != nil || unclassified.Remedy != nil {
			t.Errorf("%s was given class %v / remedy %v for text this surface could not vet",
				kind, unclassified.FailureClass, unclassified.Remedy)
		}
		if unclassified.Reason == "" {
			t.Errorf("%s carries no sentence at all", kind)
		}
	}

	// A row with no attempt error has no first failure, and a zero time would
	// render in a failure list as 1970.
	if at := report.RecentFailures[failureOf(t, report, "no_cause_pass")].FirstFailedAt; at != nil {
		t.Errorf("first_failed_at = %q for a row that recorded no attempt error at all", *at)
	}
}

// TestJobHealthStillReportsARowWhoseFirstAttemptTimestampIsUnreadable — the
// "at" key is app-written text in a column with no constraint on it, so it is
// parsed rather than cast in SQL: a value River did not write must cost this row
// its "failing since", never cost the whole read its answer. An operator whose
// page 500s learns nothing about what died.
func TestJobHealthStillReportsARowWhoseFirstAttemptTimestampIsUnreadable(t *testing.T) {
	e := apptest.SetupApp(t)
	e.BootstrapWorkspace(t)
	mine := callerWorkspace(t, e)

	seedRiverRow(t, e, riverRow{
		kind: "unreadable_at_pass", state: "discarded", workspace: mine, attempt: 3,
		errorText: "the record this job names no longer exists",
	})
	if _, err := e.Owner.Exec(context.Background(), `
		UPDATE river_job
		   SET errors = ARRAY['{"at": "the day before yesterday", "attempt": 1,
		                       "error": "the record this job names no longer exists"}']::jsonb[]
		 WHERE kind = 'unreadable_at_pass'`); err != nil {
		t.Fatalf("rewriting the fixture row's attempt error: %v", err)
	}

	var report jobHealthDTO
	if status := e.Call(t, "GET", "/v1/admin/job-health", nil, nil, &report); status != http.StatusOK {
		t.Fatalf("GET /admin/job-health → %d, want 200: one unparseable timestamp must not "+
			"fail the whole read", status)
	}
	failure := report.RecentFailures[failureOf(t, report, "unreadable_at_pass")]

	if failure.FirstFailedAt != nil {
		t.Errorf("first_failed_at = %q, want absent: the stored moment could not be read, and "+
			"guessing one would date the failure wrongly", *failure.FirstFailedAt)
	}
	// The rest of the row is unaffected: what died, and which row to go look at.
	if failure.FailureClass == nil || *failure.FailureClass != "record_gone" {
		t.Errorf("failure_class = %v, want the row still classified", failure.FailureClass)
	}
	if failure.JobID == nil {
		t.Error("job_id went absent with the timestamp; the two are independent facts")
	}
}
