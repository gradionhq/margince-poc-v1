// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package ai

// A learning signal's retention boundary is written by ONE clock, the
// database's, derived from this package's source rather than remembered.
//
// privacy's erasure sweep selects on `retention_until < now()` INSIDE Postgres
// (retentionai.go), so a boundary bound from the app process makes that a
// cross-clock comparison. What the boundary decides is when stored plaintext
// stops existing: early destroys evidence a member still had a claim on, late
// keeps text past the window they were promised.
//
// Scope is this package because voice_learning_signal is owned here, pinned by
// tableownership_test.go — privacy reads and erases, and writes no schedule.

import (
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/gatekit"
)

func TestEveryLearningSignalRetentionWriteTakesTheDatabaseClock(t *testing.T) {
	gatekit.DatabaseClock{Dir: ".", Column: "retention_until"}.Require(t)
}
