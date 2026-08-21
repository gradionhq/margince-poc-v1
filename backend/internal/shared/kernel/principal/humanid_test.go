// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package principal

// The one spelling of "which person is this". Three callers parsed it
// separately before it lived here, and the failure they each had to avoid is
// the same one: reading a uuid out of a namespace that is not a person's.

import (
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

func TestHumanUserIDReadsAPersonAndOnlyAPerson(t *testing.T) {
	user := ids.NewV7()
	cases := []struct {
		name string
		id   string
		want ids.UUID
		ok   bool
	}{
		{"a person", HumanIDPrefix + user.String(), user, true},
		// A system namespace carrying a uuid is the case that matters: read
		// loosely, it files the system's work under whoever that uuid is.
		{"a system id that happens to carry a uuid", "system:" + user.String(), ids.Nil, false},
		{"an agent id", "agent:" + user.String(), ids.Nil, false},
		{"a human namespace with no uuid", HumanIDPrefix + "nobody", ids.Nil, false},
		{"an empty id", "", ids.Nil, false},
		{"a bare uuid with no namespace", user.String(), ids.Nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := HumanUserID(tc.id)
			if ok != tc.ok || got != tc.want {
				t.Fatalf("HumanUserID(%q) = %v/%t, want %v/%t", tc.id, got, ok, tc.want, tc.ok)
			}
		})
	}
}
