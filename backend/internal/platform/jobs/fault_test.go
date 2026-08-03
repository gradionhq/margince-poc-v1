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

// TestVettedSentenceAdmitsOnlyWhatFaultItselfWouldHaveWritten is the whole
// safety argument for serving river_job.errors to a human: the column is
// fleet-visible with no RLS and no redaction path, so a reader must never
// pass its content through on trust. A worker that returned a bare provider
// error — naming an address it refused — stored that text here.
func TestVettedSentenceAdmitsOnlyWhatFaultItselfWouldHaveWritten(t *testing.T) {
	for _, known := range vocabulary {
		if !VettedSentence(known.sentence) {
			t.Errorf("VettedSentence(%q) = false, want true", known.sentence)
		}
	}
	if !VettedSentence(unrecognised) {
		t.Error("the unclassified sentence is written by Fault and must be admitted")
	}

	for _, raw := range []string{
		"",
		"dial tcp 10.0.0.4:5432: connect: connection refused",
		`smtp: 550 5.1.1 <someone@example.com>: recipient rejected`,
		// River's rescuer writes this itself when a worker's process died
		// mid-job. It is not a Fault sentence and must not be served as one.
		"Stuck job rescued by JobRescuer",
		// A near-miss: the vocabulary's sentence with trailing whitespace.
		// The match is exact, so a worker whose own text merely resembles a
		// vetted one does not slip through.
		"the record this job names no longer exists ",
		"THE RECORD THIS JOB NAMES NO LONGER EXISTS",
		// A concatenation that CONTAINS a vetted sentence. A substring match
		// here would let a worker prefix its raw cause onto a known sentence
		// and carry the lot to the wire.
		"connecting to 10.0.0.4: the record this job names no longer exists",
	} {
		if VettedSentence(raw) {
			t.Errorf("VettedSentence(%q) = true: raw worker text reached a reader", raw)
		}
	}
}

// TestEverySentenceFaultCanWriteIsVetted derives the obligation from the
// vocabulary rather than restating it: Fault has exactly two sources of
// output text, and a sentence added to one of them without the reader
// learning about it would be substituted away as if a worker had leaked it.
func TestEverySentenceFaultCanWriteIsVetted(t *testing.T) {
	for _, known := range vocabulary {
		stored := Fault(fmt.Errorf("a raw cause: %w", known.sentinel)).Error()
		if !VettedSentence(stored) {
			t.Errorf("Fault wrote %q for %v, and the reader would not admit it",
				stored, known.sentinel)
		}
	}
	unclassified := Fault(errors.New("a cause no sentinel covers")).Error()
	if !VettedSentence(unclassified) {
		t.Errorf("Fault wrote %q for an unclassified cause, and the reader would not admit it",
			unclassified)
	}
}
