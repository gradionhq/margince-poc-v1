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

// BodyOf reads the prose a draft would put in front of a recipient. The check
// judges the body alone: a subject is one line with its own rules, and a
// concatenation would let a phrase banned in prose hide in a subject.
type BodyOf[D any] func(D) string

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
	write Writer[D], bodyOf BodyOf[D],
) (D, error) {
	draft, err := write(ctx, "")
	if err != nil {
		var zero D
		return zero, err
	}

	findings := draftcheck.Body(bodyOf(draft), lang, band)
	if len(findings) == 0 {
		return draft, nil
	}

	retried, retryErr := write(ctx, draftcheck.Feedback(findings))
	if retryErr != nil {
		// The first attempt stands. It carries the defect and it is still a
		// real message a human can edit, which beats refusing to answer.
		return draft, nil
	}
	if len(draftcheck.Body(bodyOf(retried), lang, band)) < len(findings) {
		return retried, nil
	}
	return draft, nil
}
