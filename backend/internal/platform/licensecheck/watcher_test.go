// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package licensecheck

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"
)

// fixedClock is the injected now: a test never reads the wall clock, so a
// re-check's stamp is a value the assertions can name.
func fixedClock(at time.Time) func() time.Time { return func() time.Time { return at } }

func TestNewWatcherRefusesARejectedLicense(t *testing.T) {
	t.Parallel()
	_, err := NewWatcher(context.Background(), "not-a-license", fixedClock(checkedAt), slog.New(slog.DiscardHandler))
	if err == nil {
		t.Fatal("NewWatcher accepted a license the module refuses; the role would serve on a bad license")
	}
	// The refusal has to name the module that refused: a stale bundled module
	// and a genuinely bad license look identical to an operator otherwise.
	if !strings.Contains(err.Error(), ModuleVersion()) {
		t.Errorf("boot refusal %q does not name the bundled module version %q", err, ModuleVersion())
	}
}

func TestNewWatcherBootsWithoutALicense(t *testing.T) {
	t.Parallel()
	w, err := NewWatcher(context.Background(), "", fixedClock(checkedAt), slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatalf("NewWatcher refused an unlicensed installation: %v", err)
	}
	if got := w.Posture(); got.State != StateAbsent {
		t.Errorf("posture = %q, want %q", got.State, StateAbsent)
	}
}

// A posture that degrades while the process runs is recorded and reported, and
// the process keeps its watcher: nothing here stops anything.
func TestRecheckRecordsADegradedPostureAndLogsTheTransition(t *testing.T) {
	t.Parallel()
	var log bytes.Buffer
	w, err := NewWatcher(context.Background(), "", fixedClock(checkedAt),
		slog.New(slog.NewTextHandler(&log, &slog.HandlerOptions{Level: slog.LevelWarn})))
	if err != nil {
		t.Fatalf("NewWatcher: %v", err)
	}

	// The license the process booted with stops being honored. Standing in for
	// expiry-past-grace, which no token this repository can mint would reach.
	later := checkedAt.Add(48 * time.Hour)
	w.token = "no-longer-honored"
	w.now = fixedClock(later)
	w.Recheck(context.Background())

	got := w.Posture()
	if got.State != StateRejected {
		t.Errorf("posture = %q, want %q", got.State, StateRejected)
	}
	if !got.CheckedAt.Equal(later) {
		t.Errorf("CheckedAt = %v, want the re-check's %v", got.CheckedAt, later)
	}
	line := log.String()
	for _, want := range []string{"license posture changed", "from=absent", "to=rejected"} {
		if !strings.Contains(line, want) {
			t.Errorf("the transition log %q does not carry %q", line, want)
		}
	}
}

// A steady posture is silent. An operator scanning a year of logs should find
// the day the license lapsed, not a line per day saying it had not.
func TestRecheckSaysNothingWhenTheStateIsUnchanged(t *testing.T) {
	t.Parallel()
	var log bytes.Buffer
	w, err := NewWatcher(context.Background(), "", fixedClock(checkedAt),
		slog.New(slog.NewTextHandler(&log, &slog.HandlerOptions{Level: slog.LevelWarn})))
	if err != nil {
		t.Fatalf("NewWatcher: %v", err)
	}
	w.Recheck(context.Background())
	if log.Len() != 0 {
		t.Errorf("an unchanged posture logged %q", log.String())
	}
	if got := w.Posture(); got.State != StateAbsent {
		t.Errorf("posture = %q, want it unchanged at %q", got.State, StateAbsent)
	}
}

// The loop ends with its context and does not outlive the process role that
// started it.
func TestRunRecheckStopsWhenTheContextIsCancelled(t *testing.T) {
	t.Parallel()
	w, err := NewWatcher(context.Background(), "", fixedClock(checkedAt), slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatalf("NewWatcher: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan struct{})
	go func() {
		w.RunRecheck(ctx)
		close(stopped)
	}()
	cancel()
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("RunRecheck did not return after its context was cancelled")
	}
}
