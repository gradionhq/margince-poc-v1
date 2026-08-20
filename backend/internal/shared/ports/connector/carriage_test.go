// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package connector

// How a sender's carriage is read — the no-default rule, at the seam that owns
// it.

import "testing"

type declaredCarrier struct{ carriage Carriage }

func (c declaredCarrier) Carriage() Carriage { return c.carriage }

// A sender that does not implement AttachmentCarrier carries NOTHING. That is
// the seam's no-default rule: an adapter written before attachments existed, or
// one whose provider cannot carry them, must never be mistaken for capable —
// because the failure would be silent, and the record of what was sent would be
// permanently wrong.
func TestCarriageOfTreatsAnUndeclaredSenderAsCarryingNothing(t *testing.T) {
	if got := CarriageOf(struct{}{}); got.Carries {
		t.Errorf("an undeclared sender reported %+v, want Carries=false", got)
	}
	if got := CarriageOf(declaredCarrier{}); got.Carries {
		t.Errorf("a sender declaring the zero carriage reported %+v, want Carries=false", got)
	}
}

// The limits travel WHOLE. A descriptor that arrived with only its bool intact
// would gate on nothing: every bound the gate checks would read as "no limit".
func TestCarriageOfReportsTheDeclaredLimits(t *testing.T) {
	want := Carriage{Carries: true, MaxBytesPerFile: 25 << 20, MaxFiles: 10, MaxBodyWithFiles: 1024}
	if got := CarriageOf(declaredCarrier{carriage: want}); got != want {
		t.Errorf("carriageOf reported %+v, want %+v", got, want)
	}
}
