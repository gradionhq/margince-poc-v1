// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package connector

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

// A machine reason is a short identifier by definition, but it is parsed out of
// bodies read up to megabytes and then logged. Anything that is not plainly a
// code must be dropped at this chokepoint rather than trusted because the
// provider is nominally reputable.
func TestMachineReasonAcceptsOnlyCodeShapedValues(t *testing.T) {
	for name, tc := range map[string]struct{ in, want string }{
		"google classic":      {"accessNotConfigured", "accessNotConfigured"},
		"google errorinfo":    {"SERVICE_DISABLED", "SERVICE_DISABLED"},
		"oauth code":          {"invalid_grant", "invalid_grant"},
		"microsoft code":      {"AADSTS700016", "AADSTS700016"},
		"dotted code":         {"foo.bar-baz", "foo.bar-baz"},
		"empty":               {"", ""},
		"prose is not a code": {"Google Calendar API has not been used in project", ""},
		"newline forgery":     {"ok\ntime=2026 level=ERROR msg=\"forged\"", ""},
		"carriage return":     {"ok\rmore", ""},
		"ansi escape":         {"\033[31mred", ""},
		"oversized":           {strings.Repeat("a", 65), ""},
		"at the bound":        {strings.Repeat("a", 64), strings.Repeat("a", 64)},
	} {
		t.Run(name, func(t *testing.T) {
			if got := MachineReason(tc.in); got != tc.want {
				t.Errorf("MachineReason(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// The whole point of the type: it must classify exactly as the bare sentinel it
// replaced, or a scheduling decision changes silently.
func TestProviderErrorClassifiesAsTheSentinelItWraps(t *testing.T) {
	wrapped := fmt.Errorf("provider: refused: %w", ErrAuthRejected)
	err := error(&ProviderError{Op: "/x", Status: 403, Reason: "someReason", Class: wrapped})

	if !errors.Is(err, ErrAuthRejected) {
		t.Error("errors.Is(err, ErrAuthRejected) = false, want true")
	}
	if errors.Is(err, ErrUnreachable) {
		t.Error("errors.Is(err, ErrUnreachable) = true, want false")
	}
	if got := ProviderReason(err); got != "someReason" {
		t.Errorf("ProviderReason = %q, want someReason", got)
	}
	// The provider's own message must still be readable through the chain.
	if msg := err.Error(); !strings.Contains(msg, "/x") || !strings.Contains(msg, "403") ||
		!strings.Contains(msg, "someReason") || !strings.Contains(msg, "refused") {
		t.Errorf("Error() = %q, want op, status, reason and the wrapped class", msg)
	}
}

// ProviderReason must not invent a reason for an error that carries none.
func TestProviderReasonIsEmptyForAPlainError(t *testing.T) {
	if got := ProviderReason(errors.New("plain")); got != "" {
		t.Errorf("ProviderReason(plain) = %q, want \"\"", got)
	}
	if got := ProviderReason(nil); got != "" {
		t.Errorf("ProviderReason(nil) = %q, want \"\"", got)
	}
}
