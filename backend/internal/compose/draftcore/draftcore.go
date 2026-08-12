// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// Package draftcore holds what must not differ between drafting surfaces.
//
// There are three: the reply to an activity, the person composer, and
// account-started outbound. They differ in what a draft is grounded IN — an
// activity, a Person360, an Organization360 — and that difference is real and
// stays with each surface. What they share is the machinery around generation,
// and every time a copy of it drifted, a defect shipped on one surface and not
// the others.
//
// This package owns the correct-and-retry loop, which is the piece with the
// most judgement in it. The surface supplies two closures — write once, read
// what came back — and gets back the draft to serve. It holds no prompt text,
// no schema and no grounding: those are per-surface by design, and a package
// that owned them would be a fourth surface pretending to be a library.
//
// It lives in the composition layer because its consumers are compose
// subpackages that may not import each other (specs/subsystems/drafting.md,
// "Where it lives").
package draftcore

import (
	"context"

	"github.com/gradionhq/margince/backend/internal/compose/draftcheck"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/convstate"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/textlang"
)

// Writer produces one draft. correction is empty on the first attempt and
// carries the feedback naming what was wrong on the retry; the surface decides
// where in its own prompt that text belongs.
type Writer[D any] func(ctx context.Context, correction string) (D, error)

// Observer is told what the loop decided, so a surface keeps the operational
// visibility the loop takes over. Optional: nil observes nothing.
//
// It exists because a retry that does not help is invisible from the outside —
// the caller gets a draft either way — and "the model kept producing rejected
// phrasing" is exactly the signal that says a phrase list or a prompt rule
// needs work.
type Observer interface {
	// RetryFailed: the correction call itself errored, and the first draft
	// stands.
	RetryFailed(ctx context.Context, findings int, err error)
	// RetryDidNotClear: the retry answered, and rejected phrasing survived it.
	RetryDidNotClear(ctx context.Context, rule, phrase string, remaining int)
}

// TextOf reads the two channels of a draft a check has to judge.
//
// Both, because they fail differently and only one used to be read. The BODY is
// what the recipient sees. The REASONING labels are what the product tells the
// rep it wrote from, and a rep reads a chip less critically than the sentence
// they are about to send — so a wrong chip is the worse of the two. An invented
// "introduction by Romina Medici" reached one on a real thread while the body
// beside it was correct.
//
// The subject is deliberately absent: it is one line with its own rules, and
// folding it in would let a phrase banned in prose hide there.
type TextOf[D any] func(D) (body string, reasoning []string)

// SubjectOf reads a draft's subject line and whether a real inbound thread
// earns it a reply prefix. Separate from TextOf because a subject fails
// differently — its worst failure is a CLAIM ("Re:" says a thread exists)
// rather than a phrase — and because a surface with no subject supplies none.
type SubjectOf[D any] func(D) (subject string, threaded bool)

// CorrectOnce writes a draft, checks it against the correspondence it was
// written into, and gives the model exactly one chance to fix what it got wrong.
//
// One retry, because the alternatives are both worse. Zero leaves a defect the
// product knows about in text a human is about to send. Two or more is a model
// that will not comply, paid for twice more, while a deterministic floor sits
// underneath that was always going to be the answer.
//
// The correction names the exact phrase back to the model, which is the thing a
// prompt sentence cannot do — and the reason this loop exists at all is that
// three separate prompt rules lost to model reflexes before it did.
//
// When the retry does not clear the findings, the attempt carrying FEWER of them
// is served. A second attempt is not automatically better, and the count is the
// only evidence available without asking a model to judge its own output.
func CorrectOnce[D any](
	ctx context.Context, lang textlang.Lang, band convstate.Band,
	write Writer[D], textOf TextOf[D], subjectOf SubjectOf[D], observe Observer,
) (D, error) {
	check := func(draft D) []draftcheck.Finding {
		body, reasoning := textOf(draft)
		findings := append(draftcheck.Body(body, lang, band),
			draftcheck.Reasoning(reasoning, lang, band)...)
		if subjectOf != nil {
			subject, threaded := subjectOf(draft)
			findings = append(findings, draftcheck.Subject(subject, lang, band, threaded)...)
		}
		return findings
	}

	draft, err := write(ctx, "")
	if err != nil {
		var zero D
		return zero, err
	}

	findings := check(draft)
	if len(findings) == 0 {
		return draft, nil
	}

	retried, retryErr := write(ctx, draftcheck.Feedback(findings))
	if retryErr != nil {
		// The first attempt stands. It carries the defect and it is still a
		// real message a human can edit, which beats refusing to answer.
		if observe != nil {
			observe.RetryFailed(ctx, len(findings), retryErr)
		}
		return draft, nil
	}

	remaining := check(retried)
	if len(remaining) == 0 {
		return retried, nil
	}
	if observe != nil {
		observe.RetryDidNotClear(ctx, remaining[0].Rule, remaining[0].Phrase, len(remaining))
	}
	// A TIE goes to the retry. Both attempts carry one finding often enough to
	// matter — the model swaps "circling back" for "checking in" — and the
	// retried one was at least written with the correction in hand, so it is
	// the better bet on everything the check does not measure. Only a retry
	// that is strictly worse is discarded.
	if len(remaining) <= len(findings) {
		return retried, nil
	}
	return draft, nil
}
