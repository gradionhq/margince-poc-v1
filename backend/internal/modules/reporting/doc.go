// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// Package reporting is the compiled report engine (interfaces.md §3
// RunReport, crm.yaml runReport): a validated, typed plan — never free
// SQL. Field vocabulary is closed per report; every identifier that
// reaches the query text comes from the report's spec, and every value
// travels as a bind parameter. Reports read across the domain modules'
// tables, so the composition layer injects the schema-descriptor lookup
// and binds this engine into the one datasource seam and the HTTP surface.
package reporting
