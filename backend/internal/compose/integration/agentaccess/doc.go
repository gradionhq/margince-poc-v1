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
// Three suites belong here by charter and are not here, for two different
// reasons — worth stating separately, because only one of them is a constraint.
//
// agentscope is pinned. It asserts the passport cap over real HTTP, which is this
// package's subject exactly, but it arranges through offerFixture and offerBody
// from offers_integration_test.go — another suite's _test.go file, which a
// subpackage cannot reach. Moving it means promoting those two first, and the
// arrange step must keep going through the real endpoints: a hand-inserted offer
// row would leave the human-only refusal proving nothing about an offer the
// product would actually have issued.
//
// approval_bundle and agenttools_http are NOT pinned — every helper they use is
// their own. They stayed because this slice was scoped to the admission path and
// they read as approvals and as the REST tool surface. Nothing prevents a later
// slice from taking them; mcp_transport already declares inline the two fields it
// needs of agenttools_http's wire shape, so that shape stays with the suite that
// owns it either way.
package agentaccess
