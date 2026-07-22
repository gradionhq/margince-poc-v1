// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/gradionhq/margince/backend/internal/modules/ai"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

func TestFailureStatusCodeClassifiesErrorFamilies(t *testing.T) {
	cases := map[string]struct {
		err  error
		want string
	}{
		"unconfigured model path": {errors.New("voice build has no model path — configure AI routing"), "model_unavailable"},
		"unbound routing ladder":  {errors.New("ai: task voice_build has no bound rung"), "model_unavailable"},
		"malformed model JSON":    {errors.New("voice build returned invalid JSON: unexpected end"), "invalid_output"},
		"fabricated evidence":     {errors.New(`voice build cited unknown sample "s-9"`), "invalid_output"},
		"non-verbatim quote":      {errors.New(`voice build signature move quote is not verbatim in sample "s-1"`), "invalid_output"},
		"anything else is opaque": {errors.New("connection reset by peer"), "internal"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := failureStatusCode(tc.err); got != tc.want {
				t.Fatalf("failureStatusCode(%v) = %s, want %s", tc.err, got, tc.want)
			}
		})
	}
}

func TestDeferralDeadlineHonorsTheTypedWindow(t *testing.T) {
	now := time.Date(2026, 7, 22, 8, 0, 0, 0, time.UTC)
	worker := &voiceBuildWorker{now: func() time.Time { return now }}

	window := now.Add(30 * 24 * time.Hour)
	typed := &ai.BudgetDeferralError{Task: ai.TaskVoiceBuild, NextAttemptAt: window}
	if got := worker.deferralDeadline(typed); !got.Equal(window) {
		t.Fatalf("typed deferral = %v, want the router's own window %v", got, window)
	}
	if got := worker.deferralDeadline(ai.ErrBudgetDeferred); !got.Equal(now.Add(voiceBuildDeferral)) {
		t.Fatalf("bare sentinel = %v, want the fixed fallback", got)
	}
	stale := &ai.BudgetDeferralError{Task: ai.TaskVoiceBuild, NextAttemptAt: now.Add(-time.Hour)}
	if got := worker.deferralDeadline(stale); !got.Equal(now.Add(voiceBuildDeferral)) {
		t.Fatalf("past window = %v, want the fixed fallback", got)
	}
}

func TestClaimTimeReadsTheClaimGeneration(t *testing.T) {
	at := time.Date(2026, 7, 22, 8, 0, 0, 0, time.UTC)
	input := ai.VoiceBuildInput{Build: ai.VoiceBuild{StartedAt: &at}}
	if got := claimTime(input); !got.Equal(at) {
		t.Fatalf("claimTime = %v, want %v", got, at)
	}
	if got := claimTime(ai.VoiceBuildInput{}); !got.IsZero() {
		t.Fatalf("claimTime without a claim = %v, want the never-matching zero fence", got)
	}
}

func TestVoiceBuildWorkerCtxBindsTheOwnerDelegate(t *testing.T) {
	ws, user := ids.NewV7(), ids.NewV7()
	ctx, err := voiceBuildWorkerCtx(context.Background(), VoiceBuildArgs{
		Workspace: ws.String(), ProfileID: ids.NewV7().String(),
		BuildID: ids.NewV7().String(), RequestedBy: user.String(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if ctx == nil {
		t.Fatal("a valid job must yield a bound context")
	}
	if _, err := voiceBuildWorkerCtx(context.Background(), VoiceBuildArgs{
		Workspace: "not-a-uuid", RequestedBy: user.String(),
	}); err == nil {
		t.Fatal("a malformed workspace id must be an explicit error")
	}
	if _, err := voiceBuildWorkerCtx(context.Background(), VoiceBuildArgs{
		Workspace: ws.String(), RequestedBy: "",
	}); err == nil {
		t.Fatal("a missing requester must be an explicit error — the runner acts as the owner's delegate")
	}
}

func TestPredecessorWordCountReadsStoredStats(t *testing.T) {
	if got := predecessorWordCount(nil); got != 0 {
		t.Fatalf("no predecessor = %d, want 0", got)
	}
	version := &ai.VoiceProfileVersion{StatsJSON: map[string]any{"word_count": float64(1234)}}
	if got := predecessorWordCount(version); got != 1234 {
		t.Fatalf("stored stats = %d, want 1234", got)
	}
	malformed := &ai.VoiceProfileVersion{StatsJSON: map[string]any{"word_count": "not-a-number"}}
	if got := predecessorWordCount(malformed); got != 0 {
		t.Fatalf("malformed stats = %d, want the honest zero fallback", got)
	}
}
