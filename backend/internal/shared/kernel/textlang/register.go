// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package textlang

// Whether a German correspondence is on du or Sie terms.
//
// This is the same shape of problem as the language, and it was left to the
// prompt for one release longer. The rule said "address the recipient as Sie
// unless the correspondence shows both sides already using du", which is
// correct and unusable: the model re-decides it on every call, and on a real
// thread carrying four du-forms and four Sie-forms it answers differently each
// time. Two consecutive drafts to the same person, one du and one Sie, is worse
// than either — it reads as a machine that does not know who it is writing to.
//
// So the register is resolved once, from the correspondence, and travels in the
// envelope beside the language. The model is told which one to use rather than
// asked to work it out.

import (
	"regexp"
	"strings"
)

// Register is how a German draft addresses its recipient.
type Register string

const (
	// RegisterUnknown: no German correspondence to read, or nothing decisive
	// in it. The caller falls back, and Sie is the safe fallback in business
	// correspondence — being too formal is a smaller error than being too
	// familiar with somebody who never invited it.
	RegisterUnknown Register = ""
	// RegisterDu: both sides are on du terms.
	RegisterDu Register = "du"
	// RegisterSie: the formal address.
	RegisterSie Register = "Sie"
)

// The du and Sie evidence, as whole words.
//
// Sie is capitalized in its polite sense and lowercase "sie" means she/they, so
// the Sie pattern is deliberately case-SENSITIVE while the du pattern is not.
// That one distinction does most of the work: without it every "sie haben" in
// ordinary prose counts as formal address.
var (
	duForms  = regexp.MustCompile(`(?i)\b(du|dir|dich|dein|deine|deinem|deinen|deiner|deines|euch|euer|eure|eurem|euren|eurer)\b`)
	sieForms = regexp.MustCompile(`\b(Sie|Ihnen|Ihr|Ihre|Ihrem|Ihren|Ihrer|Ihres)\b`)
)

// RegisterMargin is how far ahead one register must be before the
// correspondence is read as settled on it.
//
// A single stray form decides nothing: a thread on du terms still contains
// "Ihre Unterlagen" about a third party, and a formal thread quotes a du-form
// from somebody else's forwarded mail. Real evidence is repeated.
const RegisterMargin = 2

// DetectRegister reads which register a German correspondence is on.
//
// Ambiguity answers Unknown rather than guessing. That is not a failure: the
// caller's fallback is Sie, and the honest reading of "this thread uses both
// equally" is that nothing here says to be familiar.
func DetectRegister(text string) Register {
	if strings.TrimSpace(text) == "" {
		return RegisterUnknown
	}
	// The quoted thread counts here, unlike in language detection. Which
	// register two people are on is a property of the RELATIONSHIP, and the
	// history is where the evidence for it lives — a single reply may contain
	// neither form while the exchange behind it is unmistakably du.
	du := len(duForms.FindAllString(text, -1))
	sie := len(sieForms.FindAllString(text, -1))

	switch {
	case du >= sie+RegisterMargin:
		return RegisterDu
	case sie >= du+RegisterMargin:
		return RegisterSie
	default:
		return RegisterUnknown
	}
}

// HasBothRegisters reports whether text contains du AND Sie forms at all.
//
// Distinct from DetectRegister answering Unknown, which also covers text with
// neither: this asks whether one piece of writing mixes them, which is a defect
// in a draft even though it is ordinary in a long thread.
func HasBothRegisters(text string) bool {
	return duForms.MatchString(text) && sieForms.MatchString(text)
}
