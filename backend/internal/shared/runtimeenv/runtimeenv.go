// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// Package runtimeenv reads the deployment posture from MARGINCE_ENV. It is
// fail-closed by construction: only an explicit, known non-production value
// enables dev-only trust switches (today: the admin data-reset endpoint);
// unset or anything unrecognized is Production, which disables them.
package runtimeenv

// Environment is the deployment posture derived from MARGINCE_ENV.
type Environment string

// The recognized deployment postures. Only the non-production values enable
// dev-only trust switches; every unrecognized value parses to Production.
const (
	Production  Environment = "production"
	Development Environment = "dev"
	Staging     Environment = "staging"
	Test        Environment = "test"
)

// Parse maps a MARGINCE_ENV string to a posture. Unset / "production" / any
// unknown value ⇒ Production (fail-closed).
func Parse(s string) Environment {
	switch s {
	case string(Development):
		return Development
	case string(Staging):
		return Staging
	case string(Test):
		return Test
	default:
		return Production
	}
}

// IsNonProduction reports whether dev-only destructive switches may run.
func (e Environment) IsNonProduction() bool {
	return e == Development || e == Staging || e == Test
}
