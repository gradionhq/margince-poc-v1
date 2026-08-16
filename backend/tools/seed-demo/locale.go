// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package main

// What language a company's paper is written in.
//
// The dataset has two halves from two exhibitor lists: K5 Berlin, which is
// German and DACH, and Automation World Vietnam, which is Vietnamese, Korean
// and regional. Every generated document was German regardless, so a
// Vietnamese customer held a Rahmenvertrag headed "Auftragnehmer" — which is
// not a translation problem so much as a demo that cannot be shown in Hanoi.
//
// The language is DERIVED from the domain rather than configured, for the
// same reason ownership and lifecycle are: a company crawled next month gets
// the right one without an edit. A `.vn` domain is Vietnamese, everything
// else in this dataset is German. English is here as the fallback for a
// company that is neither, so the choice is always a real answer rather than
// a silent default to German.

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// docLocale is the language one company's documents are written in.
type docLocale string

const (
	localeDE docLocale = "de"
	localeVI docLocale = "vi"
	localeEN docLocale = "en"
)

// companyLocales is what the dataset SAYS each company's language is, read
// from datasets/v1/company-locale.json. Empty until loadCompanyLocales runs;
// a domain absent from it is German, which is the K5 default.
var companyLocales = map[string]docLocale{}

// loadCompanyLocales reads the dataset's own answer for each company.
//
// The domain suffix is not enough and guessing from it is wrong for a fifth
// of the Automation World list: Vu Le Technology is Vietnamese and DACELL is
// Korean, and both sit on .com. The dataset builds company-locale.json from
// the exhibitor list, which is the authoritative statement of which companies
// these are.
//
// A missing file is not an error. The dataset may predate it, and every
// company then falls back to German — the same answer this code gave before
// the file existed.
func loadCompanyLocales(root string) error {
	path := filepath.Join(root, "datasets", "v1", "company-locale.json")
	raw, err := os.ReadFile(path) //nolint:gosec // G304: the dataset root is a deliberate operator-supplied flag
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("reading %s: %w", path, err)
	}
	var file struct {
		Locales map[string]string `json:"locales"`
	}
	if err := json.Unmarshal(raw, &file); err != nil {
		return fmt.Errorf("parsing %s: %w", path, err)
	}
	for domain, locale := range file.Locales {
		switch docLocale(locale) {
		case localeDE, localeVI, localeEN:
			companyLocales[strings.ToLower(domain)] = docLocale(locale)
		default:
			return fmt.Errorf("%s gives %s the language %q, which has no vocabulary",
				path, domain, locale)
		}
	}
	return nil
}

// localeFor decides a company's document language.
//
// The dataset's own answer wins. Only a company it does not name falls back to
// the domain suffix, which is right for the K5 half — every one of those is a
// German-speaking company, whatever TLD it sits on.
func localeFor(domain string) docLocale {
	domain = strings.ToLower(strings.TrimSpace(domain))
	if locale, ok := companyLocales[domain]; ok {
		return locale
	}
	if strings.HasSuffix(domain, ".vn") || strings.HasSuffix(domain, ".com.vn") {
		return localeVI
	}
	return localeDE
}

// contractWords is every label a contract page prints, per language.
//
// A struct rather than a map keyed by string: a missing label is then a
// compile error instead of an empty line on a document nobody proofreads.
type contractWords struct {
	Number        string
	Status        string
	Supplier      string
	Customer      string
	TotalValue    string
	AnnualValue   string
	Term          string
	TermJoiner    string
	Signed        string
	NoticePeriod  string
	NoticeDaysFmt string
	DemoBanner    []string
}

var contractVocabulary = map[docLocale]contractWords{
	localeDE: {
		Number: "Vertragsnummer", Status: "Status",
		Supplier: "Auftragnehmer", Customer: "Auftraggeber",
		TotalValue: "Gesamtwert", AnnualValue: "Jahreswert",
		Term: "Laufzeit", TermJoiner: "bis", Signed: "Unterzeichnet",
		NoticePeriod: "Kuendigungsfrist", NoticeDaysFmt: "%s: %d Tage",
		DemoBanner: []string{
			"DEMO-DOKUMENT. Erzeugt fuer Test- und Vorfuehrzwecke.",
			"Keine rechtliche Wirkung, keine Unterschrift, kein Angebot.",
		},
	},
	localeVI: {
		Number: "So hop dong", Status: "Trang thai",
		Supplier: "Ben cung cap", Customer: "Ben mua",
		TotalValue: "Tong gia tri", AnnualValue: "Gia tri hang nam",
		Term: "Thoi han", TermJoiner: "den", Signed: "Ngay ky",
		NoticePeriod: "Thoi han bao truoc", NoticeDaysFmt: "%s: %d ngay",
		DemoBanner: []string{
			"TAI LIEU DEMO. Tao ra cho muc dich thu nghiem va trinh dien.",
			"Khong co gia tri phap ly, khong co chu ky, khong phai chao gia.",
		},
	},
	localeEN: {
		Number: "Contract number", Status: "Status",
		Supplier: "Supplier", Customer: "Customer",
		TotalValue: "Total value", AnnualValue: "Annual value",
		Term: "Term", TermJoiner: "to", Signed: "Signed",
		NoticePeriod: "Notice period", NoticeDaysFmt: "%s: %d days",
		DemoBanner: []string{
			"DEMO DOCUMENT. Generated for testing and demonstration.",
			"No legal effect, no signature, not an offer.",
		},
	},
}

// wordsFor is the vocabulary for a language, falling back to German rather
// than to an empty struct — a document with blank labels would look like a
// rendering bug rather than a missing translation.
func wordsFor(locale docLocale) contractWords {
	if words, ok := contractVocabulary[locale]; ok {
		return words
	}
	return contractVocabulary[localeDE]
}

