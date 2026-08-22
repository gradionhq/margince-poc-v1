// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// Package nextaction answers "what is the one thing to do next on this deal?"
// as a READ: it computes a recommendation from the deal's timeline and open
// promises, names the verb that would perform it and the arguments that verb
// takes, and performs nothing. The click is the client's, through the verb's
// own door, so the write shape and the approval gates stay where they are.
//
// A compose orchestration group (the named trigger: it reads deals and
// activities together and owns no table), in the shape of meetingbrief: a
// Service that assembles and a thin Handlers that serves it.
//
// The rules are deterministic and ordered, and every answer carries the
// evidence it rests on. Where the rules do not apply the answer is `none` with
// a reason — never a guess, never an empty body a card would render as "do
// nothing" without saying why.
package nextaction
