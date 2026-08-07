// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

// Package org360 holds the account-360 integration suites: what belongs on an
// account's page and what a caller may see of it — the timeline, the context
// graph, suggestions and their dismissal, signals advice, and the visit baseline.
//
// It is a suite package split out of internal/compose/integration so the lane
// has another scheduling slot. It rides the parent's exported Env fixture and its
// shared seeding and principal helpers — OwnerConn, SeedRow, LinkActivity,
// LinkToOrg, DealFixture, AccountMailDirectedAt, AgentWithOrgRead and the
// permission fixtures — and owns its own assertions.
//
// The production org360 service is imported as org360svc, because inside a
// package named org360 a bare org360.X reads as a self-reference.
package org360
