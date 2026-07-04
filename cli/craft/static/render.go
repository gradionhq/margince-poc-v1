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
// verdict and a per-severity tally.
func (r Report) WriteText(w io.Writer) {
	var blockers, majors, minors int
	lastFile := ""
	for _, f := range r.Findings {
		if f.File != lastFile {
			fmt.Fprintf(w, "\n%s\n", f.File)
			lastFile = f.File
		}
		fmt.Fprintf(w, "  %d: [%s] %s — %s\n", f.Line, f.Level, f.Check, f.Message)
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
		fmt.Fprint(w, "no craftsmanship findings\n")
	}
	fmt.Fprintf(w, "\n%s — %d blocker, %d major, %d minor\n", r.Verdict, blockers, majors, minors)
}
