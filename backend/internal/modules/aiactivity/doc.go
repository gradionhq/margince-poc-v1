// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// Package aiactivity owns the projection behind the UI's AI-activity display:
// what the AI is doing for one person right now, and what it finished.
//
// Every FACT in it comes from the bus. Every AI-backed writer publishes
// ai_task.state_changed and this package's handler projects those events into
// ai_task_run; nothing outside this package writes the table, and no statement
// anywhere invents a fact the bus did not carry. That is why it imports no
// sibling module: the facts it needs arrive in the envelope, and a projection
// that reached back into a source's tables would be a second reader of a truth
// it is supposed to hold.
//
// The exceptions are RETENTION, and they are the reason this paragraph does not
// simply say "fed entirely from the bus" any more. PurgeSettledBefore drops rows
// the feed no longer reaches, and CloseAbandonedRouterRuns settles the ones
// whose source will never settle them. Neither carries an audit or outbox row,
// and neither needs one: this table is derived read-model state whose own
// migration disclaims the ride-along, because the events that FEED it carry the
// write shape at their own writers. Ageing a read model is not a domain
// mutation — but it IS a write, and a doc claiming otherwise is how the next
// author concludes the bus is the only thing that can change a row here.
//
// The state machine is NOT monotonic and that shapes everything here — a
// claimed occurrence can be released and re-claimed, so ordering is
// (attempt, state_rank) and never state alone. Ordering across rows is this
// package's own seq, never an emitter's clock.
//
// Tables owned: ai_task_run. Imports shared + platform only; never a sibling.
package aiactivity
