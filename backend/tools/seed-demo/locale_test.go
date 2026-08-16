// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package main

import "testing"

// TestTheDatasetDecidesTheLanguage — the domain suffix is not enough, and
// guessing from it is wrong for a fifth of the Automation World list. Vu Le
// Technology is Vietnamese and DACELL is Korean, and both sit on .com.
func TestTheDatasetDecidesTheLanguage(t *testing.T) {
	restore := companyLocales
	t.Cleanup(func() { companyLocales = restore })
	companyLocales = map[string]docLocale{
		"vuletech.com": localeVI, // Vietnamese, on a .com
		"soragroup.vn": localeVI,
		"dacell.com":   localeEN, // Korean, on a .com
		"condt.co.kr":  localeEN,
		"beckhoff.com": localeEN, // the German parent of a Vietnam exhibitor
	}
	for domain, want := range map[string]docLocale{
		"vuletech.com": localeVI,
		"VULETECH.COM": localeVI, // case must not matter
		"soragroup.vn": localeVI,
		"dacell.com":   localeEN,
		"condt.co.kr":  localeEN,
		// Not named by the dataset: the K5 half, German whatever its TLD.
		"trbo.com":  localeDE,
		"adesso.de": localeDE,
		// A .vn nobody listed is still plainly Vietnamese.
		"someone.com.vn": localeVI,
	} {
		if got := localeFor(domain); got != want {
			t.Errorf("localeFor(%q) = %q, want %q", domain, got, want)
		}
	}
}

// TestAnUnknownCompanyIsGerman — the fallback has to be a real answer, not an
// empty string that renders a document with no labels.
func TestAnUnknownCompanyIsGerman(t *testing.T) {
	restore := companyLocales
	t.Cleanup(func() { companyLocales = restore })
	companyLocales = map[string]docLocale{}
	if got := localeFor("never-heard-of-it.example"); got != localeDE {
		t.Errorf("an unlisted company got %q, want German", got)
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

// TestStatusValuesAreTranslated — a page reading "Trang thai: active" is
// half-translated, which reads as a bug rather than a language choice.
func TestStatusValuesAreTranslated(t *testing.T) {
	for _, status := range []string{"draft", "active", "expired", "cancelled", "superseded"} {
		for _, locale := range []docLocale{localeDE, localeVI, localeEN} {
			got := statusWord(locale, status)
			if got == "" {
				t.Errorf("%q has no word for status %q", locale, status)
			}
			if locale != localeEN && got == status {
				t.Errorf("%q leaves status %q untranslated", locale, status)
			}
		}
	}
	// A status the product adds later must show up as itself rather than
	// disappear from the page.
	if got := statusWord(localeVI, "renegotiating"); got != "renegotiating" {
		t.Errorf("an unknown status rendered %q, want the raw value", got)
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

// TestEveryLanguageHasACustomer — the coverage matrix guarantees counts, never
// WHICH company holds them, and there are two Vietnamese companies among 171.
// The hash reliably made both a prospect and a target, so the Vietnamese
// contracts, documents and dong invoices existed in code and in tests and
// nowhere a demo could reach. That is the same as not having built them.
func TestEveryLanguageHasACustomer(t *testing.T) {
	restore := companyLocales
	t.Cleanup(func() { companyLocales = restore })
	companyLocales = map[string]docLocale{
		"vuletech.com": localeVI,
		"soragroup.vn": localeVI,
		"micube.co.kr": localeEN,
		"dacell.com":   localeEN,
	}
	domains := append(synthDomains(160),
		"vuletech.com", "soragroup.vn", "micube.co.kr", "dacell.com")
	profiles := planProfiles(domains, demoConfig{})

	for _, locale := range []docLocale{localeVI, localeEN} {
		found := ""
		for domain, p := range profiles {
			if localeFor(domain) == locale && p.Lifecycle == "customer" {
				found = domain
				break
			}
		}
		if found == "" {
			t.Errorf("no %q company is a customer — that language's paper exists nowhere a demo can reach", locale)
			continue
		}
		if len(profiles[found].Contracts) == 0 {
			t.Errorf("%s is the %q customer but holds no contract, so there is no document to show", found, locale)
		}
	}
}
