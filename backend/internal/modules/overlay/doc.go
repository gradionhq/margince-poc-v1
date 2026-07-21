// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// Package overlay owns the HubSpot mirror overlay: a second
// datasource.SystemOfRecordProvider backed by a mirrored read cache of
// the customer's incumbent CRM (design.md, data-model.md §12). It covers
// the incumbent connection lifecycle, mirror sync health, the incumbent
// API budget, and the read-mode→overlay flip — the ops that have no
// SoR-mode equivalent (crm.yaml /overlay/*).
//
// This is the contract-first skeleton: every transport method declares
// its 501 until the mirror/provider/connection logic lands.
package overlay
