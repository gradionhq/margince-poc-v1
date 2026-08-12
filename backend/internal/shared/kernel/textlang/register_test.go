// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package textlang_test

import (
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/textlang"
)

// Real correspondence, and the case that made this necessary: Frank Miller's
// own mail carries four du-forms and four Sie-forms, so a model asked to work
// the register out answers differently on every call — three consecutive drafts
// came back du, du, Sie.
func TestAnAmbiguousThreadAnswersUnknownRatherThanGuessing(t *testing.T) {
	mixed := "Lars, danke fürs Teilen des Decks, das habe ich durchgearbeitet. " +
		"Du kannst das gerne übersetzen lassen. Ihre Unterlagen zu HubSpot habe " +
		"ich auch gesehen; sagen Sie mir, ob Ihnen mein Feedback weiterhilft, " +
		"dann schicke ich dir noch die Notizen."

	if got := textlang.DetectRegister(mixed); got != textlang.RegisterUnknown {
		t.Fatalf("DetectRegister(evenly mixed) = %q, want Unknown: a thread using both "+
			"equally says nothing, and guessing is what produced two registers in two drafts", got)
	}
}

func TestASettledRegisterIsRead(t *testing.T) {
	cases := []struct {
		name string
		text string
		want textlang.Register
	}{
		{
			name: "on du terms",
			text: "Hallo Frank, danke für deine Notizen. Ich schicke dir den Entwurf, " +
				"sag mir gerne, was du davon hältst und ob euch das so passt.",
			want: textlang.RegisterDu,
		},
		{
			name: "on Sie terms",
			text: "Sehr geehrter Herr Miller, vielen Dank für Ihre Rückmeldung. " +
				"Ich sende Ihnen die Unterlagen und freue mich, wenn Sie mir Ihre " +
				"Einschätzung mitteilen.",
			want: textlang.RegisterSie,
		},
		{
			name: "one stray form decides nothing",
			text: "Hallo Frank, danke für deine Notizen zu dem Thema. Ihre Position " +
				"dazu ist im Deck beschrieben, ich schicke dir das gleich mit und " +
				"melde mich bei dir.",
			want: textlang.RegisterDu,
		},
		{name: "nothing to read", text: "", want: textlang.RegisterUnknown},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := textlang.DetectRegister(c.text); got != c.want {
				t.Errorf("DetectRegister = %q, want %q", got, c.want)
			}
		})
	}
}

// "sie" lowercase is she/they and says nothing about how the recipient is
// addressed. Counting it would read ordinary prose as formal.
func TestLowercaseSieIsNotFormalAddress(t *testing.T) {
	prose := "Hallo Frank, ich habe mit dem Team gesprochen und sie haben das " +
		"geprüft. Sag mir bitte, ob dir das so passt, dann schicke ich dir alles."

	if got := textlang.DetectRegister(prose); got != textlang.RegisterDu {
		t.Errorf("DetectRegister = %q, want du: lowercase \"sie\" is she/they", got)
	}
}
