// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package main

// Splitting a printed address into the parts the organization API takes.
//
// The crawl stores what the Impressum printed — one line, as a human reads it
// — and the contract wants line1, city, postal_code and country separately.
// Nothing in between does the splitting, so a dataset that HAS 54 addresses
// filed none of them.
//
// This parses the shapes the dataset actually holds rather than addresses in
// general. Every case below was taken from a real accepted.json, and a value
// this cannot read is filed whole in line1 rather than guessed at: a company
// whose street is right and whose city is empty is honest, and one whose city
// was invented is not.

import (
	"fmt"
	"regexp"
	"strings"
)

// address is the structured form the organization API takes.
type address struct {
	Line1      string
	City       string
	PostalCode string
	Country    string // ISO-3166 alpha-2
}

// empty reports an address with nothing worth writing.
func (a address) empty() bool {
	return a.Line1 == "" && a.City == "" && a.PostalCode == "" && a.Country == ""
}

// dachPostcode finds the postcode-and-city pair that ends a German, Austrian
// or Swiss address, with the country prefix those countries print in front of
// the postcode ("D-50668", "A-4030", "CH-6300", "D – 80801").
//
// The city runs to the end of the value or to the country word, and may carry
// the spaces and hyphens a real city name has: "Bad Friedrichshall",
// "Frankfurt/Main", "Mühldorf am Inn".
var dachPostcode = regexp.MustCompile(
	`(?:\b([ADCH]{1,2})\s*[-–]\s*)?\b(\d{4,5})\s+([\p{Lu}][\p{L}\p{M}]*(?:[ /-][\p{L}\p{M}.]+)*)`)

// countryWords maps the country a site prints, in the languages this dataset
// crawls, onto the ISO code the contract wants. The country is stated by the
// page or it is absent — never inferred from a postcode, because a postcode
// range is not a country and guessing one would be exactly the invention the
// crawl's own gates refuse.
var countryWords = map[string]string{
	"deutschland": "DE", "germany": "DE",
	"österreich": "AT", "oesterreich": "AT", "austria": "AT",
	"schweiz": "CH", "switzerland": "CH", "suisse": "CH",
	"usa": "US", "united states": "US",
	"united kingdom": "GB", "england": "GB",
	"nederland": "NL", "netherlands": "NL",
	"españa": "ES", "espana": "ES", "spain": "ES",
	"france": "FR", "italia": "IT", "italy": "IT",
	"polska": "PL", "poland": "PL",
	"việt nam": "VN", "viet nam": "VN", "vietnam": "VN",
}

// countryPrefixes maps the letter a DACH address prints before its postcode.
var countryPrefixes = map[string]string{"D": "DE", "A": "AT", "CH": "CH"}

// parseAddress splits one printed address.
//
// It never returns an error. An address it cannot read keeps its whole printed
// form in Line1, which is the honest answer: the company's address is on
// record and the parts are simply not separated, rather than separated wrongly.
func parseAddress(printed string) address {
	printed = cleanPrintedAddress(printed)
	if printed == "" {
		return address{}
	}
	out := address{Line1: printed}
	rest, country := splitTrailingCountry(printed)
	out.Country = country

	match := dachPostcode.FindStringSubmatchIndex(rest)
	if match == nil {
		out.Line1 = strings.TrimSpace(strings.TrimRight(rest, ","))
		return out
	}
	groups := dachPostcode.FindStringSubmatch(rest)
	if out.Country == "" {
		out.Country = countryPrefixes[strings.ToUpper(groups[1])]
	}
	out.PostalCode = groups[2]
	out.City = strings.TrimSpace(strings.Trim(groups[3], ",."))
	// Everything before the postcode is the street and whatever the notice
	// printed with it — a floor, a building name, a care-of line.
	out.Line1 = strings.TrimSpace(strings.TrimRight(rest[:match[0]], " ,"))
	if out.Line1 == "" {
		out.Line1 = printed
	}
	return out
}

