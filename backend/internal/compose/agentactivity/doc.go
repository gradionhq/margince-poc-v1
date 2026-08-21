// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// Package agentactivity serves one person's view of the scheduled agent's work:
// what is running for them now, and what settled today.
//
// Tables owned: NONE. It reads agent_run and runner_job, which the agents module
// owns, and it writes nothing at all — so it has no audit or outbox ride-along.
// It lives in compose because phase 2 unions the per-record run tables owned by
// several other modules, and a module never imports a sibling (ADR-0054).
//
// It serves FACTS, never sentences. The reader's locale decides the words, so a
// server-rendered string would freeze one language into the wire.
package agentactivity
