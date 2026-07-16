// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package storekit

import (
	"context"
	"testing"
)

// LogSystem stamps its actor from the authenticated principal, exactly like
// Audit — so a call with no actor bound is a programming error that must be
// refused BEFORE any SQL runs (a nil tx here proves it never reaches Exec).
func TestLogSystem_requiresActor(t *testing.T) {
	_, err := LogSystem(context.Background(), nil, "login", nil)
	if err == nil {
		t.Fatal("LogSystem with no actor bound must return an error, not write a row")
	}
}
