// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package main

import (
	"fmt"
	"strings"
	"text/tabwriter"

	"github.com/gradionhq/margince/backend/internal/compose/aicert"
)

// renderMatrix formats records — already sorted by LoadRecords on
// Task/Provider/ServedModel/EnvClass — as a task×model certification
// matrix. An empty set (nothing has been certified yet, e.g. before the
// corpus exists or before `make e2e-ai` has ever run) renders no table at
// all: a header with zero rows reads like a passing check, so the empty
// case gets its own honest sentence instead.
func renderMatrix(records []aicert.Record) string {
	if len(records) == 0 {
		return "no certification records found — run `make e2e-ai` " +
			"(e.g. `make e2e-ai TASK=<task> MODEL=<provider:model>`) to produce some\n"
	}

	var buf strings.Builder
	w := tabwriter.NewWriter(&buf, 0, 0, 2, ' ', 0)
	var writeErr error
	writeRow := func(format string, args ...any) {
		if writeErr != nil {
			return
		}
		_, writeErr = fmt.Fprintf(w, format, args...)
	}
	writeRow("TASK\tPROVIDER\tMODEL\tVERDICT\tRELIABILITY\tSCORE_P50\tLATENCY_P50_MS\tRUNS\n")
	for _, r := range records {
		writeRow("%s\t%s\t%s\t%s\t%.2f\t%d\t%d\t%d\n",
			r.Task, r.Provider, r.ServedModel, r.Verdict, r.Reliability, r.ScoreP50, r.LatencyP50, r.Runs)
	}
	if writeErr == nil {
		writeErr = w.Flush()
	}
	if writeErr != nil {
		// An in-memory strings.Builder never actually errors, but a
		// write into the tabwriter is still, mechanically, a write —
		// checked like any other rather than assumed infallible.
		return fmt.Sprintf("aicert: formatting the report table: %v\n", writeErr)
	}
	return buf.String()
}
