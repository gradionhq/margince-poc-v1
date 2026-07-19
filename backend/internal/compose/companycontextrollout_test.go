// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

import "testing"

func TestCompanyContextRolloutStagesAreMonotonic(t *testing.T) {
	tests := []struct {
		rollout                 string
		read, tasks, onboarding bool
	}{
		{rollout: companyContextRolloutOff},
		{rollout: companyContextRolloutRead, read: true},
		{rollout: companyContextRolloutTasks, read: true, tasks: true},
		{rollout: companyContextRolloutOnboarding, read: true, tasks: true, onboarding: true},
	}
	for _, test := range tests {
		t.Run(test.rollout, func(t *testing.T) {
			if companyContextReadEnabled(test.rollout) != test.read ||
				companyContextTasksEnabled(test.rollout) != test.tasks ||
				companyContextOnboardingEnabled(test.rollout) != test.onboarding {
				t.Fatalf("rollout %q resolved to read=%v tasks=%v onboarding=%v",
					test.rollout, companyContextReadEnabled(test.rollout),
					companyContextTasksEnabled(test.rollout), companyContextOnboardingEnabled(test.rollout))
			}
		})
	}
}