// documentTitles is what each kind of paper is called, per language.
var documentTitles = map[docLocale]map[string]string{
	localeDE: {
		"contract": "Rahmenvertrag", "contract_draft": "Rahmenvertrag (Entwurf)",
		"contract_renewal": "Rahmenvertrag, Verlaengerung",
		"nda":              "Geheimhaltungsvereinbarung", "price_list": "Preisliste",
		"dpa": "Auftragsverarbeitungsvertrag", "order_form": "Bestellformular",
	},
	localeVI: {
		"contract": "Hop dong khung", "contract_draft": "Hop dong khung (Ban thao)",
		"contract_renewal": "Hop dong khung, Gia han",
		"nda":              "Thoa thuan bao mat", "price_list": "Bang gia",
		"dpa": "Thoa thuan xu ly du lieu", "order_form": "Phieu dat hang",
	},
	localeEN: {
		"contract": "Master agreement", "contract_draft": "Master agreement (draft)",
		"contract_renewal": "Master agreement, renewal",
		"nda":              "Non-disclosure agreement", "price_list": "Price list",
		"dpa": "Data processing agreement", "order_form": "Order form",
	},
}

// titleFor names a document in the company's language, falling back through
// German to the key itself so a new document type is visible rather than blank.
func titleFor(locale docLocale, key string) string {
	if byKey, ok := documentTitles[locale]; ok {
		if title, ok := byKey[key]; ok {
			return title
		}
	}
	if title, ok := documentTitles[localeDE][key]; ok {
		return title
	}
	return key
}

// looseDocumentBodies is the text inside an account document, per language.
var looseDocumentBodies = map[docLocale]map[string][]string{
	localeDE: {
		"nda": {
			"Gegenseitige Geheimhaltungsvereinbarung (NDA)",
			"Laufzeit: 3 Jahre ab Unterzeichnung",
			"Gegenstand: Austausch technischer und kaufmaennischer Informationen",
		},
		"price_list": {
			"Preisliste, gueltig fuer das laufende Geschaeftsjahr",
			"Alle Preise netto zzgl. gesetzlicher Umsatzsteuer",
			"Staffelrabatte ab 50 Lizenzen auf Anfrage",
		},
		"dpa": {
			"Auftragsverarbeitungsvertrag nach Art. 28 DSGVO",
			"Technische und organisatorische Massnahmen als Anlage 1",
			"Unterauftragsverarbeiter als Anlage 2",
		},
		"order_form": {
			"Bestellformular fuer zusaetzliche Lizenzen",
			"Abrechnung anteilig bis zum Ende der laufenden Periode",
		},
	},
	localeVI: {
		"nda": {
			"Thoa thuan bao mat thong tin song phuong (NDA)",
			"Thoi han: 3 nam ke tu ngay ky",
			"Pham vi: Trao doi thong tin ky thuat va thuong mai",
		},
		"price_list": {
			"Bang gia ap dung cho nam tai chinh hien hanh",
			"Gia chua bao gom thue gia tri gia tang",
			"Chiet khau theo so luong tu 50 giay phep tro len",
		},
		"dpa": {
			"Thoa thuan xu ly du lieu ca nhan",
			"Bien phap ky thuat va to chuc tai Phu luc 1",
			"Danh sach ben xu ly phu tai Phu luc 2",
		},
		"order_form": {
			"Phieu dat hang cho giay phep bo sung",
			"Tinh phi theo ty le den het ky hien tai",
		},
	},
	localeEN: {
		"nda": {
			"Mutual non-disclosure agreement (NDA)",
			"Term: 3 years from signature",
			"Scope: exchange of technical and commercial information",
		},
		"price_list": {
			"Price list, valid for the current financial year",
			"All prices net, excluding VAT",
			"Volume discounts from 50 licences on request",
		},
		"dpa": {
			"Data processing agreement",
			"Technical and organisational measures in Annex 1",
			"Sub-processors in Annex 2",
		},
		"order_form": {
			"Order form for additional licences",
			"Billed pro rata to the end of the current period",
		},
	},
}

// bodyFor is an account document's text, falling back to German.
func bodyFor(locale docLocale, key string) []string {
	if byKey, ok := looseDocumentBodies[locale]; ok {
		if body, ok := byKey[key]; ok {
			return body
		}
	}
	return looseDocumentBodies[localeDE][key]
}

// dealNameFor is what a generated deal is called, in the account's language.
func dealNameFor(locale docLocale, displayName, stage string) string {
	var suffix string
	switch locale {
	case localeVI:
		switch stage {
		case "won":
			suffix = "Hop dong dau tien"
		case "lost":
			suffix = "Danh gia"
		default:
			suffix = "Trien khai"
		}
	case localeEN:
		switch stage {
		case "won":
			suffix = "First contract"
		case "lost":
			suffix = "Evaluation"
		default:
			suffix = "Rollout"
		}
	default:
		switch stage {
		case "won":
			suffix = "Erstvertrag"
		case "lost":
			suffix = "Evaluierung"
		default:
			suffix = "Einführung"
		}
	}
	return displayName + " — " + suffix
}

// currencyFor is what a company is billed in.
//
// The finance mirror generates its ledger in the contract's currency, so this
// is what makes a Vietnamese customer's invoices arrive in dong rather than
// euro. VND has no minor unit in practice, but the product stores minor units
// everywhere, so the amounts stay in the same integer shape.
func currencyFor(locale docLocale) string {
	switch locale {
	case localeVI:
		return "VND"
	case localeEN:
		return "USD"
	default:
		return "EUR"
	}
}
