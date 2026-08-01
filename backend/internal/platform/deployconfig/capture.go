// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package deployconfig

// The `capture:` block of margince.yaml: the suppression lists the mail
// pipeline's gates read. Each list is a deployment-level default beneath the
// workspace's own lists, not a replacement for them.

// Capture is the deployment's mail-capture pipeline tuning (ADR-0063).
type Capture struct {
	// FreemailExtra appends deployment-specific consumer mail domains to
	// the pinned baseline blocklist (CAP-PARAM-5): mail from these domains
	// still creates the person, never a company.
	FreemailExtra []string `yaml:"freemail_extra"`
	// FreemailNever carves a domain back out of the consumer-mail baseline
	// (CAP-PARAM-5): the shipped dataset is a third-party list, and a wrong
	// entry would otherwise bar an operator's real customers from ever having a
	// company. It wins over the baseline and over FreemailExtra.
	FreemailNever []string `yaml:"freemail_never"`
	// TransactionalExtra appends deployment-specific mail-infrastructure
	// eSLDs to the pinned baseline (CAP-PARAM-6, ADR-0072): mail from these
	// senders keeps the activity but derives no counterparty at all.
	TransactionalExtra []string `yaml:"transactional_extra"`
	// TransactionalNever is the operator allowlist of registrable domains
	// that must never be suppressed as transactional (CAP-PARAM-6) — it wins
	// over every baseline/prefix rule.
	TransactionalNever []string `yaml:"transactional_never"`
}
