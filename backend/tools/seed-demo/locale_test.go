// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package main

import "testing"

func TestLocaleFollowsTheDomain(t *testing.T) {
	for domain, want := range map[string]docLocale{
		// The Automation World Vietnam half.
		"vinamilk.com.vn": localeVI,
		"viettel.vn":      localeVI,
		"BIDV.COM.VN":     localeVI, // case must not matter
		// Korean and regional exhibitors on the same list. Their sites are
		// published in English, which is what a demo shown in Hanoi expects
		// of them too.
		"dacell.co.kr": localeEN,
		"crevis.kr":    localeEN,
		"example.sg":   localeEN,
		// The K5 half, which is German whether or not it sits on a .de.
		"trbo.com":   localeDE,
		"adesso.de":  localeDE,
		"atamya.ch":  localeDE,
		"example.at": localeDE,
	} {
		if got := localeFor(domain); got != want {
			t.Errorf("localeFor(%q) = %q, want %q", domain, got, want)
		}
	}
}

// TestEveryLocaleIsComplete is the check that matters for a demo: a missing
// label renders as an empty line on a document nobody proofreads, and a
// missing document body renders as a blank page.
func TestEveryLocaleIsComplete(t *testing.T) {
	for _, locale := range []docLocale{localeDE, localeVI, localeEN} {
		words, ok := contractVocabulary[locale]
		if !ok {
			t.Errorf("%q has no contract vocabulary", locale)
			continue
		}
		for label, value := range map[string]string{
			"Number": words.Number, "Status": words.Status,
			"Supplier": words.Supplier, "Customer": words.Customer,
			"TotalValue": words.TotalValue, "AnnualValue": words.AnnualValue,
			"Term": words.Term, "TermJoiner": words.TermJoiner,
			"Signed": words.Signed, "NoticePeriod": words.NoticePeriod,
			"NoticeDaysFmt": words.NoticeDaysFmt,
		} {
			if value == "" {
				t.Errorf("%q leaves contract label %s empty", locale, label)
			}
		}
		if len(words.DemoBanner) == 0 {
			t.Errorf("%q has no demo banner — every generated page must say it is a demo", locale)
		}

		for _, docType := range looseDocTypes {
			if titleFor(locale, docType) == docType {
				t.Errorf("%q has no title for document type %q", locale, docType)
			}
			if len(bodyFor(locale, docType)) == 0 {
				t.Errorf("%q has no body text for document type %q", locale, docType)
			}
		}
		for _, key := range []string{"contract", "contract_draft", "contract_renewal"} {
			if titleFor(locale, key) == key {
				t.Errorf("%q has no title for %q", locale, key)
			}
		}
	}
}

// TestVietnameseTextIsASCII — the PDF writer lays out WinAnsi text, so a
// document carrying Vietnamese diacritics would render as mojibake or drop
// glyphs. The copy is deliberately written unaccented; this holds it there.
func TestVietnameseTextIsASCII(t *testing.T) {
	check := func(what, s string) {
		for _, r := range s {
			if r > 127 {
				t.Errorf("%s contains %q (U+%04X) — the PDF writer is WinAnsi, so it will not render", what, r, r)
				return
			}
		}
	}
	words := contractVocabulary[localeVI]
	for label, value := range map[string]string{
		"Number": words.Number, "Supplier": words.Supplier,
		"Customer": words.Customer, "TotalValue": words.TotalValue,
		"AnnualValue": words.AnnualValue, "Term": words.Term,
		"Signed": words.Signed, "NoticePeriod": words.NoticePeriod,
	} {
		check("vi contract label "+label, value)
	}
	for _, line := range words.DemoBanner {
		check("vi demo banner", line)
	}
	for _, docType := range looseDocTypes {
		check("vi title "+docType, titleFor(localeVI, docType))
		for _, line := range bodyFor(localeVI, docType) {
			check("vi body "+docType, line)
		}
	}
}

func TestCurrencyFollowsTheLocale(t *testing.T) {
	for locale, want := range map[docLocale]string{
		localeVI: "VND", localeEN: "USD", localeDE: "EUR",
	} {
		if got := currencyFor(locale); got != want {
			t.Errorf("currencyFor(%q) = %q, want %q", locale, got, want)
		}
	}
}
