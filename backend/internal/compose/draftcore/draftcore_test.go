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

func bodyOf(d draft) string { return d.body }

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
		textlang.German, convstate.BandMonths, lane.write, bodyOf)
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
		textlang.English, convstate.BandMonths, lane.write, bodyOf)
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
		textlang.English, convstate.BandMonths, lane.write, bodyOf); err != nil {
		t.Fatal(err)
	}
	if len(lane.corrections) != 2 {
		t.Fatalf("a stubborn model should cost two calls and no more, got %d",
			len(lane.corrections))
	}
}

// A retry that makes things WORSE is discarded. A second attempt is not
// automatically better, and the count is the only evidence available without
// asking a model to judge its own output.
func TestTheBetterOfTheTwoAttemptsIsServed(t *testing.T) {
	lane := &scripted{bodies: []string{
		"Hi Priya, just checking in.",
		"Hi Priya, just checking in, as discussed, and touching base.",
	}}

	got, err := draftcore.CorrectOnce(context.Background(),
		textlang.English, convstate.BandMonths, lane.write, bodyOf)
	if err != nil {
		t.Fatal(err)
	}
	if got.body != lane.bodies[0] {
		t.Errorf("the worse retry should be discarded, got %q", got.body)
	}
}

// A retry that FAILS leaves the first draft standing. It carries the defect and
// it is still a real message a human can edit, which beats refusing to answer.
func TestAFailedRetryLeavesTheFirstDraftStanding(t *testing.T) {
	first := "Hi Priya, just checking in on the integration."
	lane := &failOnRetry{first: first}

	got, err := draftcore.CorrectOnce(context.Background(),
		textlang.English, convstate.BandMonths, lane.write, bodyOf)
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
		textlang.English, convstate.BandFresh, lane.write, bodyOf); err == nil {
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
