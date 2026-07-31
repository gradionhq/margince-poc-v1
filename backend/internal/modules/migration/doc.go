// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// Package migration is the shared importer engine
// (import-export-migration, IEM-FORM-1): one mapping/classification
// step, one dry-run, one checkpointed resumable run loop — sources plug
// in as connectors behind the Source seam, and native rows land through
// the Writers seam so this module never imports the record modules it
// feeds (compose injects both). The overlay→native flip is its first
// caller (OVA-WIRE-8 runs the engine against the frozen mirror
// snapshot); the direct migrate-in connectors (HubSpot / Salesforce /
// CSV, UC-E11-03) plug into the same engine in their own tickets.
package migration
