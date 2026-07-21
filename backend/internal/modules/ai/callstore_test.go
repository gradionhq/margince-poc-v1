// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package ai

import (
	"errors"
	"fmt"
	"testing"
)

func TestClassifyError(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"nil is success", nil, ""},
		{"budget deferred", fmt.Errorf("wrap: %w", ErrBudgetDeferred), "budget_deferred"},
		{"served but metering failed", fmt.Errorf("wrap: %w", errMeteringFailed), "metering_failed"},
		{"other is provider_error", errors.New("connection reset"), "provider_error"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := classifyError(tc.err); got != tc.want {
				t.Fatalf("classifyError = %q; want %q", got, tc.want)
			}
		})
	}
}
