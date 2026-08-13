// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package draftcheck

// A subject line fails differently from the prose beneath it.
//
// Its worst failure is a CLAIM rather than a phrase: "Re:" asserts that a
// message was received, and a client renders that assertion as a thread whether
// or not one exists. It is also one line, which is why the body's phrase lists
// are not simply reused here — "Follow-up" alone is a claim in a subject where
// it needs a sentence around it to be one in prose.

import (
	"strings"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/convstate"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/textlang"
)

// SubjectMaxRunes is where a subject line stops being read.
//
// Mail clients truncate around here, and a subject that needs more than this
// is a first sentence rather than a label. The number is the conventional one
// rather than any client's exact cut, because every client's differs and the
// point is to stay short of all of them.
const SubjectMaxRunes = 70

// replyPrefixes are the ways a client marks a subject as a reply, in the
// languages this product writes.
var replyPrefixes = []string{"re:", "aw:", "fwd:", "wg:", "antw:"}

// Subject reads a draft's subject line against the correspondence it belongs to.
//
// Separate from Body because a subject fails differently: it is one line, it is
// read before anything else, and its worst failure is a claim rather than a
// phrase. "Re:" says a thread exists. A follow-up subject says there was a
// previous message. Both are checkable facts the envelope already holds, which
// is why they are refused here rather than explained in a prompt.
func Subject(subject string, lang textlang.Lang, band convstate.Band, threaded bool) []Finding {
	trimmed := strings.TrimSpace(subject)
	lowered := strings.ToLower(trimmed)
	var findings []Finding

	if trimmed == "" {
		return []Finding{{
			Rule:   "empty-subject",
			Phrase: "",
			Why:    "a message with no subject line arrives looking like spam",
		}}
	}

	for _, prefix := range replyPrefixes {
		if !strings.HasPrefix(lowered, prefix) {
			continue
		}
		if !threaded {
			findings = append(findings, Finding{
				Rule:   "unearned-reply-prefix",
				Phrase: strings.TrimSuffix(prefix, ":"),
				Why: "there is no inbound thread with this subject, so the prefix claims " +
					"a message that was never received",
			})
		}
		break
	}

	// A follow-up subject at band none says there was something before this.
	// There was not: this is the first message.
	if band == convstate.BandNone {
		for _, phrase := range append(append([]string{},
			assumedMemory[lang]...), firstTouchSubjects[lang]...) {
			if contains(lowered, phrase) {
				findings = append(findings, Finding{
					Rule:   "invented-history-subject",
					Phrase: phrase,
					Why:    "this is a first message, so the subject cannot refer back to anything",
				})
				break
			}
		}
	}

	if n := len([]rune(trimmed)); n > SubjectMaxRunes {
		findings = append(findings, Finding{
			Rule:   "long-subject",
			Phrase: trimmed[:40] + "…",
			Why: "a subject this long is truncated by the client that shows it, so the " +
				"part that carries the meaning may never be read",
		})
	}
	return findings
}

// firstTouchSubjects are the subject-line formulas that imply a previous
// message. They overlap the body's assumed-memory list and are not the same:
// a subject is a label, so "Follow-up" alone is a claim there where it needs a
// sentence around it to be one in prose.
var firstTouchSubjects = map[textlang.Lang][]string{
	textlang.English:    {"follow-up", "follow up", "checking in", "touching base", "reminder"},
	textlang.German:     {"nachfassen", "nachfrage", "erinnerung", "wiedervorlage"},
	textlang.Vietnamese: {"nhắc lại", "tiếp theo"},
}

// Feedback turns findings into the correction a regeneration prompt carries.
// One line per finding, naming the phrase and why it is wrong here, because a
// model told only "try again" produces the same draft with different adjectives.
func Feedback(findings []Finding) string {
	if len(findings) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n\nThe previous draft was rejected. Rewrite it, and this time:\n")
	for _, f := range findings {
		b.WriteString("- Do not write \"" + f.Phrase + "\" or any synonym of it. " + f.Why + ".\n")
	}
	// A correction that only says what to delete gets the nearest synonym back:
	// told to drop "circling back", the model returns "checking in", which is
	// the same sentence. So the retry is told what to WRITE — a message with a
	// reason to exist does not need a re-contact formula at all.
	b.WriteString("Open on the substance instead. Name what the message is about " +
		"in your own words and ask one question they can answer. A message that " +
		"opens on why you are writing needs no phrase for the act of writing.\n")
	return b.String()
}
