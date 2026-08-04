// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package main

// The two boot phases run() delegates to. Both are one-liners around a
// dependency, and both have exactly one failure the process must not survive:
// a log level nobody meant, and an extension set that refused to register.

import (
	"bytes"
	"strings"
	"testing"
)

// TestTheLoggerHonoursTheOperatorsLevelAndFormat — the level and the format
// are the operator's, and a typo in either is a boot error rather than a
// silent fallback to a level nobody asked for. A worker logging at the wrong
// level looks healthy and says nothing.
func TestTheLoggerHonoursTheOperatorsLevelAndFormat(t *testing.T) {
	var out bytes.Buffer
	log, err := newWorkerLogger(workerConfig{logLevel: "error", logFormat: "json"}, &out)
	if err != nil {
		t.Fatalf("newWorkerLogger: %v", err)
	}

	log.Info("this is below the configured level")
	if out.Len() != 0 {
		t.Errorf("an info line was written at level=error: %s", out.String())
	}

	log.Error("this one is not")
	line := out.String()
	if !strings.HasPrefix(strings.TrimSpace(line), "{") {
		t.Errorf("format=json produced %q, which is not a JSON record", line)
	}
}

func TestAnUnknownLogLevelOrFormatFailsTheBoot(t *testing.T) {
	for _, tc := range []struct{ name, level, format string }{
		{"level", "verbose", "text"},
		{"format", "info", "logfmt"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			if _, err := newWorkerLogger(workerConfig{logLevel: tc.level, logFormat: tc.format}, &out); err == nil {
				t.Errorf("an unknown %s was accepted; the operator would get a level or a format they did not ask for", tc.name)
			}
		})
	}
}

// TestThisBuildsComposedExtensionSetRegisters — a failing registration aborts
// the worker boot (ADR-0069 EXT-P4), so what this asserts is that the set THIS
// build composes is one the boot survives. An extension whose declaration the
// registry refuses fails here rather than at a customer's start-up.
//
// It does not compare the returned slice against composition.Extensions():
// only a role's main may import the composition module, and TestCompositionWiredOnlyFromCmd
// enforces that. The identity of the snapshot is a compile-time fact of run()
// passing the returned value straight on to the boot inventory.
func TestThisBuildsComposedExtensionSetRegisters(t *testing.T) {
	if _, err := registerComposedExtensions(); err != nil {
		t.Fatalf("this build's composed extension set refused to register, which aborts the worker boot: %v", err)
	}
}
