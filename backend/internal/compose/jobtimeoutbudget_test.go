// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

import (
	"testing"
	"time"

	"github.com/gradionhq/margince/backend/internal/platform/geocode"
	"github.com/gradionhq/margince/backend/internal/platform/jobs"
)

// The geocode job's ceiling must sit ABOVE everything it can wait for.
//
// River applies a job timeout by cancelling the job's context, and the worker
// reads a cancelled context as "we were stopped" — re-queueing the lookup
// unrecorded rather than counting it against the address. That is right for a
// shutdown and wrong for a job that genuinely ran out of time, and the two are
// indistinguishable once the context is done.
//
// So the ceiling is what keeps the distinction honest: while it stays clear of
// the real budget, a cancelled context means a shutdown and nothing else. The
// budget is the pacer's full interval plus the HTTP client's own timeout,
// because the pacer holds a lookup before the request is even built.
func TestTheGeocodeCeilingStaysClearOfWhatTheJobCanWaitFor(t *testing.T) {
	spec, declared := jobs.SpecFor("geocode_organization")
	if !declared {
		t.Fatal("geocode_organization is not declared, so nothing bounds it")
	}
	const httpTimeout = 20 * time.Second // geocode.NewNominatim's default client
	budget := geocode.RecurringInterval + httpTimeout
	if spec.Timeout.Fixed <= budget {
		t.Errorf("the job ceiling is %s against a %s budget (pacer %s + HTTP %s) — a job that "+
			"hit the ceiling would be read as a shutdown and never counted against the address",
			spec.Timeout.Fixed, budget, geocode.RecurringInterval, httpTimeout)
	}
}
