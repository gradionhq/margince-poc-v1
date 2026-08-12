// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package draftcore_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/compose/draftcore"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/convstate"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/textlang"
)

// draft is a stand-in for whatever shape a surface returns. The loop only ever
// reads its body, which is the point of taking a reader rather than a type.
type draft struct{ body string }

func bodyOf(d draft) (string, []string) { return d.body, nil }

// scripted answers with each body in turn and records the corrections it was
// given, so a test can assert both what came back and what the model was told.
type scripted struct {
	bodies      []string
	corrections []string
	err         error
}

func (s *scripted) write(_ context.Context, correction string) (draft, error) {
	s.corrections = append(s.corrections, correction)
	if s.err != nil {
		return draft{}, s.err
	}
	i := len(s.corrections) - 1
	if i >= len(s.bodies) {
		i = len(s.bodies) - 1
	}
	return draft{body: s.bodies[i]}, nil
}

// A clean draft is served as-is, and — the part that matters for cost — the
// model is called exactly once.
func TestACleanDraftIsNotRetried(t *testing.T) {
	lane := &scripted{bodies: []string{"Hallo Marek,\n\nder Vertrag ist unterschrieben."}}

	got, err := draftcore.CorrectOnce(context.Background(),
		textlang.German, convstate.BandMonths, lane.write, bodyOf, nil, nil)
	if err != nil {
		t.Fatalf("CorrectOnce errored on a clean draft: %v", err)
	}
	if len(lane.corrections) != 1 {
		t.Errorf("a clean draft should cost one call, got %d", len(lane.corrections))
	}
	if got.body != lane.bodies[0] {
		t.Errorf("the clean draft should be served unchanged, got %q", got.body)
	}
}

