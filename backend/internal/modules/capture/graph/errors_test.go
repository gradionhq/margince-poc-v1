// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package graph

import (
	"errors"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/ports/connector"
)

// The registry schedules by the shared vocabulary; a package sentinel that
// stops answering it silently turns a parkable auth failure into a
// backoff-retried one (or worse).
func TestPackageSentinelsSpeakTheSharedVocabulary(t *testing.T) {
	if !errors.Is(ErrAuthRejected, connector.ErrAuthRejected) {
		t.Fatal("ErrAuthRejected must wrap connector.ErrAuthRejected")
	}
	if !errors.Is(ErrUnreachable, connector.ErrUnreachable) {
		t.Fatal("ErrUnreachable must wrap connector.ErrUnreachable")
	}
	if !errors.Is(ErrDeltaGone, connector.ErrCursorGone) {
		t.Fatal("ErrDeltaGone must wrap connector.ErrCursorGone")
	}
}
