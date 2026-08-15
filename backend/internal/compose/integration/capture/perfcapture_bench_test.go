// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration && bench

package capture

// CAP-PARAM-1, measured: from a message's RECEIPT to that message being visible
// on the matched timeline, 60 s p95, "including auto-create + link of new
// participants".
//
// The receipt instant is INJECTED — it is the fixture's own `Date:` header,
// stamped at the moment the message is handed to the connector, which is what
// the chapter means by "integration-tested with a clock assert". Nothing here
// sleeps and nothing waits on a wall-clock deadline; the only real-clock reading
// is the elapsed time being measured, and a budget of 60 s against a path that
// completes in milliseconds leaves four orders of magnitude of headroom, so
// there is no timing this can lose.
//
// Every sample uses a FRESH counterparty on a fresh domain, and the thread shape
// is the one that actually auto-creates: a stranger's first message defers, so
// the budget's "auto-create + link" only happens once the owner's own attested
// reply makes the sender a counterparty (ADR-0072 §1). Measuring a deferred
// message would report the budget as held by the path that does no work.
//
// Run it with `make bench-capture`.

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/compose/integration"
	"github.com/gradionhq/margince/backend/internal/modules/search"
	"github.com/gradionhq/margince/backend/internal/platform/database"
)

// capParam1Budget is the published capture-to-timeline budget (capture.md
// CAP-PARAM-1, restated by AC1.1 and AC3.4). A calibration value: changing it is
// a noted budget revision, never a bump to make a red run green.
const capParam1Budget = 60 * time.Second

// captureBenchSamples is the measured thread count. Lower than the record
// benchmark's because each sample drives a whole three-message sync with an
// auto-create behind it, not one HTTP round trip.
const captureBenchSamples = 20

// renderNotBlockedTimeout bounds the pre-capture timeline read. It exists to
// turn AC3.4's "never blocks the inbound-mail timeline render" into something
// that can FAIL: if the read path ever waited on capture, it would wait here,
// where there is nothing to wait for, and the deadline fires instead of the
// suite hanging until the lane times out.
const renderNotBlockedTimeout = 5 * time.Second

func TestCaptureToTimelineLatencyBudget(t *testing.T) {
	env := newCaptureEnv(t)

	assertTimelineRenderIsNotBlockedByCapture(t, env.e)

	durations := make([]time.Duration, 0, captureBenchSamples)
	for i := range captureBenchSamples {
		durations = append(durations, captureOneThread(t, env, i))
	}

	stats, err := search.MeasureQuery("capture_to_timeline", capParam1Budget, durations)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("perfbench [capture]: %s p50=%s p95=%s p99=%s (budget %s, %d samples)",
		stats.Query, stats.P50, stats.P95, stats.P99, stats.Budget, stats.Samples)

	// Written BEFORE the gate, deliberately: a breach is exactly the run whose
	// numbers a reader most wants to see, and recording after the gate would
	// leave the published page green while the build went red.
	integration.WritePerfRecord(t, "bench-capture", capturePostgresVersion(t, env.e),
		[]integration.BudgetMeasurement{integration.MeasurementFrom(
			"CAP-PARAM-1", stats.Query, stats.P50, stats.P95, stats.P99, stats.Budget, stats.Samples)})

	report := search.BenchReport{Tier: search.BenchTierSMB, Queries: []search.QueryStats{stats}}
	if err := report.Gate(); err != nil {
		t.Fatalf("CAP-PARAM-1 budget gate is red: %v", err)
	}
}

// capturePostgresVersion asks the server under measurement what it is. A
// latency is a claim about that server as much as about this code.
func capturePostgresVersion(t *testing.T, e *integration.SearchEnv) string {
	t.Helper()
	return integration.PostgresVersion(func(sql string) (string, error) {
		var version string
		err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
			return tx.QueryRow(context.Background(), sql).Scan(&version)
		})
		return version, err
	})
}

