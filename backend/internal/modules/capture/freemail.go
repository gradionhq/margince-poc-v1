// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package capture

// The free-mail gate (CAP-PARAM-5): consumer mail domains that must never
// derive an organization — alice@gmail.com is a person, "Gmail" is not her
// company. The gate suppresses ORG derivation only; the person is still
// created. The list itself lives in platform/freemail, because the same answer
// is owed at the other end of the path — people's counterparty ensure is the
// chokepoint every creation route enters, and the two modules cannot import
// each other. This file is only capture's handle on it.

import "github.com/gradionhq/margince/backend/internal/platform/freemail"

// FreemailList answers "is this a consumer mail domain?" for the capture tier
// ladder, against the pinned baseline plus the workspace's own additions and
// carve-outs.
type FreemailList struct {
	matcher *freemail.Matcher
}

// NewFreemailList builds the gate. extra adds domains the baseline misses,
// never carves out a domain the baseline wrongly claims; both are the
// workspace's own lists and may be nil.
func NewFreemailList(extra, never []string) *FreemailList {
	return &FreemailList{matcher: freemail.New(extra, never)}
}

// IsFreemail reports whether domain is a consumer mail domain, so organization
// derivation must be suppressed for it. Subdomains of a listed provider match
// too — "mail.gmx.net" is the same mailbox service as "gmx.net".
func (l *FreemailList) IsFreemail(domain string) bool {
	return l.matcher.IsConsumer(domain)
}
