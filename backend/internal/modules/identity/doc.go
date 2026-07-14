// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// Package identity owns identity: workspaces, users, opaque server-side
// sessions (ADR-0043), RBAC roles, and the Agent Seat Passport. Auth is
// in-app — there is no separate identity service (P7 on-prem).
//
// Tables owned: workspace, app_user, team, team_membership, session,
// passport, role, role_assignment. Role policy documents live ONLY in
// internal/policy. Imports shared + platform + the
// generated contract only; never a sibling module — the workspace
// bootstrap's default-pipeline seed is injected at the composition root.
package identity
