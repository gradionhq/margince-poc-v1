// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

// Package agentaccess holds the integration suites for how a non-human caller is
// admitted and what it may then do: the OAuth surface and its discovery documents,
// dynamic client registration, consent and its refusals, the grant, refresh,
// revocation and lending of tokens, the passports those tokens carry, and the MCP
// transport that presents them — handshake, framing, deadlines and the task
// surface — plus the query vocabulary the surface publishes.
//
// The credentials and the transport are one package on purpose. ADR-0055 governs a
// passport identically whether it arrives on REST or on /mcp, and the suites
// reflect that: they share the connector harness and the minted-passport fixture,
// and splitting them would put a token's issue and its presentation in different
// binaries.
//
// It is a suite package split out of internal/compose/integration so the lane has
// another scheduling slot: one package is one slot, and the parent is large enough
// to be the lane's long pole by itself. Its suites ride integration's exported
// fixtures and integration/apptest's.
//
// Where the boundary fell, since the names do not predict it. agentscope and
// approval_bundle stayed in the parent even though they mint passports — they turn
// on approvals, not on admission, and the fixture they shared is now
// apptest.PassportBearer. agenttools_http stayed too: it pins the wire shape of
// /v1/agent-tools, which mcp_transport only reads two fields of.
package agentaccess
