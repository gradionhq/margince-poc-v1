// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

// Package webhooks holds the outbound-webhook integration suites: the subscription
// surface and its delivery, the published event envelope checked against
// api/public-events.yaml, and the retry and fan-out behaviour when an endpoint is
// slow, failing, or one of several.
//
// It is a suite package split out of internal/compose/integration so the lane has
// another scheduling slot: one package is one slot, and the parent is large enough
// to be the lane's long pole by itself. Its suites ride the parent's exported
// fixtures and integration/apptest's.
//
// Every outbound-webhook suite moved; none stayed behind, so there is no boundary
// exception to record here.
package webhooks
