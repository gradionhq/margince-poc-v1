// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The capture pipeline's counter family: what this process decided about the
// messages it captured.
//
// Until now /metrics said nothing at all about capture — an operator could see
// the outbox backlog and the job queue, but not that every message from a
// mailbox had been dropped as internal since somebody registered a domain.

import (
	"fmt"
	"io"
	"sort"

	"github.com/gradionhq/margince/backend/internal/modules/capture"
)

// writeCaptureMetrics renders one counter per traced outcome.
//
// Sorted, because Prometheus does not care but a human reading a scrape by hand
// does, and an unordered map would reshuffle the block on every request.
func writeCaptureMetrics(w io.Writer, totals map[string]uint64) {
	if len(totals) == 0 {
		// Nothing traced yet in this process. Printing zeros for every outcome
		// would claim a pipeline ran and decided nothing, which is a different
		// fact from not having run.
		return
	}
	outcomes := make([]string, 0, len(totals))
	for outcome := range totals {
		outcomes = append(outcomes, outcome)
	}
	sort.Strings(outcomes)
	_, _ = fmt.Fprintf(w, "# HELP margince_capture_outcomes_total What the capture pipeline decided about each message, since process start.\n")
	_, _ = fmt.Fprintf(w, "# TYPE margince_capture_outcomes_total counter\n")
	for _, outcome := range outcomes {
		_, _ = fmt.Fprintf(w, "margince_capture_outcomes_total{outcome=%q} %d\n", outcome, totals[outcome])
	}
}

// writeCaptureSection is the fan-out's entry point.
func (Server) writeCaptureSection(w io.Writer) {
	writeCaptureMetrics(w, capture.TraceOutcomeTotals())
}
