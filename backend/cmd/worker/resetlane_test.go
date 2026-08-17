// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package main

// The worker's stake in the data reset is one subscriber: the cache flush an
// announced reset triggers. It exists only to serve that reset, so an
// installation that never armed the reset holds no subscriber either — and must
// not, because the announcement channel is reachable by anyone who can publish
// to the bus, and a subscriber nobody asked for is a way to force cache misses
// on this role indefinitely.
//
// What is worth pinning here is that the worker READS the switch from the same
// deployment file the api does. The branch it feeds is one `if`; the wiring that
// carries the operator's answer across two process roles is the part that can
// silently disagree.

import (
	"os"
	"path/filepath"
	"testing"
)

func TestTheWorkerReadsTheDataResetSwitchFromTheDeploymentFile(t *testing.T) {
	for _, tc := range []struct {
		name string
		yaml string
		want bool
	}{
		{name: "armed", yaml: "version: 1\noperations:\n  allow_data_reset: true\n", want: true},
		{name: "stated off", yaml: "version: 1\noperations:\n  allow_data_reset: false\n", want: false},
		// The case that matters most: a deployment that says nothing about
		// operations gets no reset, whatever posture it runs under.
		{name: "unstated", yaml: "version: 1\n", want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "margince.yaml")
			if err := os.WriteFile(path, []byte(tc.yaml), 0o600); err != nil {
				t.Fatal(err)
			}
			cfg := workerConfig{configPath: path}
			if _, err := loadDeployment(&cfg); err != nil {
				t.Fatalf("loadDeployment: %v", err)
			}
			if cfg.allowDataReset != tc.want {
				t.Errorf("allowDataReset = %v, want %v", cfg.allowDataReset, tc.want)
			}
		})
	}
}

func TestAWorkerThatLoadedNothingArmsNothing(t *testing.T) {
	// The zero value is what a role holds before any file is read, and what one
	// with no --config holds for good. Fail-closed is not a posture question.
	var cfg workerConfig
	if cfg.allowDataReset {
		t.Fatal("a worker that read no deployment file armed the data reset")
	}
}
