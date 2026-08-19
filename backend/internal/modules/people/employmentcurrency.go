// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package people

// What "currently employed" means, in one place. Its own file because it is one
// concept with two readers' worth of reach: the write paths in relationship.go
// decide the flag with it, and four separate READS derive currency with it
// rather than trusting a flag written months earlier.

import "github.com/gradionhq/margince/backend/internal/platform/database/storekit"

// EmploymentIsCurrentSQL is the ONE spelling of "this job is still theirs", and
// the only definition of a current employment in this product. `date` is the
// end-date expression at the call site: a column on a read, the incoming value
// on a create, the patched-or-existing one on an update.
//
// A DATE COMPARISON, not a null check: somebody serving three months' notice
// still works there. Reading the column's mere presence as "gone" took a person
// off their employer's contact list the day their notice was filed, with no way
// back, because `ended_at` cannot be cleared through the API.
//
// `> current_date`, so an employment dated TODAY is already over. That is what
// `ended_at` means in this schema — 0007 documents NULL as "current/ongoing", so
// a date that has arrived is a date that has happened — and it is what keeps the
// rail's "End employment" button doing something the moment it is pressed. A
// future date is the only case that is not yet a departure, which is exactly the
// notice period this predicate exists for.
//
// current_date, evaluated by Postgres. A Go-side comparison would answer a
// different question on a server in a different timezone from the database, and
// every reader of this predicate is SQL that knows only the database's own day.
//
// EXPORTED because currency is not decided once and stored. The flag records
// which employer represents the person; whether that employment is still current
// is a function of today's date, so every READER derives it instead of trusting
// a value written months ago. compose reaches this for the same reason the
// readers in this package do — one definition, or the copies drift.
func EmploymentIsCurrentSQL(date string) string {
	return storekit.SQLf("(%s IS NULL OR %s > current_date)", date, date)
}

// CurrentPrimaryEmploymentSQL is what a READER of `is_current_primary` means:
// the flag AND the employment still being theirs. Spelled once so a new reader
// cannot trust the flag alone, which is what let somebody go on counting at a
// company after their last day had passed.
//
// READERS, not every mention of the column. The uniqueness guards must stay
// date-BLIND and deliberately do — `employmentedge.go`, `domaintriageresolve.go`
// and both merge relink paths ask "is the flag already taken" to keep
// uq_rel_current_primary_employer satisfied, and that index's own predicate
// knows nothing about dates. A guard that used this helper would think the slot
// was free while the index still held it, and answer 409 instead of skipping.
// Two different questions about one column; this one is "who works there now".
func CurrentPrimaryEmploymentSQL(alias string) string {
	prefix := ""
	if alias != "" {
		prefix = alias + "."
	}
	return storekit.SQLf("%sis_current_primary AND %s", prefix, EmploymentIsCurrentSQL(prefix+"ended_at"))
}
