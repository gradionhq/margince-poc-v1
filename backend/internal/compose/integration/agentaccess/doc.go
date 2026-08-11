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
// reflect that: they share the connector harness, and each mints passports,
// and splitting them would put a token's issue and its presentation in different
// binaries.
//
// It is a suite package split out of internal/compose/integration so the lane has
// another scheduling slot: one package is one slot, and the parent is large enough
// to be the lane's long pole by itself. Its suites ride integration's exported
// fixtures and integration/apptest's.
//
// Where the boundary fell, and it is fixture entanglement rather than subject
// matter. agentscope and approval_bundle mint passports and assert on admission
// too, so they belong here by charter; they stayed because the assertions they
// turn on — assertNothingStaged and the approval-inbox queries — live in the
// parent's _test.go files, which a subpackage cannot reach at all. The fixture
// they DID share is now apptest.PassportBearer, so what remains behind is those
// assertions, not the minting.
//
// agenttools_http stayed for the cheaper version of the same reason: it pins the
// full wire shape of /v1/agent-tools, and mcp_transport needs two fields of it,
// which it declares inline rather than dragging the shape across the boundary.
package agentaccess
