// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// Package automation is the closed catalog: a bounded set of trigger-and-action
// templates a user enables and parameterizes, and the runtime that fires them.
// It is deliberately not an engine — there is no expression language, no
// branching, and no user-defined trigger or action type. Adding a member is a
// code-and-test change, never data (ADR-0035).
//
// The shipped handlers live in handlers_event.go (bus-triggered) and
// handlers_clock.go (time-scan-triggered); the engine that fires them is
// engine.go + engine_run.go. The one place every workflow is
// REGISTERED — including the two that live in people (assign_lead_owner,
// lead_score_recompute) — is compose/workflows.go, so that file is the
// authoritative list of what runs.
//
// Tables owned: automation, workflow_run.
package automation
