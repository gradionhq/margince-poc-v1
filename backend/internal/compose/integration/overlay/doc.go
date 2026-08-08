// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

// Package overlay holds the incumbent-CRM mirror's integration suites: the
// acceptance surface and its budget, mirror-backed reads and their fail-closed
// visibility, write-back and its audit, the continuous sync, the user map, the
// tool surface, and the ADR-0071 overlay→native cutover with its flip and claim.
//
// A suite package split out of internal/compose/integration so the lane has
// another scheduling slot. Its suites ride integration.SearchEnv and
// integration/apptest.AppEnv.
//
// overlay_acceptance_seam_test.go deliberately stayed behind. It carries no
// integration build tag, so it belongs to the unit lane rather than this one, and
// it owns backendModuleRoot, which a webhooks suite there also reads.
//
// The production overlay module is imported as overlaymod, because inside a
// package named overlay a bare overlay.X reads as a self-reference.
package overlay
