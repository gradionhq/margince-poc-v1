// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package offlinedemo

// The generated correspondence must be in the account's own language.
//
// It was German for everybody. The demo's Korean government agency received
// "Kickoff 중소기업기술정보진흥원" — a German subject word concatenated onto a Korean
// company name — with a German body and "Viele Grüße" underneath. 44 of the 235
// generated activities were German mail to Vietnamese and Korean accounts.

import (
	"strings"
	"testing"
	"time"
)

func testMailbox() Mailbox {
	return Mailbox{
		Email: "lena.fischer@demo.test", DisplayName: "Lena Fischer",
	}
}

func testAccount(domain, name, lifecycle string) Account {
	return Account{
		OrganizationID: "01a00000-0000-7000-8000-000000000001",
		Name:           name,
		Domain:         domain,
		Lifecycle:      lifecycle,
		ContractNumber: "GR-2026-0402",
		People:         []Person{{Name: "Seo Min-ji", Email: "minji.seo@example.com"}},
		Now:            time.Date(2026, 8, 22, 9, 0, 0, 0, time.UTC),
	}
}

// TestAKoreanAccountIsNotWrittenToInGerman is the regression this file exists
// for. It asserts the ABSENCE of the German phrases rather than the presence of
// Korean ones: the failure mode was German leaking through, and a test that
// only looked for Korean would pass on a message that carried both.
func TestAKoreanAccountIsNotWrittenToInGerman(t *testing.T) {
	msgs := generate(testMailbox(), testAccount("tipa.or.kr", "중소기업기술정보진흥원", "customer"))
	if len(msgs) == 0 {
		t.Fatal("a customer account generated no correspondence at all")
	}
	for _, msg := range msgs {
		for _, german := range []string{"Viele Grüße", "Hallo ", "Kickoff", "Rechnung", "Termin:"} {
			if strings.Contains(msg.Subject, german) || strings.Contains(msg.Body, german) {
				t.Errorf("a Korean account got German text %q in\n  subject: %s\n  body: %s",
					german, msg.Subject, msg.Body)
			}
		}
	}
}

func TestAVietnameseAccountIsNotWrittenToInGerman(t *testing.T) {
	msgs := generate(testMailbox(), testAccount("vinatechgroup.vn", "Vinatech Group", "customer"))
	if len(msgs) == 0 {
		t.Fatal("a customer account generated no correspondence at all")
	}
	for _, msg := range msgs {
		for _, german := range []string{"Viele Grüße", "Hallo ", "Kickoff", "Rechnung", "Termin:"} {
			if strings.Contains(msg.Subject, german) || strings.Contains(msg.Body, german) {
				t.Errorf("a Vietnamese account got German text %q in\n  subject: %s\n  body: %s",
					german, msg.Subject, msg.Body)
			}
		}
	}
}

// A German account must still read as German. The fix would be worthless if it
// translated the majority of the dataset into something else.
func TestAGermanAccountStillReadsAsGerman(t *testing.T) {
	msgs := generate(testMailbox(), testAccount("valantic.com", "valantic", "customer"))
	if len(msgs) == 0 {
		t.Fatal("a customer account generated no correspondence at all")
	}
	var sawSignOff bool
	for _, msg := range msgs {
		if strings.Contains(msg.Body, "Viele Grüße") {
			sawSignOff = true
		}
	}
	if !sawSignOff {
		t.Error("no German sign-off anywhere on a German account")
	}
}

func TestLocaleOfReadsTheDomainSuffix(t *testing.T) {
	for domain, want := range map[string]docLocale{
		"tipa.or.kr":       localeKO,
		"mv21.kr":          localeKO,
		"micube.co.kr":     localeKO,
		"vinatechgroup.vn": localeVI,
		"i-soft.com.vn":    localeVI,
		"valantic.com":     localeDE,
		"akeneo.com":       localeDE,
		"":                 localeDE,
	} {
		if got := localeOf(domain); got != want {
			t.Errorf("localeOf(%q) = %q, want %q", domain, got, want)
		}
	}
}

// Every language must answer every thread key the generator can ask for. A
// missing entry falls back to German, which is exactly the bug this file is
// about — so the fallback must never be reached in practice.
func TestEveryLanguageAnswersEveryThreadKey(t *testing.T) {
	keys := []string{"kickoff", "invoice", "offboarding", "offer", "intro", "inbound", "cold"}
	for _, locale := range []docLocale{localeDE, localeVI, localeKO} {
		byKey, ok := threadBodies[locale]
		if !ok {
			t.Errorf("no thread bodies at all for %q", locale)
			continue
		}
		for _, key := range keys {
			body, ok := byKey[key]
			if !ok {
				t.Errorf("%q has no body for the %q thread — it would fall back to German", locale, key)
				continue
			}
			if strings.TrimSpace(body[0]) == "" {
				t.Errorf("%q/%q has an empty opener", locale, key)
			}
		}
	}
}

// wordsFor must never return a zero struct: Hello is a func, and a nil one
// panics inside newMessage rather than rendering badly.
func TestWordsForAlwaysReturnsAUsableGreeting(t *testing.T) {
	for _, locale := range []docLocale{localeDE, localeVI, localeKO, localeEN, docLocale("xx")} {
		words := wordsFor(locale)
		if words.Hello == nil {
			t.Fatalf("wordsFor(%q) returned a nil Hello", locale)
		}
		if got := words.Greeting("Test"); !strings.Contains(got, "Test") {
			t.Errorf("wordsFor(%q).Greeting does not name the addressee: %q", locale, got)
		}
		if strings.TrimSpace(words.SignOff) == "" {
			t.Errorf("wordsFor(%q) has no sign-off", locale)
		}
	}
}
