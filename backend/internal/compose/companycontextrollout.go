// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

import "github.com/jackc/pgx/v5/pgxpool"

const (
	companyContextRolloutOff        = "off"
	companyContextRolloutRead       = "read"
	companyContextRolloutTasks      = "tasks"
	companyContextRolloutOnboarding = "onboarding"
)

// WithCompanyContextRollout gives the HTTP surfaces the already-validated
// operator capability. The composition root is the only config reader.
func WithCompanyContextRollout(rollout string) Option {
	return func(s *Server, _ *pgxpool.Pool) {
		s.rollout = rollout
		s.companyContextRollout = rollout
	}
}

func companyContextReadEnabled(rollout string) bool {
	return rollout == "" || rollout == companyContextRolloutRead || rollout == companyContextRolloutTasks || rollout == companyContextRolloutOnboarding
}

func companyContextTasksEnabled(rollout string) bool {
	return rollout == "" || rollout == companyContextRolloutTasks || rollout == companyContextRolloutOnboarding
}

func companyContextOnboardingEnabled(rollout string) bool {
	return rollout == "" || rollout == companyContextRolloutOnboarding
}
