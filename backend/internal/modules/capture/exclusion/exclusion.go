// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// Package exclusion is the pure personal-mail exclusion matcher (RC-2,
// capture.md CAP-DDL-3): given a normalized message's matchable attributes
// and a user's bounded rule set, does the message match any rule? It is
// the test-guarded surface — no I/O, no provider handle — so the gate the
// ONE Sink runs before ingestion is proven by fixtures, not a live mailbox.
// Deliberately not a filtering DSL: three fixed kinds (sender domain,
// recipient domain, mail label), exact case-insensitive equality, any-match.
package exclusion

import (
	"strings"

	"github.com/gradionhq/margince/backend/internal/shared/ports/connector"
)

// The three bounded rule kinds (CAP-DDL-3); no others exist.
const (
	KindSenderDomain    = "sender_domain"
	KindRecipientDomain = "recipient_domain"
	KindLabel           = "label"
)

// Rule is one bounded (kind, value) exclusion rule.
type Rule struct {
	Kind  string
	Value string
}

// Match reports the first rule (in the given order) the message attributes
// satisfy, and whether any matched. Comparison is case-insensitive on both
// sides. A rule with an empty value, or attributes with nothing to compare,
// never matches — so a non-mail record (zero attrs) always passes the gate.
func Match(attrs connector.ExclusionAttrs, rules []Rule) (Rule, bool) {
	for _, rule := range rules {
		value := strings.TrimSpace(strings.ToLower(rule.Value))
		if value == "" {
			continue
		}
		switch rule.Kind {
		case KindSenderDomain:
			if eq(attrs.SenderDomain, value) {
				return rule, true
			}
		case KindRecipientDomain:
			if containsFold(attrs.RecipientDomains, value) {
				return rule, true
			}
		case KindLabel:
			if containsFold(attrs.Labels, value) {
				return rule, true
			}
		}
	}
	return Rule{}, false
}

func eq(candidate, valueLower string) bool {
	return strings.TrimSpace(strings.ToLower(candidate)) == valueLower
}

func containsFold(candidates []string, valueLower string) bool {
	for _, c := range candidates {
		if eq(c, valueLower) {
			return true
		}
	}
	return false
}