// captureOneThread measures one message's whole journey: stamp the receipt,
// hand the thread to the connector, and stop the clock once the message is
// readable on the counterparty's own timeline.
//
// The elapsed time is taken from the message's DATE — the receipt the budget is
// measured from — not from the moment the sync call started, so the connector's
// own work is inside the number rather than beside it.
func captureOneThread(t *testing.T, env captureEnv, sample int) time.Duration {
	t.Helper()
	counterparty := fmt.Sprintf("alice%d@acme%d.example", sample, sample)
	inbound := "in" + strconv.Itoa(sample) + "@acme.example"
	reply := "out" + strconv.Itoa(sample) + "@myco.example"
	followUp := "in" + strconv.Itoa(sample) + "b@acme.example"

	receipt := time.Now()
	env.syncSent(t, map[string]bool{reply: true},
		benchEmailAt(counterparty, "Alice Example", captureOwner, inbound, "", receipt),
		benchEmailAt(captureOwner, "", counterparty, reply, inbound, receipt),
		benchEmailAt(counterparty, "Alice Example", captureOwner, followUp, inbound, receipt),
	)
	visible := timelineActivityCount(t, env.e, counterparty)
	elapsed := time.Since(receipt)

	// A sample that linked nothing is not a fast capture, it is a capture that
	// did not happen — recording its duration would report the budget as held
	// by the run that skipped the work.
	if visible == 0 {
		t.Fatalf("sample %d: %s has no activity on their timeline after the sync — "+
			"the thread did not auto-create, so this sample measures nothing", sample, counterparty)
	}
	return elapsed
}

// assertTimelineRenderIsNotBlockedByCapture proves AC3.4's second half. Read a
// counterparty's timeline BEFORE anything about them has been captured: the
// read must answer — promptly, and with nothing — rather than wait for a
// capture that is not coming.
func assertTimelineRenderIsNotBlockedByCapture(t *testing.T, e *integration.SearchEnv) {
	t.Helper()
	ctx, cancel := context.WithTimeout(e.Admin(), renderNotBlockedTimeout)
	defer cancel()

	var n int
	err := database.WithWorkspaceTx(ctx, e.Pool, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			SELECT count(*) FROM activity_link al JOIN person_email pe ON pe.person_id = al.person_id
			WHERE al.entity_type = 'person' AND pe.email = 'nobody@uncaptured.example'`).Scan(&n)
	})
	if err != nil {
		t.Fatalf("the timeline render waited on a capture that never came (AC3.4): %v", err)
	}
	if n != 0 {
		t.Fatalf("an uncaptured counterparty has %d timeline rows, want 0 — the fixture is wrong", n)
	}
}

// timelineActivityCount is what "visible on the matched timeline" means in the
// schema: activities linked to the person the message was matched to.
func timelineActivityCount(t *testing.T, e *integration.SearchEnv, email string) int {
	t.Helper()
	var n int
	err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(), `
			SELECT count(*) FROM activity_link al JOIN person_email pe ON pe.person_id = al.person_id
			WHERE al.entity_type = 'person' AND pe.email = $1`, email).Scan(&n)
	})
	if err != nil {
		t.Fatalf("reading %s's timeline: %v", email, err)
	}
	return n
}

// benchEmailAt is email() with the receipt instant as a parameter. It is not a
// change to email() because every other suite in this package wants that Date
// FIXED — a fixture whose timestamp moves would make their assertions depend on
// when they ran. Here the Date is the measurement's zero point and has to move.
func benchEmailAt(from, fromName, to, msgID, refs string, at time.Time) []byte {
	fromHeader := from
	if fromName != "" {
		fromHeader = fmt.Sprintf("%s <%s>", fromName, from)
	}
	lines := []string{
		"From: " + fromHeader,
		"To: " + to,
		"Subject: project",
		"Date: " + at.UTC().Format(time.RFC1123Z),
		"Message-ID: <" + msgID + ">",
	}
	if refs != "" {
		lines = append(lines, "References: <"+refs+">")
	}
	lines = append(lines, "Content-Type: text/plain", "", "hello", "")
	return []byte(strings.Join(lines, "\r\n"))
}
