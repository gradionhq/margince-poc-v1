// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package static

import (
	"encoding/json"
	"fmt"
	"io"
)

// WriteJSON emits the canonical machine-readable report — the same shape the
// Critic Agent (architecture/16) emits, so both arms of the gate feed one
// downstream consumer.
func (r Report) WriteJSON(w io.Writer) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(r)
}

// WriteText emits a human-readable report grouped by file, ending in the
// verdict and a per-severity tally. Like WriteJSON, it surfaces the writer's
// error so a truncated report is never mistaken for a complete one.
func (r Report) WriteText(w io.Writer) error {
	ew := &errWriter{w: w}
	var blockers, majors, minors int
	lastFile := ""
	for _, f := range r.Findings {
		if f.File != lastFile {
			ew.printf("\n%s\n", f.File)
			lastFile = f.File
		}
		ew.printf("  %d: [%s] %s — %s\n", f.Line, f.Level, f.Check, f.Message)
		switch f.Severity {
		case Blocker:
			blockers++
		case Major:
			majors++
		default:
			minors++
		}
	}
	if len(r.Findings) == 0 {
		ew.printf("no craftsmanship findings\n")
	}
	ew.printf("\n%s — %d blocker, %d major, %d minor\n", r.Verdict, blockers, majors, minors)
	return ew.err
}

// errWriter latches the first write error and drops subsequent writes, so the
// render loop stays linear instead of threading an error through every line.
type errWriter struct {
	w   io.Writer
	err error
}

func (ew *errWriter) printf(format string, args ...any) {
	if ew.err != nil {
		return
	}
	_, ew.err = fmt.Fprintf(ew.w, format, args...)
}
