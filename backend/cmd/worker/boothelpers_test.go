// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package main

// The configuration phases run() delegates to before it opens anything. What
// they share is the property the grouping exists for: none of them touches a
// network, so every way a deployment can be misconfigured is refused while
// there is nothing built to clean up.

import (
	"bytes"
	"io"
	"path/filepath"
	"strings"
	"testing"
)

// ONE test in this binary may drive configureWorker to completion.
//
// It registers the composed extension set into a PROCESS-GLOBAL registry, which
// is a boot action and deliberately not idempotent: a second registration of
// the same jurisdiction is refused, because in a running process that would
// mean two packs claiming one jurisdiction. So the success path is exercised
// once, below, and every other case here stops before that phase.

// TestConfiguringTheWorkerFailsBeforeAnythingIsOpened is the property that
// makes these phases one step: none of them opens a connection, so every way a
// deployment can be misconfigured is refused while there is no pool, no bus
// client and no listener to clean up. A phase that moved below the pool would
// turn a typo into a half-built process.
//
// Both cases fail in the flag phase, ahead of the registration. The logger's
// own refusal — the one phase after it — is driven directly by
// TestAnUnknownLogLevelOrFormatFailsTheBoot instead.
func TestConfiguringTheWorkerFailsBeforeAnythingIsOpened(t *testing.T) {
	for _, tc := range []struct {
		name, wantIn string
		args         []string
	}{
		{"a missing dsn", "dsn", []string{}},
		{"an interval no schedule can use", "runner-interval", []string{"--dsn", "postgres://localhost/x", "--runner-interval=0"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// --dsn backs its default with MARGINCE_DSN, so a CI that exports
			// one would make the missing-dsn case boot successfully and this
			// subtest fail for a reason that has nothing to do with the code.
			t.Setenv("MARGINCE_DSN", "")
			_, err := configureWorker(tc.args, io.Discard)
			if err == nil {
				t.Fatalf("configureWorker(%v) succeeded; a misconfigured deployment must be refused here", tc.args)
			}
			if !strings.Contains(err.Error(), tc.wantIn) {
				t.Errorf("the error does not name what was wrong (%q): %v", tc.wantIn, err)
			}
		})
	}
}

// TestConfiguringTheWorkerCarriesTheDeploymentPostureOntoTheConfig is the
// success path, and the only test here that reaches the extension registration.
// A failing registration aborts the worker boot (ADR-0069 EXT-P4), so this also
// asserts that the set THIS build composes is one the boot survives.
//
// The capture posture is resolved LAST, after the logger exists, because it
// carries that logger into the Sink's post-commit steps where a fault is
// reported rather than returned. Ordered the other way it would carry a nil.
func TestConfiguringTheWorkerCarriesTheDeploymentPostureOntoTheConfig(t *testing.T) {
	// A path with no file is a legitimate deployment: the docs say a missing
	// config boots with defaults, so this exercises the whole chain rather
	// than a fixture's contents.
	boot, err := configureWorker([]string{
		"--dsn", "postgres://localhost/x",
		"--config", filepath.Join(t.TempDir(), "absent.yaml"),
	}, io.Discard)
	if err != nil {
		t.Fatalf("configureWorker: %v — a composed extension set that refuses to register aborts the worker boot", err)
	}
	if boot.log == nil {
		t.Error("no logger was built, so every phase after this one would report through nothing")
	}
	if boot.cfg.dsn != "postgres://localhost/x" {
		t.Errorf("dsn = %q, want the one supplied", boot.cfg.dsn)
	}
	if boot.cfg.captureConfig.Logger == nil {
		t.Error("the capture posture carries no logger; the Sink's post-commit steps report a fault through it, and a nil there is a panic on the one path that must not have one")
	}
}

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
