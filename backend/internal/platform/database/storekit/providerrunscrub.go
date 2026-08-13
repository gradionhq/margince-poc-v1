// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package storekit

// The provider-run scrub, spelled ONCE.
//
// Two paths detach a subject from the runs that bought data about them, and
// they must remove exactly the same columns: the delete-data action a customer
// triggers per provider (modules/integrations), and the Art. 17 erasure a
// subject triggers for themselves (modules/privacy, both the delete arm and
// the anonymize-in-place arm). They were written separately once and diverged
// — the erasure cleaned two of the six columns the ordinary delete cleaned,
// so exercising a legal right removed LESS than a settings toggle did.
//
// Both live here because the seam between them is a module boundary: privacy
// may not import integrations, and integrations may not import privacy.

// ScrubProviderRunColumns is what "this run names nobody" means. Each column
// is a way back to the subject:
//
//   - person_id names them outright;
//   - subject_kind must say so, or the row's CHECK constraint still demands a
//     person id;
//   - input_fingerprint is a hash OF their identifiers;
//   - provider_job_id would let the provider be re-asked for the same answer;
//   - requested_by names the colleague who asked about them;
//   - configuration_snapshot can carry identifying configuration.
//
// What survives is what the installation SPENT: the state, the reservations
// and the dates, attached to nobody. That is deliberate — an erasure removes
// the subject, not the accounting (PI-AC-8), which is what lets a spend
// history stay stable across one.
// It is a SET clause rather than a whole statement on purpose. The callers
// write their own UPDATE around it — the fitness gates that prove erasure
// reaches a table scan for the table name in the erasing package's own source,
// and a shared function that swallowed the statement would make three real
// erasure arms invisible to them. Sharing the CLAUSE keeps the columns from
// drifting; keeping the statement local keeps the gates able to see it.
const ScrubProviderRunColumns = `
	person_id = NULL, subject_kind = 'scrubbed',
	input_fingerprint = '', provider_job_id = NULL,
	requested_by = NULL, configuration_snapshot = '{}'::jsonb`
