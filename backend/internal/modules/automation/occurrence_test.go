// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package automation

// The occurrence-key convention (Task 12, interfaces.md §5): a clock
// trigger's condition is continuously true once its anchor goes stale
// ("no activity for 7 days" stays true on day 8, 9, 10 …), so its
// IdempotencyKey must derive from the ANCHOR — the timestamp that makes
// the condition true — never from ev.ID, which a fresh scan pass mints
// every time regardless of whether the anchor moved. scriptedClockWorkflow
// proves the convention with a synthetic handler whose Match is flipped
// by the test, not real time; the real clock handlers (no_activity_reminder
// etc.) and the coarse time-scan that drives them are Task 14.
//
// This file has no build tag: it pins the DB-free routing decision
// (isClockTrigger) that the load-bearing proof — recordSkip must never
// claim a clock trigger's anchor key — depends on. That proof itself
// needs a real workspace transaction and lives in
// occurrence_integration_test.go.

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/gradionhq/margince/backend/internal/shared/ports/mcp"
	"github.com/gradionhq/margince/backend/internal/shared/ports/workflow"
)

// scriptedClockWorkflow is a synthetic clock-triggered handler: matches
// flips false→true the way a real anchor going stale would, without a
// real clock or time.Sleep — the test moves it directly. IdempotencyKey
// is anchor-derived, establishing the convention this task proves.
type scriptedClockWorkflow struct {
	name    string
	anchor  time.Time
	matches bool
	applies int // counts real Apply calls, so a test can prove at-most-once by CALL COUNT, not just row count
}

func (s *scriptedClockWorkflow) Spec() workflow.Spec {
	return workflow.Spec{Name: s.name, Trigger: workflow.Trigger{Schedule: "@daily"}, Tier: mcp.TierAutoExecute}
}

func (s *scriptedClockWorkflow) Match(_ context.Context, _ workflow.Event) (bool, error) {
	return s.matches, nil
}

func (s *scriptedClockWorkflow) Plan(_ context.Context, ev workflow.Event) (workflow.Effect, error) {
	args, err := json.Marshal(map[string]string{"note": "clock condition satisfied"})
	if err != nil {
		return workflow.Effect{}, err
	}
	return workflow.Effect{Actions: []workflow.Action{{
		Kind: workflow.ActionCreateTask, Target: ev.Entity, Args: args,
	}}}, nil
}

func (s *scriptedClockWorkflow) Apply(_ context.Context, _ workflow.Event, eff workflow.Effect, _ *workflow.ApprovalToken) (workflow.RunResult, error) {
	s.applies++
	return workflow.RunResult{Applied: eff.Actions}, nil
}

// IdempotencyKey is the anchor-based key the convention requires: stable
// while the anchor is unchanged (so a redelivered or repeatedly-false
// pass over the SAME anchor never mints a new claim), and different once
// the anchor moves (so the trigger re-arms). Never ev.ID — a clock scan
// mints a fresh one every pass regardless of the anchor.
func (s *scriptedClockWorkflow) IdempotencyKey(_ workflow.Event) string {
	return s.name + ":anchor:" + s.anchor.Format(time.RFC3339Nano)
}

func TestIsClockTriggerSplitsScheduleFromEventHandlers(t *testing.T) {
	clock := &scriptedClockWorkflow{name: "clock_probe", anchor: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	event := stageChangeCreateTask{} // a real production event-triggered handler, zero-value construction

	if !isClockTrigger(clock) {
		t.Errorf("isClockTrigger(%+v) = false, want true — a Schedule-bearing spec must route through the no-record path", clock.Spec())
	}
	if isClockTrigger(event) {
		t.Errorf("isClockTrigger(%+v) = true, want false — an EventType-bearing spec must still recordSkip on a non-match", event.Spec())
	}
}
