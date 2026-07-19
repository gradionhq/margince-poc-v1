// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build e2e_llm

package aicert_test

// The certification lane's PAID, LIVE run: drives a real candidate
// router (and a real judge router) over whatever the corpus holds, on a
// real routing config. Excluded from every ordinary lane (unit,
// integration, `make check`) by the e2e_llm build tag — the same
// convention compose/sitereade2e_test.go's TestSiteReadE2EGradionQualityFloor
// uses — so it never runs, let alone silently "passes" by doing
// nothing, in a lane that has not explicitly opted into real network
// and real spend. A `make e2e-ai` target invokes this with `-tags
// e2e_llm` plus the env vars below.
//
// Once entered (the build tag is set), MARGINCE_AICERT is still checked
// at runtime and its absence FAILS the test — t.Skip is forbidden by
// this repo's test culture: a skipped gate must never read the same as
// a passing one. The remaining env vars name what to certify.

import (
	"context"
	"log/slog"
	"os"
	"strconv"
	"testing"

	"github.com/gradionhq/margince/backend/internal/compose/aicert"
)

// TestE2ECertify runs one certification pass against the configured
// routing file. MARGINCE_AI_ROUTING is the only hard requirement beyond
// the lane gate itself; the rest default to "certify everything the
// corpus covers, on the routing file's own bindings, Run's own default
// repeat count."
func TestE2ECertify(t *testing.T) {
	if os.Getenv("MARGINCE_AICERT") == "" {
		t.Fatal("TestE2ECertify requires MARGINCE_AICERT=1 (set by `make e2e-ai`) — " +
			"this lane costs real tokens and real network, so it never runs implicitly")
	}
	routingPath := os.Getenv("MARGINCE_AI_ROUTING")
	if routingPath == "" {
		t.Fatal("MARGINCE_AI_ROUTING is required — name the routing config to certify against")
	}

	// repeats stays 0 (Run's own "default to 3" per RunnerConfig.Repeats'
	// own doc) when MARGINCE_AICERT_RUNS is unset — this lane restates no
	// default the runner already owns.
	var repeats int
	if raw := os.Getenv("MARGINCE_AICERT_RUNS"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil {
			t.Fatalf("MARGINCE_AICERT_RUNS=%q is not an integer: %v", raw, err)
		}
		repeats = n
	}

	cfg := aicert.RunnerConfig{
		RoutingPath: routingPath,
		TaskFilter:  os.Getenv("MARGINCE_AICERT_TASK"),
		Override:    os.Getenv("MARGINCE_AICERT_MODEL"),
		Repeats:     repeats,
		CorpusDir:   "corpus",
		RecordDir:   "records",
	}

	records, err := aicert.Run(context.Background(), cfg, slog.Default())
	if err != nil {
		t.Fatalf("certification run failed: %v", err)
	}
	if len(records) == 0 {
		t.Fatal("the run produced no records — check MARGINCE_AICERT_TASK against the corpus")
	}
	for _, r := range records {
		t.Logf("%s: %s (reliability=%.2f score_p50=%d self_judged=%v)",
			r.Task, r.Verdict, r.Reliability, r.ScoreP50, r.SelfJudged)
	}
}
