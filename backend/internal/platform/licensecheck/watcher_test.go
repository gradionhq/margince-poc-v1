// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package licensecheck

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/gradionhq/margince/backend/internal/shared/runtimeenv"
)

// fixedClock is the injected now: a test never reads the wall clock, so a
// re-check's stamp is a value the assertions can name.
func fixedClock(at time.Time) func() time.Time { return func() time.Time { return at } }

// tokens is a TokenSource a test drives — the same seam production uses, where
// deployconfig re-reads the operator's file or variable.
type tokens struct {
	token string
	err   error
	reads int
}

func (t *tokens) source() (string, error) {
	t.reads++
	return t.token, t.err
}

func warnLog(into *bytes.Buffer) *slog.Logger {
	return slog.New(slog.NewTextHandler(into, &slog.HandlerOptions{Level: slog.LevelWarn}))
}

func TestNewWatcherRefusesARejectedLicense(t *testing.T) {
	t.Parallel()
	source := &tokens{token: "not-a-license"}
	_, err := NewWatcher(context.Background(), source.source, fixedClock(checkedAt), slog.New(slog.DiscardHandler), runtimeenv.Production)
	if err == nil {
		t.Fatal("NewWatcher accepted a license the module refuses; the role would serve on a bad license")
	}
	// The refusal has to name the module that refused: a stale bundled module
	// and a genuinely bad license look identical to an operator otherwise.
	if !strings.Contains(err.Error(), ModuleVersion()) {
		t.Errorf("boot refusal %q does not name the bundled module version %q", err, ModuleVersion())
	}
}

// A token that cannot be read is the caller's error to report, not a posture:
// this is deployconfig's mistyped-path refusal arriving through the source.
func TestNewWatcherRefusesATokenItCannotRead(t *testing.T) {
	t.Parallel()
	source := &tokens{err: errors.New("reading license.token_file: no such file")}
	_, err := NewWatcher(context.Background(), source.source, fixedClock(checkedAt), slog.New(slog.DiscardHandler), runtimeenv.Production)
	if err == nil {
		t.Fatal("NewWatcher booted on a token source that could not answer")
	}
	if !strings.Contains(err.Error(), "license.token_file") {
		t.Errorf("boot refusal %q drops what the source said", err)
	}
}

func TestNewWatcherBootsWithoutALicense(t *testing.T) {
	t.Parallel()
	source := &tokens{}
	w, err := NewWatcher(context.Background(), source.source, fixedClock(checkedAt), slog.New(slog.DiscardHandler), runtimeenv.Production)
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
	source := &tokens{}
	w, err := NewWatcher(context.Background(), source.source, fixedClock(checkedAt), warnLog(&log), runtimeenv.Production)
	if err != nil {
		t.Fatalf("NewWatcher: %v", err)
	}

	// The license the process booted with stops being honored. Standing in for
	// expiry-past-grace, which no token this repository can mint would reach.
	later := checkedAt.Add(48 * time.Hour)
	source.token = "no-longer-honored"
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

// The re-check re-READS the token, so a license replaced in place is picked up.
// A watcher holding the boot-time value could only ever watch a license lapse,
// and would keep reporting a refusal an operator had already fixed.
func TestRecheckReadsTheTokenAgainRatherThanTheOneItBootedWith(t *testing.T) {
	t.Parallel()
	source := &tokens{}
	w, err := NewWatcher(context.Background(), source.source, fixedClock(checkedAt), slog.New(slog.DiscardHandler), runtimeenv.Production)
	if err != nil {
		t.Fatalf("NewWatcher: %v", err)
	}
	if source.reads != 1 {
		t.Fatalf("the boot read the token source %d times, want once", source.reads)
	}
	w.Recheck(context.Background())
	if source.reads != 2 {
		t.Errorf("the re-check read the token source %d times in total, want 2 — a replaced token would never be seen",
			source.reads)
	}
}

// A source that stops answering says nothing about the license, so the posture
// stands. Degrading here would tell an operator their license was refused when
// what broke was the machinery for asking.
func TestRecheckKeepsThePostureWhenTheTokenCannotBeRead(t *testing.T) {
	t.Parallel()
	var log bytes.Buffer
	source := &tokens{}
	w, err := NewWatcher(context.Background(), source.source, fixedClock(checkedAt),
		slog.New(slog.NewTextHandler(&log, &slog.HandlerOptions{Level: slog.LevelWarn})), runtimeenv.Production)
	if err != nil {
		t.Fatalf("NewWatcher: %v", err)
	}
	source.err = errors.New("token file vanished")
	w.Recheck(context.Background())

	if got := w.Posture(); got.State != StateAbsent {
		t.Errorf("posture = %q, want it to stand at %q", got.State, StateAbsent)
	}
	if !strings.Contains(log.String(), "keeping the posture") {
		t.Errorf("the fault log %q does not say the posture was kept", log.String())
	}
}

// A steady posture is silent. An operator scanning a year of logs should find
// the day the license lapsed, not a line per day saying it had not.
func TestRecheckSaysNothingWhenTheStateIsUnchanged(t *testing.T) {
	t.Parallel()
	var log bytes.Buffer
	source := &tokens{}
	w, err := NewWatcher(context.Background(), source.source, fixedClock(checkedAt), warnLog(&log), runtimeenv.Production)
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

// The type's promise is that nothing about a license ends a serving process. The
// module runs through a runtime that panics rather than returning on some faults,
// and this loop is a bare goroutine in the api's boot — an escaping panic would
// be process death with a stack dump instead of a degraded posture.
func TestRecheckSurvivesAPanicOnTheWayToAVerdict(t *testing.T) {
	t.Parallel()
	var log bytes.Buffer
	source := &tokens{}
	w, err := NewWatcher(context.Background(), source.source,
		fixedClock(checkedAt), slog.New(slog.NewTextHandler(&log, &slog.HandlerOptions{Level: slog.LevelWarn})),
		runtimeenv.Production)
	if err != nil {
		t.Fatalf("NewWatcher: %v", err)
	}
	w.now = func() time.Time { panic("the runtime came apart") }

	w.Recheck(context.Background())

	if got := w.Posture(); got.State != StateAbsent {
		t.Errorf("posture = %q, want the last resolved %q", got.State, StateAbsent)
	}
	line := log.String()
	if !strings.Contains(line, "panicked") || !strings.Contains(line, "the runtime came apart") {
		t.Errorf("the fault log %q does not report the panic", line)
	}
	if !strings.Contains(line, "stack") {
		t.Error("the fault log carries no stack; a panic nobody can locate is a panic nobody fixes")
	}
}

// The loop ends with its context and does not outlive the process role that
// started it.
func TestRunRecheckStopsWhenTheContextIsCancelled(t *testing.T) {
	t.Parallel()
	source := &tokens{}
	w, err := NewWatcher(context.Background(), source.source, fixedClock(checkedAt), slog.New(slog.DiscardHandler), runtimeenv.Production)
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
