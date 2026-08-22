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

// testAccount is a CUSTOMER on purpose: that lifecycle is the one threadsFor
// gives two threads and a meeting, so it exercises the most vocabulary per
// call. A thread key no lifecycle reaches is covered by
// TestEveryLanguageAnswersEveryThreadKey instead, which reads the maps directly.
func testAccount(domain, name string) Account {
	return Account{
		OrganizationID: "01a00000-0000-7000-8000-000000000001",
		Name:           name,
		Domain:         domain,
		Lifecycle:      "customer",
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
	msgs := generate(testMailbox(), testAccount("tipa.or.kr", "중소기업기술정보진흥원"))
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
	msgs := generate(testMailbox(), testAccount("vinatechgroup.vn", "Vinatech Group"))
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
	msgs := generate(testMailbox(), testAccount("valantic.com", "valantic"))
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

// The dataset's answer beats the domain, which is the whole point: nine of its
// 21 Vietnamese companies are on a .com, fpt-is.com and vuletech.com among
// them, and a suffix rule writes German to every one of them.
func TestTheDatasetsLocaleBeatsTheDomainSuffix(t *testing.T) {
	account := testAccount("vuletech.com", "VULETECH")
	account.Locale = "vi"
	if got := localeFor(account); got != localeVI {
		t.Fatalf("localeFor(vuletech.com stated vi) = %q, want vi", got)
	}
	for _, msg := range generate(testMailbox(), account) {
		for _, german := range []string{"Viele Grüße", "Hallo ", "Kickoff", "Rechnung"} {
			if strings.Contains(msg.Subject, german) || strings.Contains(msg.Body, german) {
				t.Errorf("a .com Vietnamese company got German %q in\n  subject: %s\n  body: %s",
					german, msg.Subject, msg.Body)
			}
		}
	}
}

// An account with no stated locale falls back to the suffix. That is an
// installation seeded by something other than the demo dataset, and German is
// the right answer for the majority it will hold.
func TestAnAccountWithNoStatedLocaleFallsBackToTheSuffix(t *testing.T) {
	for domain, want := range map[string]docLocale{
		"vinatechgroup.vn": localeVI,
		"tipa.or.kr":       localeKO,
		"valantic.com":     localeDE,
	} {
		account := testAccount(domain, "Test")
		if got := localeFor(account); got != want {
			t.Errorf("localeFor(%q with no stated locale) = %q, want %q", domain, got, want)
		}
	}
}

// A locale the dataset does not use must not silently become German — it falls
// through to the suffix, which is a real answer rather than a wrong one.
func TestAnUnknownStatedLocaleFallsThroughRatherThanDefaulting(t *testing.T) {
	account := testAccount("vinatechgroup.vn", "Vinatech")
	account.Locale = "zz"
	if got := localeFor(account); got != localeVI {
		t.Errorf("an unrecognised locale on a .vn domain gave %q, want vi from the suffix", got)
	}
}

// The dataset marks EVERY Korean company `en`, because that is what their
// websites and contracts are in. Taking that literally for correspondence
// writes English to Seoul and turns 착수 회의 back into Kickoff — the exact bug
// this connector was fixed for.
//
// This test carries the dataset's REAL value (`en` for tipa.or.kr, verified in
// company-locale.json) rather than a convenient one, because the earlier
// Korean test left Locale empty and so never exercised the seeded path at all.
func TestAKoreanDomainStaysKoreanEvenThoughItsPaperIsEnglish(t *testing.T) {
	for _, domain := range []string{"tipa.or.kr", "condt.co.kr", "mv21.kr"} {
		account := testAccount(domain, "중소기업기술정보진흥원")
		account.Locale = "en" // what company-locale.json actually says
		if got := localeFor(account); got != localeKO {
			t.Errorf("localeFor(%q stated en) = %q, want ko — the dataset's `en` is about PAPER", domain, got)
		}
		for _, msg := range generate(testMailbox(), account) {
			if strings.Contains(msg.Subject, "Kickoff") || strings.Contains(msg.Body, "Best regards") {
				t.Errorf("%s got English correspondence: %s / %s", domain, msg.Subject, msg.Body)
			}
		}
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
	keys := []string{
		threadKickoff, threadInvoice, threadOffboarding,
		threadOffer, threadIntro, threadInbound, threadCold,
	}
	// localeEN is in the list on purpose. It had vocabulary and NO bodies, so
	// bodiesFor(localeEN, ...) silently returned German — a language that
	// claimed to exist and did not, which is the same class of bug as the
	// German-to-Seoul one this file is about.
	for _, locale := range []docLocale{localeDE, localeVI, localeKO, localeEN} {
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
			// The reply matters as much as the opener: threadsFor asks for one
			// or two on every thread except `cold`, and an empty one silently
			// truncates the conversation to a single unanswered message.
			if key != threadCold && strings.TrimSpace(body[1]) == "" {
				t.Errorf("%q/%q has no reply — the thread would end unanswered", locale, key)
			}
		}
	}
}

// Every language must fill every SUBJECT field too. A blank one renders as a
// bare account name, or as ": " in front of a meeting.
func TestEveryLanguageNamesEverySubject(t *testing.T) {
	for _, locale := range []docLocale{localeDE, localeVI, localeKO, localeEN} {
		w := wordsFor(locale)
		for name, value := range map[string]string{
			"Kickoff": w.Kickoff, "Invoice": w.Invoice, "Offer": w.Offer,
			"Offboarding": w.Offboarding, "Intro": w.Intro, "Enquiry": w.Enquiry,
			"Cold": w.Cold, "Meeting": w.Meeting, "MeetingBody": w.MeetingBody,
		} {
			if strings.TrimSpace(value) == "" {
				t.Errorf("%q has no %s subject", locale, name)
			}
		}
	}
}

// An addressee with no name must not produce " 님께," or "Hallo ,".
func TestANamelessAddresseeGetsNoSalutationRatherThanABrokenOne(t *testing.T) {
	account := testAccount("tipa.or.kr", "중소기업기술정보진흥원")
	account.People = []Person{{Name: "", Email: "nobody@example.com"}}
	for _, msg := range generate(testMailbox(), account) {
		if strings.HasPrefix(msg.Body, " ") || strings.Contains(msg.Body, "Hallo ,") {
			t.Errorf("a nameless addressee produced a broken salutation: %q", msg.Body)
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

// The auth bundle must still accept the bare seat id. scripts/seed-dev.sql
// writes one, and so did every seeder before the locale map — treating that as
// a parse error would leave a dev database with a mailbox that never syncs.
func TestReadAuthAcceptsBothTheJSONBundleAndTheBareSeatID(t *testing.T) {
	const seat = "01a00000-0000-7000-8000-00000000000a"

	bare := readAuth([]byte(seat))
	if bare.UserID != seat {
		t.Errorf("a bare seat id parsed to %q, want %q", bare.UserID, seat)
	}
	if len(bare.Locales) != 0 {
		t.Errorf("a bare seat id carried locales: %v", bare.Locales)
	}

	bundle := readAuth([]byte(`{"user_id":"` + seat + `","locales":{"vuletech.com":"vi"}}`))
	if bundle.UserID != seat {
		t.Errorf("the bundle's seat id parsed to %q, want %q", bundle.UserID, seat)
	}
	if bundle.Locales["vuletech.com"] != "vi" {
		t.Errorf("the bundle lost its locales: %v", bundle.Locales)
	}

	if empty := readAuth(nil); empty.UserID != "" {
		t.Errorf("an empty credential produced a seat id %q", empty.UserID)
	}
}
