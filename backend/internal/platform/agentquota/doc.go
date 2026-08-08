// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// Package agentquota is the per-Passport volume accounting the MCP session used
// to stand in for (api-rate-limits-and-abuse §2.2/§2.4, byo-agent-and-mcp
// MCP-SESS-* and BYO-STEP-*, ADR-0092 §6): how much one agent has read,
// written, sent and spent inside one window, and what happens when it passes a
// threshold.
//
// WHY IT IS PER PASSPORT AND NOT PER SESSION. The spec names these quotas after
// a session, and until ADR-0092 the in-process session registry was what
// implicitly bounded volume — a bound that pinned an agent to one api replica
// and did not exist at all on the REST half of the same credential (ADR-0055
// made a Passport a REST credential governed exactly like MCP). ADR-0092 removes
// the registry and ratifies the per-Passport counters as its replacement. The
// Passport is the one thing both doors share, so it is the key that survives the
// change rather than the one it would have to undo.
//
// PER RECORD WHERE THE SPEC SAYS RECORDS, PER CALL WHERE IT SAYS CALLS. `reads`
// counts RECORDS, because the spec's own wording is that the bound exists "so a
// single search_records returning 5,000 rows trips it — closing the obvious
// evasion". `writes`, `egress` and `calls` count CALLS, because each of those is
// one act whatever it touched. `cost` counts model TOKENS, which is the unit the
// workspace AI budget is already denominated in and the only one available where
// the charge lands: an ai_call row stores tokens and never a price.
//
// THE LADDER IS THE POINT, NOT THE COUNTER. Crossing a threshold does four
// different things depending on which counter it was, and the difference is what
// makes the control usable rather than merely restrictive: `calls` and `egress`
// stop the agent for the window, `reads` and `writes` ask the human who lent the
// Passport whether to continue, and `cost` only says so. See Counter.
//
// FAIL-CLOSED. With no Redis, or a Redis that errors, the meter cannot know
// whether a threshold has been passed — and a control that cannot answer must
// not answer "no". Every governed counter reports the threshold as passed. The
// consequence is deliberate and worth stating plainly: while the counter is
// unreachable, agent traffic stops. Human sessions never enter this path; their
// authority is RBAC at the store.
//
// It lives in the platform tier and owns no domain: both doors onto the governed
// surface charge it — the MCP tool dispatcher and the ADR-0055 REST agent gate —
// and neither may import the other.
package agentquota
