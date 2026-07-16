// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// Package automation is the closed catalog: a bounded set of trigger-and-action
// templates a user enables and parameterizes, and the runtime that fires them.
// It is deliberately not an engine — there is no expression language, no
// branching, and no user-defined trigger or action type. Adding a member is a
// code-and-test change, never data (ADR-0035).
//
// Tables owned: automation, workflow_run.
package automation
