// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// Package quotas owns the quota aggregate (RD-T06): a per-owner XOR
// per-team revenue target over an explicit period, with a human-set
// target_minor (RD-PARAM-3 — never AI-guessed or server-computed).
// Store CRUD with keyset listing, optimistic If-Match updates that
// re-validate the XOR contract on the MERGED state, and idempotent
// archive returning the full entity, flat per ADR-0054 §3.
//
// Tables owned: quota.
//
// Quota rows follow the pipeline/product config posture: workspace-
// shared, governed by the `quota` object grants alone — never
// row-scoped. The owner_id/team_id columns name the MEASURED subject
// (whose target this is), not an access owner, so quota stays out of
// auth's ownerScopedTables and no EnsureVisible probe runs: anyone with
// quota.read sees every target in the workspace (the RD-T06 leaderboard
// read), while RLS walls off other tenants.
//
// Imports shared + platform + the generated contract only; never a
// sibling module. Every mutation rides storekit's audit shape inside
// one transaction; the events.md §5 catalog defines no quota.* type, so
// the writes are ratified audit-only (backend/writeshape_test.go).
package quotas
