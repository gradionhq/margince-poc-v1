// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// Package draftfloor holds the prose skeleton of a draft written without a
// model, in the language of the correspondence and honest about how long it
// has been silent.
//
// Four places in the product write an email with no model available: the person
// composer, the account composer, the timeline's own fallback, and the
// warm-intro path in signals. All four spelled their skeleton in hardcoded
// English, and all four opened a first message to a stranger with "Following
// up" — an invented history, which is exactly what DRAFT-AC-E-3 forbids. Four
// copies also means a fix to one is a fix to one.
//
// So the skeleton is one table here, indexed by band and language, and each
// caller keeps its own wire shape, its own reasoning assembly, and its own
// ranking of what to say. This package supplies only the words around them.
//
// It is in the shared tier because two of the four callers are modules
// (activities, signals) that sit below composition and may not import a
// sibling. Stdlib-only, so it stays a Tier-0 leaf.
package draftfloor

import (
	"strings"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/convstate"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/textlang"
)

// DefaultLang is what an unresolved language falls back to. English, as the
// last rung of the resolution ladder (DRAFT-AC-E-2) — a floor draft in a
// language nobody asked for is still a draft the rep can edit, where no draft
// at all is a broken screen.
const DefaultLang = textlang.English

// Phrases is the prose skeleton for one band in one language.
//
// Every field is a whole clause rather than a fragment to be assembled, because
// the three languages do not agree on where the pieces go and a template that
// concatenates them produces German that reads translated.
type Phrases struct {
	// SubjectNoContext is the subject when nothing better is known. At band
	// none it names a reason to write; it is never a follow-up line, because
	// there is nothing to follow up on.
	SubjectNoContext string
	// SubjectAbout builds a subject around a topic that IS known - a deal
	// name, an employer. The verb "%s" is the topic.
	SubjectAbout string
	// GreetingNamed and GreetingAnonymous open the message. Named takes the
	// recipient's first name.
	GreetingNamed     string
	GreetingAnonymous string
	// Opener acknowledges where the conversation stands, before the message
	// says anything of its own. Empty at band fresh, where a live exchange
	// needs no preamble and one reads as padding.
	Opener string
	// Ask closes the message with the one request it makes.
	Ask string
}

