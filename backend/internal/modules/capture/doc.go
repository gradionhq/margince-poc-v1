// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// Package capture owns inbound capture (interfaces.md §1): the ONE
// connector.Sink implementation — a connector normalizes provider
// records, capture writes them. One transaction per record: raw
// original + domain row + audit entry (connector principal, never
// forgeable) + the captured event through the outbox, idempotent on
// the (source_system, source_id) natural key so replays are free.
//
// The registry holds the compiled-in connector set and enforces the
// grant-time scope intersection: a connector's declared scopes must be
// ⊆ the granting human's — connector ≤ human, exactly like agents.
//
// Tables owned: raw_capture, capture_connection. Imports shared +
// platform only; never a sibling module.
package capture
