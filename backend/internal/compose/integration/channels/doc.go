// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

// Package channels holds the messaging-channel integration suites: Telegram
// ingress end to end (the webhook, the normalized sink and its idempotency, the
// round trip back out, and erasure reaching the raw capture), and the channel
// identity a handle resolves to — its lifecycle, suppression, rebinding onto a
// new account, and the lock that erasure must respect.
//
// It is a suite package split out of internal/compose/integration so the lane
// has another scheduling slot: one package is one slot, and the parent is large
// enough to be the lane's long pole by itself. Measured, these suites are 14.8s
// of the parent's 160s. It rides the parent's exported fixtures and apptest's,
// and owns its own connector stubs, seeding and assertions.
//
// The boundary is where the fixtures fall, not where the names suggest. Three
// neighbours that read as if they belong here stay in the parent, each for the
// same reason — a fixture they share with suites that are not moving:
//
//   - channelsend and send_preflight share the preflight environment and the
//     Google scope constants with each other and with comms_send.
//   - comms_send and messageidentity build on that same preflight environment.
//   - channelsend_mcp asserts through the sealed tool envelope, whose helpers are
//     five types deep in the parent's _test.go files and reachable from nowhere
//     else.
//
// Moving any of them would mean promoting those fixtures to importable files for
// one caller, which trades a clear boundary for a wider one.
package channels
