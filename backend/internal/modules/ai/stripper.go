// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package ai

import (
	"context"
	"regexp"
	"sort"

	"github.com/gradionhq/margince/backend/internal/shared/ports/model"
)

// NewSecretStripper builds the credential-hygiene pass that runs over
// every model-bound payload (ai-operational-spec §4.2). It removes
// secrets — API keys, tokens, private keys, password assignments —
// irreversibly. It is NOT a PII filter: names, emails and phone numbers
// pass through untouched, because privacy is the location ladder (A8
// revised), and pretending a regex protects PII would be a false
// guarantee.
func NewSecretStripper() model.SecretStripper {
	return secretStripper{rules: stripRules}
}

type stripRule struct {
	kind string
	re   *regexp.Regexp
	// keepPrefix preserves the first capture group (the "password=" part
	// of an assignment) so the surrounding JSON/text stays well-formed
	// and the redaction is visible in place of the value alone.
	keepPrefix bool
	// keepSuffix preserves the LAST capture group after the redaction —
	// the structural tail a value sits inside (the "@" that closes URL
	// userinfo). RE2 has no lookahead, so a rule that must assert such a
	// trailing anchor has to consume it and put it back.
	keepSuffix bool
}

// The patterns work on both plain text and JSON-encoded text: none may
// match across a double quote or backslash, so replacing inside a
// marshaled request body can never break the JSON framing.
var stripRules = []stripRule{
	// PEM private keys. (?s) spans lines; in JSON the newlines arrive as
	// literal \n escapes, which .*? crosses just the same.
	{kind: "private_key", re: regexp.MustCompile(`(?s)-----BEGIN [A-Z ]*PRIVATE KEY-----.*?-----END [A-Z ]*PRIVATE KEY-----`)},
	// Vendor-prefixed API keys (Anthropic/OpenAI sk-, GitHub gh*_,
	// Slack xox, Google AIza, AWS AKIA/ASIA).
	{kind: "api_key", re: regexp.MustCompile(`\bsk-[A-Za-z0-9_-]{16,}`)},
	// Stripe-style underscore keys (sk_/rk_/pk_ live|test) — a DIFFERENT
	// shape from the sk- rule above, which its hyphen anchor never reaches;
	// a bare Stripe secret with no api_key: prefix would otherwise egress
	// verbatim.
	{kind: "api_key", re: regexp.MustCompile(`\b[srp]k_(?:live|test)_[0-9A-Za-z]{16,}`)},
	{kind: "api_key", re: regexp.MustCompile(`\bgh[pousr]_[A-Za-z0-9]{20,}`)},
	{kind: "api_key", re: regexp.MustCompile(`\bxox[baprs]-[A-Za-z0-9-]{10,}`)},
	{kind: "api_key", re: regexp.MustCompile(`\bAIza[0-9A-Za-z_-]{30,}`)},
	{kind: "aws_access_key", re: regexp.MustCompile(`\b(?:AKIA|ASIA)[0-9A-Z]{16}\b`)},
	// AWS SECRET access key: the 40-char value, not the AKIA id above. It
	// carries no self-identifying prefix, so it is anchored on its key name
	// — and that name (aws_secret_access_key) is one underscore-joined
	// token, so the generic credential_assignment rule's \b never fires
	// inside it. Matched here with the id-safe base64 alphabet.
	{kind: "aws_secret_key", keepPrefix: true,
		re: regexp.MustCompile(`(?i)\b((?:aws[_-]?)?secret[_-]?access[_-]?key["']?\s*[:=]\s*["']?)([A-Za-z0-9/+]{40})`)},
	// URL-embedded credentials (scheme://user:PASSWORD@host). The password
	// is redacted in place; the trailing @ is consumed (RE2 cannot look
	// ahead) and restored, so host:port after it is never mistaken for
	// user:password.
	{kind: "url_credential", keepPrefix: true, keepSuffix: true,
		re: regexp.MustCompile(`(://[^/\s:@"'\\]+:)([^/\s@"'\\]{2,})(@)`)},
	// Signed JWTs (three base64url segments) before the generic bearer
	// rule, so the kind names what was actually caught.
	{kind: "jwt", re: regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}`)},
	{kind: "bearer_token", re: regexp.MustCompile(`(?i)\bbearer[ \t]+[A-Za-z0-9._~+/=-]{16,}`)},
	// key=value / key: value credential assignments; the value stops at
	// whitespace, quotes and separators so only the secret itself goes.
	{kind: "credential_assignment", keepPrefix: true,
		re: regexp.MustCompile(`(?i)\b((?:password|passwd|pwd|secret|api[_-]?key|apikey|access[_-]?token|auth[_-]?token|client[_-]?secret|private[_-]?key)["']?\s*[:=]\s*["']?)([^\s"'\\,;&]{4,})`)},
}

type secretStripper struct {
	rules []stripRule
}

func (s secretStripper) Strip(_ context.Context, payload []byte) ([]byte, model.StripReport, error) {
	report := model.StripReport{}
	kinds := map[string]bool{}
	for _, rule := range s.rules {
		payload = rule.re.ReplaceAllFunc(payload, func(match []byte) []byte {
			report.Findings++
			kinds[rule.kind] = true
			if rule.keepPrefix || rule.keepSuffix {
				groups := rule.re.FindSubmatch(match)
				out := []byte{}
				if rule.keepPrefix {
					out = append(out, groups[1]...)
				}
				out = append(out, []byte("[SECRET-REMOVED:"+rule.kind+"]")...)
				if rule.keepSuffix {
					out = append(out, groups[len(groups)-1]...)
				}
				return out
			}
			return []byte("[SECRET-REMOVED:" + rule.kind + "]")
		})
	}
	for k := range kinds {
		report.Kinds = append(report.Kinds, k)
	}
	sort.Strings(report.Kinds)
	return payload, report, nil
}
