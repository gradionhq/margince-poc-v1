// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package deployconfig

// The `capture:` block of margince.yaml: the suppression lists the mail
// pipeline's gates read.

// Capture is the deployment's mail-capture pipeline tuning (ADR-0063).
type Capture struct {
	// FreemailExtra and FreemailNever MOVED to the workspace's own
	// consumer-mail list (CAP-PARAM-5), editable in Settings and read per
	// transaction so a correction takes effect on the next message.
	//
	// They are still decoded, and deliberately: the file is parsed with
	// KnownFields(true), so deleting them outright would turn an upgrade into a
	// refusal to boot, reported by the yaml library in a message that names no
	// remedy and for a list the operator can no longer reach because the
	// process will not start. Values here are IGNORED; Warnings says so.
	FreemailExtra []string `yaml:"freemail_extra"`
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

// Warnings names the settings this block still accepts but no longer acts on,
// one sentence each, for a role to log at boot. Empty when the file says
// nothing stale — an operator who never set these hears nothing.
func (c Capture) Warnings() []string {
	var out []string
	if len(c.FreemailExtra) > 0 || len(c.FreemailNever) > 0 {
		out = append(out,
			"capture.freemail_extra / capture.freemail_never are ignored: the consumer-mail list moved to the workspace, editable under Settings or at POST /v1/capture/consumer-mail-domains. Remove them from margince.yaml.")
	}
	return out
}