// A rejected phrase earns one retry, and the correction names the phrase — a
// model told only "try again" produces the same draft with new adjectives.
func TestARejectedPhraseEarnsOneRetryThatNamesIt(t *testing.T) {
	lane := &scripted{bodies: []string{
		"Hi Priya, just checking in on the integration.",
		"Hi Priya, the integration scope is ready. Is the project still live?",
	}}

	got, err := draftcore.CorrectOnce(context.Background(),
		textlang.English, convstate.BandMonths, lane.write, bodyOf, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(lane.corrections) != 2 {
		t.Fatalf("expected exactly one retry, got %d calls", len(lane.corrections))
	}
	if lane.corrections[0] != "" {
		t.Error("the first attempt should carry no correction")
	}
	if !strings.Contains(lane.corrections[1], "checking in") {
		t.Errorf("the correction should name the phrase, got %q", lane.corrections[1])
	}
	if got.body != lane.bodies[1] {
		t.Errorf("the corrected draft should be served, got %q", got.body)
	}
}

// One retry is the limit. A model that will not comply is not asked a third
// time — the cost is real and a deterministic floor sits underneath.
func TestTheModelIsNeverAskedMoreThanTwice(t *testing.T) {
	stubborn := "Hi Priya, just checking in as discussed."
	lane := &scripted{bodies: []string{stubborn, stubborn}}

	if _, err := draftcore.CorrectOnce(context.Background(),
		textlang.English, convstate.BandMonths, lane.write, bodyOf, nil, nil); err != nil {
		t.Fatal(err)
	}
	if len(lane.corrections) != 2 {
		t.Fatalf("a stubborn model should cost two calls and no more, got %d",
			len(lane.corrections))
	}
}

// A retry that makes things WORSE is discarded. Strictly worse: a TIE goes to
// the retry, because both attempts carry one finding often enough to matter —
// the model swaps "circling back" for "checking in" — and the retried one was
// at least written with the correction in hand.
func TestOnlyAStrictlyWorseRetryIsDiscarded(t *testing.T) {
	tied := &scripted{bodies: []string{
		"Hi Priya, just circling back on this.",
		"Hi Priya, just checking in on this.",
	}}
	got, err := draftcore.CorrectOnce(context.Background(),
		textlang.English, convstate.BandMonths, tied.write, bodyOf, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.body != tied.bodies[1] {
		t.Errorf("a tie should go to the corrected attempt, got %q", got.body)
	}

	lane := &scripted{bodies: []string{
		"Hi Priya, just checking in.",
		"Hi Priya, just checking in, as discussed, and touching base.",
	}}

	worse, err := draftcore.CorrectOnce(context.Background(),
		textlang.English, convstate.BandMonths, lane.write, bodyOf, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if worse.body != lane.bodies[0] {
		t.Errorf("the strictly worse retry should be discarded, got %q", worse.body)
	}
}

// A retry that FAILS leaves the first draft standing. It carries the defect and
// it is still a real message a human can edit, which beats refusing to answer.
func TestAFailedRetryLeavesTheFirstDraftStanding(t *testing.T) {
	first := "Hi Priya, just checking in on the integration."
	lane := &failOnRetry{first: first}

	got, err := draftcore.CorrectOnce(context.Background(),
		textlang.English, convstate.BandMonths, lane.write, bodyOf, nil, nil)
	if err != nil {
		t.Fatalf("a failed retry must not fail the draft: %v", err)
	}
	if got.body != first {
		t.Errorf("the first draft should stand, got %q", got.body)
	}
}

// A first attempt that fails IS a failure: there is nothing to serve, and the
// caller's own floor is the answer.
func TestAFailedFirstAttemptIsReturnedAsAnError(t *testing.T) {
	lane := &scripted{bodies: []string{"unused"}, err: errors.New("model unavailable")}

	if _, err := draftcore.CorrectOnce(context.Background(),
		textlang.English, convstate.BandFresh, lane.write, bodyOf, nil, nil); err == nil {
		t.Fatal("a failed first attempt should return its error")
	}
}

type failOnRetry struct {
	first string
	calls int
}

func (f *failOnRetry) write(context.Context, string) (draft, error) {
	f.calls++
	if f.calls > 1 {
		return draft{}, errors.New("model unavailable on retry")
	}
	return draft{body: f.first}, nil
}

// recorder captures what the loop reported, so the observability the loop took
// over from its callers is proven rather than assumed.
type recorder struct {
	failed     int
	notCleared []string
}

func (r *recorder) RetryFailed(context.Context, int, error) { r.failed++ }
func (r *recorder) RetryDidNotClear(_ context.Context, _, phrase string, _ int) {
	r.notCleared = append(r.notCleared, phrase)
}

// A retry that does not help is invisible from the outside — the caller gets a
// draft either way — so the loop has to say so. Both ways of not helping are
// reported, and a retry that DOES help says nothing.
func TestTheLoopReportsARetryThatDidNotHelp(t *testing.T) {
	stubborn := "Hi Priya, just checking in as discussed."
	seen := &recorder{}
	lane := &scripted{bodies: []string{stubborn, stubborn}}

	if _, err := draftcore.CorrectOnce(context.Background(),
		textlang.English, convstate.BandMonths, lane.write, bodyOf, nil, seen); err != nil {
		t.Fatal(err)
	}
	if len(seen.notCleared) != 1 {
		t.Errorf("surviving phrasing should be reported once, got %v", seen.notCleared)
	}
	if seen.failed != 0 {
		t.Error("a retry that answered is not a failed retry")
	}

	broken := &recorder{}
	if _, err := draftcore.CorrectOnce(context.Background(), textlang.English,
		convstate.BandMonths, (&failOnRetry{first: stubborn}).write, bodyOf, nil, broken); err != nil {
		t.Fatal(err)
	}
	if broken.failed != 1 {
		t.Errorf("a failed retry should be reported once, got %d", broken.failed)
	}

	quiet := &recorder{}
	clean := &scripted{bodies: []string{stubborn, "Hi Priya, the scope is ready."}}
	if _, err := draftcore.CorrectOnce(context.Background(),
		textlang.English, convstate.BandMonths, clean.write, bodyOf, nil, quiet); err != nil {
		t.Fatal(err)
	}
	if quiet.failed != 0 || len(quiet.notCleared) != 0 {
		t.Errorf("a retry that worked should report nothing, got %+v", quiet)
	}
}
