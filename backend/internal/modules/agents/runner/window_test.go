// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package runner

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/ports/workflow"
)

// triggerRef is the shape the defect was found on: an occurrence reference that
// renders exactly like the record refs one line below it.
const triggerRef = "calendar:0198f3a1-7c42-7e0b-9d51-2a6f4b8c1e02"

// A record id comes from something the run READ. The trigger ref is the one
// `<type>:<uuid>` in the window that names no record — it is why the run exists,
// not something it may prepare against — and it is printed in the runner's own
// voice one line above grounding refs of an identical shape. So wherever it
// appears, the line it appears on says what it is; a grounding ref, which IS a
// record, must not pick up the same label.
func TestTheTriggerReferenceIsNeverPrintedWithoutSayingWhatItIs(t *testing.T) {
	win := newWindow(Job{
		Goal:       "what do I need to know before the Acme meeting?",
		TriggerRef: triggerRef,
		Grounding: []Grounding{
			{SourceID: "org:0198f3a1-7c42-7e0b-9d51-2a6f4b8c1e10", TrustTier: "T1", Content: "Acme GmbH"},
		},
	}, nil, nil)

	// Assert the whole line BEFORE the property below, which is a loop: a loop
	// over lines that never matches passes by finding nothing, so dropping the
	// trigger line — or renaming what introduces it — would satisfy it vacuously.
	if labelled := "Trigger: " + triggerRef + " (" + triggerProvenance + ")"; !strings.Contains(win.msgs[0].Content, labelled) {
		t.Fatalf("the goal prompt does not carry the labelled trigger %q: %q", labelled, win.msgs[0].Content)
	}
	// EXACTLY one line carries the clause. Asserting "no grounding line carries
	// it" would be the weaker property and, with one producer, a loop that cannot
	// fail; counting catches both directions — the clause leaking onto the
	// grounding refs, which are records, and a second producer appearing.
	var labelled, named int
	for _, line := range strings.Split(win.msgs[0].Content, "\n") {
		if strings.Contains(line, triggerProvenance) {
			labelled++
		}
		if strings.Contains(line, triggerRef) {
			named++
		}
	}
	if labelled != 1 || named != 1 {
		t.Fatalf("the goal prompt must name the trigger once and say what it is once, got %d/%d: %q",
			named, labelled, win.msgs[0].Content)
	}
	if !strings.Contains(win.system, triggerProvenance) {
		t.Fatalf("the system frame does not say where a record id comes from: %q", win.system)
	}
}

// The rule lives in the system frame and the label lives in the goal turn, and
// those are the only two places in this window nothing can elide. A long run is
// exactly when a model has the most `<type>:<uuid>` strings in front of it, so a
// provenance rule that the ceiling could trim would be absent when it is needed
// most.
func TestTheProvenanceRuleOutlivesEveryObservationTheCeilingElides(t *testing.T) {
	win := newWindow(Job{Goal: "prep the meeting", TriggerRef: triggerRef}, nil, nil)
	for i := 0; i < 50; i++ {
		win.observe("read_record", strings.Repeat("x", 4000)+fmt.Sprintf("-%d", i))
	}

	req := win.asRequest(1000)
	if req.Messages[1].Content != elisionMarker {
		t.Fatalf("this test proves nothing unless the window actually elided: %q", req.Messages[1].Content)
	}
	if !strings.Contains(req.System, triggerProvenance) {
		t.Fatalf("the rule did not survive the ceiling: %q", req.System)
	}
	if !strings.Contains(req.Messages[0].Content, triggerRef+" ("+triggerProvenance+")") {
		t.Fatalf("the labelled trigger did not survive the ceiling: %q", req.Messages[0].Content)
	}
}

// A suspended run's system frame is rebuilt, not replayed — only the transcript
// is snapshotted. So a run that comes back hours later, having been approved by
// a human, is told where a record id comes from on exactly the same terms as a
// run that never stopped.
func TestAResumedRunIsStillToldWhereARecordIdComesFrom(t *testing.T) {
	staging := &fakeSurface{errs: map[string]error{
		"send_email": &workflow.StagedApprovalError{ApprovalID: ids.New[ids.ApprovalKind]()},
	}}
	job := Job{Goal: "follow up after the meeting", TriggerRef: triggerRef}
	suspended, err := New(staging, &scriptedBrain{texts: []string{
		`{"tool":"send_email","args":{"to":"a@b.c"}}`,
	}}).Run(context.Background(), job)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if suspended.Pending == nil {
		t.Fatalf("expected a suspension to resume from: %+v", suspended)
	}

	brain := &scriptedBrain{texts: []string{`{"final":{"summary":"the mail went out"}}`}}
	resuming := &fakeSurface{results: map[string]json.RawMessage{"send_email": json.RawMessage(`{"sent":true}`)}}
	if _, err := New(resuming, brain).Resume(context.Background(), job,
		Decision{Pending: *suspended.Pending, Approved: true}); err != nil {
		t.Fatalf("resume: %v", err)
	}
	if len(brain.requests) == 0 {
		t.Fatal("the model was never asked, so there is no frame to assert on")
	}
	req := brain.requests[0]
	if !strings.Contains(req.System, triggerProvenance) {
		t.Fatalf("the resumed frame does not say where a record id comes from: %q", req.System)
	}
	if !strings.Contains(req.Messages[0].Content, triggerProvenance) {
		t.Fatalf("the resumed transcript lost the labelled trigger: %q", req.Messages[0].Content)
	}
}
