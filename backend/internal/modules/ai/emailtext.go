// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package ai

// Own-authored email extraction removes transport headers, quoted history,
// forwarded blocks and legal footers before sent mail can become voice evidence.

import (
	"regexp"
	"strings"
)

var (
	replyBoundary      = regexp.MustCompile(`(?i)^\s*(?:on .+ wrote:|am .+ schrieb .+:|from:\s|von:\s|-{2,}\s*(?:original message|ursprüngliche nachricht)\s*-{2,})`)
	disclaimerBoundary = regexp.MustCompile(`(?i)^\s*(?:this (?:e-?mail|message) (?:and any attachments )?is confidential|diese (?:e-?mail|nachricht) ist vertraulich|confidentiality notice|datenschutzhinweis)`)
	personalMailTerms  = regexp.MustCompile(`(?i)\b(?:strictly personal|private matter|medical|doctor|therapy|family matter|school meeting|bank account|persönlich|privatangelegenheit|arzt|therapie|familiensache|schultermin|bankkonto)\b`)
)

// ExtractOwnEmailText removes transport and quoted counterparty material.
func ExtractOwnEmailText(body string) string {
	lines := strings.Split(strings.ReplaceAll(body, "\r\n", "\n"), "\n")
	kept := make([]string, 0, len(lines))
	for index, line := range lines {
		trimmed := strings.TrimSpace(line)
		if index < 3 && (strings.HasPrefix(strings.ToLower(trimmed), "from:") || strings.HasPrefix(strings.ToLower(trimmed), "to:")) {
			continue
		}
		if replyBoundary.MatchString(trimmed) || disclaimerBoundary.MatchString(trimmed) || trimmed == "--" {
			break
		}
		if strings.HasPrefix(trimmed, ">") {
			continue
		}
		kept = append(kept, strings.TrimRight(line, " \t"))
	}
	return strings.TrimSpace(strings.Join(kept, "\n"))
}

// ExcludeEmailFromVoice keeps sensitive or content-free mail out of storage.
func ExcludeEmailFromVoice(subject, body string) (string, bool) {
	if personalMailTerms.MatchString(subject + "\n" + body) {
		return "possible personal or sensitive correspondence", true
	}
	if WordCount(body) < 20 {
		return "too little own-authored text", true
	}
	return "", false
}
