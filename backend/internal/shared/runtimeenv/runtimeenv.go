// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// Package runtimeenv reads the deployment posture from MARGINCE_ENV. It is
// fail-closed by construction: unset or anything unrecognized is Production.
//
// What it decides is LICENSING, in two ways. Which issuers an installation
// honours — a production one trusts only the production authority, so a token
// minted by the test or dev licenser can never license a customer. And whether
// running unlicensed is permitted at all: a production role refuses to boot
// without a license, a non-production one does not.
//
// It used to decide one more thing, and should not have: whether the admin
// data-reset endpoint existed. Purging an installation's tenant data is not
// something to infer from the name a deployment was given, so it is now stated
// as operations.allow_data_reset in the deployment file, fail-closed in every
// posture including dev.
package runtimeenv

// EnvVar names the variable this posture is read from. Exported so the
// composition roots that read it do not each spell the string, which is how a
// configuration surface drifts from the one that is documented.
const EnvVar = "MARGINCE_ENV"

// Environment is the deployment posture derived from MARGINCE_ENV.
type Environment string

// The three recognized postures; every other value parses to Production.
//
// There is deliberately no `staging`. A staging installation carries real
// internal users and real data, so treating it as non-production meant it
// honoured dev-signed licenses — and, until operations.allow_data_reset, that
// its data could be purged through the API. MARGINCE_ENV=staging now parses to
// Production, which is the posture such an installation should always have had.
const (
	Production  Environment = "production"
	Development Environment = "dev"
	Test        Environment = "test"
)

// Parse maps a MARGINCE_ENV string to a posture. Unset / "production" / any
// unknown value ⇒ Production (fail-closed).
func Parse(s string) Environment {
	switch s {
	case string(Development):
		return Development
	case string(Test):
		return Test
	default:
		return Production
	}
}

// IsNonProduction reports whether this installation may honour the dev and test
// license authorities in addition to the production one, and may run with no
// license at all. Both consumers are in the licensing path; it gates nothing
// destructive — see the package comment.
func (e Environment) IsNonProduction() bool {
	return e == Development || e == Test
}
