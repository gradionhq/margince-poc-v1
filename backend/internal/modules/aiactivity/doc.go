// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// Package aiactivity owns the projection behind the UI's AI-activity display:
// what the AI is doing for one person right now, and what it finished.
//
// It is fed ENTIRELY from the bus. Every AI-backed writer publishes
// ai_task.state_changed; this package's handler projects those events into
// ai_task_run and nothing else writes the table. That is why it imports no
// sibling module: the facts it needs arrive in the envelope, and a projection
// that reached back into a source's tables would be a second reader of a truth
// it is supposed to hold.
//
// The state machine is NOT monotonic and that shapes everything here — a
// claimed occurrence can be released and re-claimed, so ordering is
// (attempt, state_rank) and never state alone. Ordering across rows is this
// package's own seq, never an emitter's clock.
//
// Tables owned: ai_task_run. Imports shared + platform only; never a sibling.
package aiactivity
