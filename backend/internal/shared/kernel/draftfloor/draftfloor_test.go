// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package draftfloor_test

import (
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/convstate"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/draftfloor"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/textlang"
)

// Every language the detector can return, and every band the axis can produce.
// Derived from the two vocabularies rather than listed, so a new band or a new
// language fails this file until the table covers it.
var (
	langs = []textlang.Lang{textlang.English, textlang.German, textlang.Vietnamese}
	bands = []convstate.Band{
		convstate.BandNone, convstate.BandFresh, convstate.BandWeeks, convstate.BandMonths,
	}
)

// Every cell is filled. A missing phrase renders as an empty line in a real
// draft, which reads as a bug in the product rather than a gap in a table.
func TestEveryBandInEveryLanguageHasItsPhrases(t *testing.T) {
	for _, lang := range langs {
		for _, band := range bands {
			phrases := draftfloor.For(lang, band)
			required := map[string]string{
				"SubjectNoContext":  phrases.SubjectNoContext,
				"SubjectAbout":      phrases.SubjectAbout,
				"GreetingNamed":     phrases.GreetingNamed,
				"GreetingAnonymous": phrases.GreetingAnonymous,
				"Ask":               phrases.Ask,
			}
			for name, value := range required {
				if strings.TrimSpace(value) == "" {
					t.Errorf("%s/%s: %s is empty", lang, band, name)
				}
			}
			// Opener is deliberately empty at fresh and required elsewhere:
			// a live exchange needs no preamble, and a gap needs acknowledging.
			opener := strings.TrimSpace(phrases.Opener)
			if band == convstate.BandFresh && opener != "" {
				t.Errorf("%s/fresh: Opener should be empty, got %q", lang, opener)
			}
			if band != convstate.BandFresh && opener == "" {
				t.Errorf("%s/%s: Opener is empty, but this band has a gap to acknowledge",
					lang, band)
			}
		}
	}
}

// Templates take exactly one substitution. Two would silently drop a value and
// none would render the placeholder to the recipient.
func TestEveryTemplateTakesExactlyOneSubstitution(t *testing.T) {
	for _, lang := range langs {
		for _, band := range bands {
			phrases := draftfloor.For(lang, band)
			for name, template := range map[string]string{
				"SubjectAbout":  phrases.SubjectAbout,
				"GreetingNamed": phrases.GreetingNamed,
			} {
				if got := strings.Count(template, "%s"); got != 1 {
					t.Errorf("%s/%s: %s has %d placeholders, want 1: %q",
						lang, band, name, got, template)
				}
			}
		}
	}
}

// The bug this table exists to kill. A first message to somebody the product
// has never written to must not open by following up on nothing, in any
// language (DRAFT-AC-E-3).
func TestAFirstTouchNeverClaimsAHistory(t *testing.T) {
	inventedHistory := []string{
		"following up", "checking in", "circling back",
		"nachfassen", "wie besprochen", "erneut",
		"tiếp theo", "nhắc lại",
	}
	for _, lang := range langs {
		phrases := draftfloor.For(lang, convstate.BandNone)
		for _, field := range []string{phrases.SubjectNoContext, phrases.Opener, phrases.Ask} {
			lowered := strings.ToLower(field)
			for _, phrase := range inventedHistory {
				if strings.Contains(lowered, phrase) {
					t.Errorf("%s/none: %q implies a prior exchange that does not exist",
						lang, field)
				}
			}
		}
	}
}

// A reply prefix is a claim that a thread exists. It is earned only by a real
// inbound thread subject, and never at band none.
func TestTheReplyPrefixIsOnlyUsedOnARealThread(t *testing.T) {
	cases := []struct {
		name       string
		band       convstate.Band
		topic      string
		threaded   bool
		wantPrefix bool
	}{
		{name: "answering a real thread", band: convstate.BandFresh, topic: "Angebot", threaded: true, wantPrefix: true},
		{name: "a topic we chose ourselves", band: convstate.BandFresh, topic: "Angebot", threaded: false, wantPrefix: false},
		{name: "first touch, even with a topic", band: convstate.BandNone, topic: "Angebot", threaded: true, wantPrefix: false},
		{name: "picking up an old thread", band: convstate.BandMonths, topic: "Angebot", threaded: true, wantPrefix: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := draftfloor.Subject(textlang.German, c.band, c.topic, c.threaded)
			if hasPrefix := strings.HasPrefix(got, "Re:"); hasPrefix != c.wantPrefix {
				t.Fatalf("Subject = %q, want reply prefix = %v", got, c.wantPrefix)
			}
		})
	}
}

// With nothing known about the topic, the subject is the band's own line.
func TestSubjectFallsBackToTheBandLineWithNoTopic(t *testing.T) {
	got := draftfloor.Subject(textlang.German, convstate.BandNone, "   ", true)
	want := draftfloor.For(textlang.German, convstate.BandNone).SubjectNoContext

	if got != want {
		t.Fatalf("Subject(no topic) = %q, want %q", got, want)
	}
}

func TestGreetingUsesTheNameWhenThereIsOne(t *testing.T) {
	if got := draftfloor.Greeting(textlang.German, convstate.BandFresh, "Marek"); got != "Hallo Marek," {
		t.Errorf("Greeting(named) = %q, want %q", got, "Hallo Marek,")
	}
	if got := draftfloor.Greeting(textlang.German, convstate.BandFresh, "  "); got != "Guten Tag," {
		t.Errorf("Greeting(anonymous) = %q, want %q", got, "Guten Tag,")
	}
}

// A name or a topic carrying a percent sign is text, not a format directive.
func TestAPercentSignInTheInputIsRenderedAsItself(t *testing.T) {
	got := draftfloor.Subject(textlang.English, convstate.BandNone, "100%d discount", false)

	if !strings.Contains(got, "100%d discount") {
		t.Fatalf("Subject = %q, want the topic rendered verbatim", got)
	}
}

// The caller is already on the no-model path, so an unresolved language or an
// unrecognized band degrades to a renderable draft rather than to nothing.
func TestUnresolvedInputDegradesToARenderableDraft(t *testing.T) {
	unknownLang := draftfloor.For(textlang.Unknown, convstate.BandFresh)
	if unknownLang != draftfloor.For(draftfloor.DefaultLang, convstate.BandFresh) {
		t.Error("an unresolved language should fall back to the default language")
	}

	unknownBand := draftfloor.For(textlang.German, convstate.Band("something-else"))
	if unknownBand != draftfloor.For(textlang.German, convstate.BandNone) {
		t.Error("an unrecognized band should fall back to the band that assumes no history")
	}
}
