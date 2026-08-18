// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package zalopersonal

// The zalo*.go files are this unit's PROTOCOL LAYER: the QR login handshake,
// the session handshake that turns cookies into a working key, and the ONE
// authenticated call this change wires — send. They speak the personal-Zalo
// (Zalo Web) wire and nothing else.
//
// The roster read, the socket settings and the accessors a websocket would need
// are absent rather than waiting: this change connects an account and sends on
// it, and a call nothing invokes is a claim about reach this unit does not
// have. They land with the capture that needs them.
//
// They import nothing from the core, which is what keeps them testable: the
// whole layer is exercised end to end against an httptest.Server with no
// runtime, no database and no fake connector — the only way an undocumented,
// reverse-engineered protocol stays verifiable. The files are named for the
// layer rather than filed under it because a unit's tree holds exactly one Go
// package: a subpackage is parsed by nothing and would run its initializers
// unchecked at import.
//
// Their identifiers carry a `zalo` prefix wherever the unit layer names one, so
// the seam between "what this installation records" and "what the wire says"
// stays visible in a flat package.
//
// The shape in one paragraph: a personal session is a cookie jar plus a device
// identity. A member scans a QR to obtain the cookies; those cookies are traded
// for a session key (zpw_enk) on every process start; every later call encrypts
// its parameters under that key and signs them with an MD5 digest. So
// zaloSealed is the whole credential and everything zaloSession holds is
// re-derived by zaloResume, because cached protocol state that goes stale
// silently is how a connector reports "connected" while transmitting nothing.
//
// Inbound messages do not arrive over REST at all — they arrive on a websocket
// with its own payload key and framing, which this layer does not yet speak.
