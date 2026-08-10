// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// Package finance owns the ingested finance mirror (ADR-0083/A128,
// finance-ingestion.md): a bounded READ-ONLY import of an accounting
// source, so the company page can answer whether a customer actually
// pays us, and on time.
//
// It is a mirror, not an integration. The capability exposes no create
// or update action on a finance record at all, and that posture is
// expressed as the ABSENCE of a grant on the permission surface
// (FIN-DDL-N-1) rather than as a runtime check — there is no code path
// to reach and no flag to set wrongly. ADR-0094's removal of invoice
// creation is unchanged in every particular: reading our own already
// issued invoices back out of our own accounting system creates no
// artifact and asserts no tax position.
//
// The mirror is subordinate. A customer link maps two identifiers and
// never merges: an accounting customer never becomes an organization,
// and an unmapped one is a visible state rather than an auto-created
// company.
//
// Money is integer minor units in the issued currency, converted through
// the existing effective-dated rate sheet and frozen on issue date
// (DM-FX-4). A missing rate is the existing refusal — never a silent
// conversion and never a zero.
//
// Tables owned: finance_connection, finance_external_customer,
// finance_customer_link, finance_invoice, finance_payment.
package finance
