// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package jobs

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/riverqueue/river"

	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
)

func TestFaultRendersAKnownSentinelAsItsFixedSentence(t *testing.T) {
	cause := fmt.Errorf("smtp 550 5.1.1 <someone@example.com> user unknown: %w", apperrors.ErrNotFound)
	got := Fault(cause).Error()
	if got != "the record this job names no longer exists" {
		t.Fatalf("Fault rendered %q, want the fixed sentence for ErrNotFound", got)
	}
}

func TestFaultNeverLeaksTheCauseText(t *testing.T) {
	cause := errors.New("smtp 550 5.1.1 <someone@example.com> user unknown")
	got := Fault(cause).Error()
	if got != unrecognised {
		t.Fatalf("Fault rendered %q, want the fixed generic sentence — an unrecognised cause must collapse, not be paraphrased", got)
	}
	// The address is the thing that may never reach river_job.errors, so assert
	// its absence rather than merely that the sentence differs from the cause.
	if strings.Contains(got, "someone@example.com") {
		t.Fatalf("the wire sentence carries the refused address: %q", got)
	}
}

func TestFaultPreservesNil(t *testing.T) {
	if err := Fault(nil); err != nil {
		t.Fatalf("Fault(nil) = %v, want nil — a successful job must stay successful", err)
	}
}

func TestFaultUnwrapsToTheCauseForErrorsIs(t *testing.T) {
	cause := fmt.Errorf("wrapped: %w", apperrors.ErrConflict)
	if !errors.Is(Fault(cause), apperrors.ErrConflict) {
		t.Fatal("Fault must keep the cause reachable through errors.Is — River's retry policy and the tests both classify on it")
	}
}

func TestFaultPassesRiverControlReturnsThroughUntouched(t *testing.T) {
	// A snooze is a reschedule and a cancel a deliberate stop; neither is a
	// failure. Both reach a worker's return through helpers, so Fault sees
	// them and must not classify, rewrite, or log them.
	snooze := river.JobSnooze(time.Minute)
	if got := Fault(snooze); !errors.Is(got, snooze) {
		t.Fatalf("Fault(JobSnooze) = %v, want the snooze returned identically", got)
	}
	cancel := river.JobCancel(errors.New("identity drift"))
	if got := Fault(cancel); !errors.Is(got, cancel) {
		t.Fatalf("Fault(JobCancel) = %v, want the cancel returned identically", got)
	}
}

func TestFaultPassesAWrappedControlReturnThrough(t *testing.T) {
	// The helper that produced the snooze may itself be wrapped by its
	// caller before the worker returns it.
	wrapped := fmt.Errorf("telegram_poll: %w", river.JobSnooze(time.Minute))
	var snooze *river.JobSnoozeError
	if !errors.As(Fault(wrapped), &snooze) {
		t.Fatal("Fault must leave a wrapped snooze detectable by River's errors.As check, or the job fails instead of rescheduling")
	}
}

// A sentinel reached through a control error must NOT be reclassified: the
// control return wins, because rescheduling is not failing.
func TestFaultPrefersTheControlReturnOverASentinelUnderneath(t *testing.T) {
	wrapped := fmt.Errorf("%w", river.JobCancel(apperrors.ErrConsentNotGranted))
	var cancel *river.JobCancelError
	if !errors.As(Fault(wrapped), &cancel) {
		t.Fatal("a cancel carrying a known sentinel must stay a cancel — River stops the job rather than spending a rung on it")
	}
}