// cleanPrintedAddress removes the characters a rendered page leaves in text
// and no field should carry: the zero-width joiners and word-joiners a CMS
// emits mid-line, and repeated whitespace.
//
// This is real rather than defensive: one company's address arrives as
// "Viktoriastraße 3b⁠ 86150 Augsburg", and the word joiner sits exactly
// where the postcode match has to begin.
func cleanPrintedAddress(printed string) string {
	printed = strings.Map(func(r rune) rune {
		switch r {
		case '\u200b', '\u200c', '\u200d', '\u2060', '\ufeff':
			return -1 // zero-width and word joiners: rendering hints, not content
		case '\u00a0', '\u2007', '\u202f':
			return ' ' // the non-breaking spaces a CMS emits between number and unit
		}
		return r
	}, printed)
	return strings.Join(strings.Fields(printed), " ")
}

// splitTrailingCountry lifts the country off the end of the value and returns
// what remains. Only a country the page NAMED is taken; the rest of the value
// is untouched.
func splitTrailingCountry(printed string) (rest, country string) {
	trimmed := strings.TrimRight(printed, " ,.")
	lower := strings.ToLower(trimmed)
	for word, code := range countryWords {
		if !strings.HasSuffix(lower, word) {
			continue
		}
		cut := len(trimmed) - len(word)
		// A suffix that is part of a longer word is not the country: only a
		// boundary before it makes "…, Austria" different from "…Faustria".
		if cut > 0 && !isAddressBoundary(rune(trimmed[cut-1])) {
			continue
		}
		return strings.TrimSpace(strings.TrimRight(trimmed[:cut], " ,")), code
	}
	return trimmed, ""
}

func isAddressBoundary(r rune) bool {
	return r == ' ' || r == ',' || r == '.'
}

// addressBody is the address as the contract takes it, or nil when the read
// found none. Only the parts that were actually printed are sent: an empty
// city is left out rather than written as an empty string, so a partial
// address reads as partial instead of as a city nobody named.
func addressBody(a address) jsonBody {
	if a.empty() {
		return nil
	}
	out := jsonBody{}
	addIfSet(out, "line1", a.Line1)
	addIfSet(out, "city", a.City)
	addIfSet(out, "postal_code", a.PostalCode)
	addIfSet(out, "country", a.Country)
	return out
}

// organizationBody is the company as the create contract takes it.
//
// Everything here comes from the reviewed site read, so improving the read
// improves the record rather than leaving two descriptions to keep in step.
func organizationBody(comp company) jsonBody {
	body := jsonBody{
		"display_name": comp.displayName(),
		"source":       seedSource,
		"domains":      []jsonBody{{"domain": comp.Domain, "is_primary": true}},
	}
	addIfSet(body, "legal_name", comp.value("legal_name"))
	addIfSet(body, "industry", comp.value("industry"))
	if description := comp.value("offer_summary"); description != "" {
		body["description"] = truncate(description, 500)
	}
	// The address the company's own legal notice printed. The crawl stores it
	// as one line, the way a person reads it, and the contract takes it in
	// parts — so it is split here rather than filed as prose in a field meant
	// for a street.
	if addr := addressBody(parseAddress(comp.value("registered_address"))); addr != nil {
		body["address"] = addr
	}
	return body
}

// fillOrganizationAddress puts an address on a company already on file.
//
// Only ever FILLS: a record that already carries a street is left alone. The
// dataset is one source among several a real installation has, and overwriting
// an address somebody corrected by hand would make re-seeding destructive
// rather than convergent.
func fillOrganizationAddress(c *client, orgID string, comp company) error {
	parsed := parseAddress(comp.value("registered_address"))
	body := addressBody(parsed)
	if body == nil {
		return nil
	}
	var current struct {
		Address *struct {
			Line1 string `json:"line1"`
		} `json:"address"`
		Version int `json:"version"`
	}
	if err := c.get("/v1/organizations/"+orgID, nil, &current); err != nil {
		return fmt.Errorf("reading %s back: %w", comp.Domain, err)
	}
	if current.Address != nil && strings.TrimSpace(current.Address.Line1) != "" {
		return nil
	}
	if err := c.patch("/v1/organizations/"+orgID, jsonBody{
		"address": body, "if_version": current.Version,
	}, nil); err != nil {
		return fmt.Errorf("setting the address on %s: %w", comp.Domain, err)
	}
	return nil
}
