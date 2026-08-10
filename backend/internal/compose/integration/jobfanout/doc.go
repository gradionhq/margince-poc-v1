// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

// Package jobfanout holds the suites that boot a REAL River worker in-process and
// wait on its event stream. That is the seam, not the subject matter: two of them
// assert per-tenant dispatch fan-out (agentscheduler, privacyretention) and two
// assert the agent runner those passes drive (runner, runnerreap), but what made
// them movable together is that each one starts a worker, and none of them owes an
// unexported helper to a suite that stayed.
//
// This package has a doc and its sibling suite packages (capture, org360, overlay)
// do not, because their names answer the question this one's does not: a suite
// named for a fan-out is not automatically at home here. webhookretry and
// embedreindex are periodic dispatch fan-outs too, and they stayed in package
// integration — moving either would have dragged its domain's fixtures
// (setupWebhooks, the embedding fakes) across the boundary, which is the test the
// parent package's doc sets for a split. They take the shared worker ceremony
// from integration/jobtest instead, which is why that ceremony is a package rather
// than a file here.
//
// So: a new suite belongs here when it needs a live worker AND its helpers travel
// with it. When only the first is true, leave it where its fixtures are and import
// jobtest.
package jobfanout
