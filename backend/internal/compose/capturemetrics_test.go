// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

import (
	"bytes"
	"strings"
	"testing"
)

func TestCaptureMetricsNamesEveryOutcomeItCounted(t *testing.T) {
	var buf bytes.Buffer
	writeCaptureMetrics(&buf, map[string]uint64{"captured": 12, "internal": 3})
	out := buf.String()

	for _, want := range []string{
		"# TYPE margince_capture_outcomes_total counter",
		`margince_capture_outcomes_total{outcome="captured"} 12`,
		`margince_capture_outcomes_total{outcome="internal"} 3`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("exposition missing %q, got:\n%s", want, out)
		}
	}
	// Sorted so a human reading a scrape by hand sees a stable block.
	if strings.Index(out, `outcome="captured"`) > strings.Index(out, `outcome="internal"`) {
		t.Error("outcomes are not sorted")
	}
}

// A process that has traced nothing has not decided nothing — it has not run.
// Printing zeros would report the first as the second.
func TestCaptureMetricsSaysNothingWhenNothingWasTraced(t *testing.T) {
	var buf bytes.Buffer
	writeCaptureMetrics(&buf, nil)
	if buf.Len() != 0 {
		t.Errorf("exposition = %q for an untraced process, want empty", buf.String())
	}
}