// The table. Twelve cells: four bands in three languages.
//
// The German is written as German rather than translated from the English, and
// uses Sie throughout — the register a drafter should default to when nothing
// tells it otherwise. The Vietnamese likewise uses the neutral business
// register.
var table = map[textlang.Lang]map[convstate.Band]Phrases{
	textlang.English: {
		convstate.BandNone: {
			SubjectNoContext:  "Getting in touch",
			SubjectAbout:      "About %s",
			GreetingNamed:     "Hi %s,",
			GreetingAnonymous: "Hello,",
			Opener:            "I am writing to you for the first time, so I will keep this short.",
			Ask:               "Would a short call in the next week or two be useful?",
		},
		convstate.BandFresh: {
			SubjectNoContext:  "Following up",
			SubjectAbout:      "Re: %s",
			GreetingNamed:     "Hi %s,",
			GreetingAnonymous: "Hello,",
			Opener:            "",
			Ask:               "Would a short call this week suit you?",
		},
		convstate.BandWeeks: {
			SubjectNoContext:  "Picking this back up",
			SubjectAbout:      "Re: %s",
			GreetingNamed:     "Hi %s,",
			GreetingAnonymous: "Hello,",
			Opener:            "It has been a few weeks since we last spoke, so here is where things stand.",
			Ask:               "Would a short call in the next week or two be useful?",
		},
		convstate.BandMonths: {
			SubjectNoContext:  "Picking this back up",
			SubjectAbout:      "Re: %s",
			GreetingNamed:     "Hi %s,",
			GreetingAnonymous: "Hello,",
			Opener:            "It has been a long time since we were last in touch, so I will not assume any of this is still current.",
			Ask:               "Is this still worth a conversation on your side?",
		},
	},
	textlang.German: {
		convstate.BandNone: {
			SubjectNoContext:  "Kurze Anfrage",
			SubjectAbout:      "Anfrage zu %s",
			GreetingNamed:     "Hallo %s,",
			GreetingAnonymous: "Guten Tag,",
			Opener:            "ich melde mich zum ersten Mal bei Ihnen und fasse mich deshalb kurz.",
			Ask:               "Wäre ein kurzes Gespräch in den nächsten zwei Wochen sinnvoll?",
		},
		convstate.BandFresh: {
			SubjectNoContext:  "Nachfassen",
			SubjectAbout:      "Re: %s",
			GreetingNamed:     "Hallo %s,",
			GreetingAnonymous: "Guten Tag,",
			Opener:            "",
			Ask:               "Passt Ihnen ein kurzes Gespräch in dieser Woche?",
		},
		convstate.BandWeeks: {
			SubjectNoContext:  "Wir nehmen das Thema wieder auf",
			SubjectAbout:      "Re: %s",
			GreetingNamed:     "Hallo %s,",
			GreetingAnonymous: "Guten Tag,",
			Opener:            "seit unserem letzten Austausch sind einige Wochen vergangen, deshalb hier der aktuelle Stand.",
			Ask:               "Wäre ein kurzes Gespräch in den nächsten zwei Wochen sinnvoll?",
		},
		convstate.BandMonths: {
			SubjectNoContext:  "Wir nehmen das Thema wieder auf",
			SubjectAbout:      "Re: %s",
			GreetingNamed:     "Hallo %s,",
			GreetingAnonymous: "Guten Tag,",
			Opener:            "unser letzter Kontakt liegt lange zurück, deshalb setze ich nichts davon als noch aktuell voraus.",
			Ask:               "Ist das Thema auf Ihrer Seite noch relevant?",
		},
	},
	textlang.Vietnamese: {
		convstate.BandNone: {
			SubjectNoContext:  "Xin được liên hệ",
			SubjectAbout:      "Về %s",
			GreetingNamed:     "Chào %s,",
			GreetingAnonymous: "Xin chào,",
			Opener:            "Đây là lần đầu tôi liên hệ với anh/chị nên tôi xin trình bày ngắn gọn.",
			Ask:               "Anh/chị có thể dành một cuộc trao đổi ngắn trong hai tuần tới không?",
		},
		convstate.BandFresh: {
			SubjectNoContext:  "Tiếp theo trao đổi",
			SubjectAbout:      "Re: %s",
			GreetingNamed:     "Chào %s,",
			GreetingAnonymous: "Xin chào,",
			Opener:            "",
			Ask:               "Anh/chị có thể dành một cuộc trao đổi ngắn trong tuần này không?",
		},
		convstate.BandWeeks: {
			SubjectNoContext:  "Xin phép trao đổi lại",
			SubjectAbout:      "Re: %s",
			GreetingNamed:     "Chào %s,",
			GreetingAnonymous: "Xin chào,",
			Opener:            "Đã vài tuần kể từ lần trao đổi trước, tôi xin cập nhật tình hình hiện tại.",
			Ask:               "Anh/chị có thể dành một cuộc trao đổi ngắn trong hai tuần tới không?",
		},
		convstate.BandMonths: {
			SubjectNoContext:  "Xin phép trao đổi lại",
			SubjectAbout:      "Re: %s",
			GreetingNamed:     "Chào %s,",
			GreetingAnonymous: "Xin chào,",
			Opener:            "Đã lâu chúng ta chưa liên hệ nên tôi không mặc định rằng mọi việc vẫn như trước.",
			Ask:               "Việc này còn phù hợp với anh/chị không?",
		},
	},
}

// For returns the skeleton for a band in a language.
//
// An unknown language falls back to DefaultLang rather than returning an error:
// the caller is already on the no-model path, and a floor draft that refuses to
// render leaves the screen with nothing. An unrecognized band falls back to
// BandNone, which is the conservative end of the axis — it assumes no history,
// and assuming none where some exists costs a sentence of context, while
// assuming some where none exists is the fabrication this package removes.
func For(lang textlang.Lang, band convstate.Band) Phrases {
	byBand, ok := table[lang]
	if !ok {
		byBand = table[DefaultLang]
	}
	phrases, ok := byBand[band]
	if !ok {
		phrases = byBand[convstate.BandNone]
	}
	return phrases
}

// Subject renders the subject line for a draft.
//
// topic is what the message is about — a deal name, an employer, the subject of
// the thread being answered — or empty when nothing is known. threaded says
// whether topic came from a real inbound thread subject, which is the only
// thing that earns a reply prefix: "Re:" on a message nobody replied to is a
// claim that a thread exists, and at band none it is always false
// (DRAFT-AC-E-3).
func Subject(lang textlang.Lang, band convstate.Band, topic string, threaded bool) string {
	phrases := For(lang, band)
	topic = strings.TrimSpace(topic)
	if topic == "" {
		return phrases.SubjectNoContext
	}
	if !threaded || band == convstate.BandNone {
		return fill(For(lang, convstate.BandNone).SubjectAbout, topic)
	}
	return fill(phrases.SubjectAbout, topic)
}

// Greeting renders the opening line, by name when one is known.
func Greeting(lang textlang.Lang, band convstate.Band, firstName string) string {
	phrases := For(lang, band)
	firstName = strings.TrimSpace(firstName)
	if firstName == "" {
		return phrases.GreetingAnonymous
	}
	return fill(phrases.GreetingNamed, firstName)
}

// fill substitutes the single "%s" in a template without going through the
// formatting verbs, so a topic or a name containing a percent sign renders as
// itself rather than as a format directive.
func fill(template, value string) string {
	return strings.Replace(template, "%s", value, 1)
}
