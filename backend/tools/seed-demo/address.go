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
	"net/url"
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
	rest, country := splitTrailingCountry(printed)
	// A value that is nothing BUT a country word describes no address. Sending
	// the country alone would file a company in Germany with no street, city
	// or postcode, which reads as an address on every screen that shows one.
	if strings.TrimSpace(rest) == "" {
		return address{}
	}
	out := address{Line1: printed, Country: country}

	// The LAST match, not the first. A postcode ends a DACH address, and an
	// earlier digit run is something else that came before the street — a
	// suite, a building number, a PO box. Taking the first match read
	// "Suite 1200 Hauptstrasse 5 80331 München" as postcode 1200 in the city
	// of Hauptstrasse.
	all := dachPostcode.FindAllStringSubmatchIndex(rest, -1)
	if all == nil {
		out.Line1 = strings.TrimSpace(strings.TrimRight(rest, ","))
		return out
	}
	match := all[len(all)-1]
	groups := make([]string, 0, len(match)/2)
	for i := 0; i < len(match); i += 2 {
		if match[i] < 0 {
			groups = append(groups, "")
			continue
		}
		groups = append(groups, rest[match[i]:match[i+1]])
	}
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
		case '\u200b', '\u2060', '\ufeff':
			// Zero-width space, word joiner, BOM: rendering hints a CMS emits
			// mid-line, and one company's page puts a word joiner exactly
			// where its postcode begins.
			//
			// U+200C and U+200D are deliberately NOT here. They look like the
			// same class of invisible character and are not: in Persian and
			// the Indic scripts they control joining and are part of the
			// spelling, so dropping them would corrupt an address rather than
			// clean it.
			return -1
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
// Only ever FILLS, and only a record with NO address at all. The dataset is one
// source among several a real installation has, so re-seeding must not be
// destructive: a record carrying any address part — a city somebody typed, a
// line2 the crawl never sees — is left exactly as it is. Checking line1 alone
// would replace all six columns and quietly drop the rest.
func fillOrganizationAddress(c *client, orgID string, comp company) error {
	body := addressBody(parseAddress(comp.value("registered_address")))
	if body == nil {
		return nil
	}
	var current struct {
		Address *struct {
			Line1      string `json:"line1"`
			Line2      string `json:"line2"`
			City       string `json:"city"`
			Region     string `json:"region"`
			PostalCode string `json:"postal_code"`
			Country    string `json:"country"`
		} `json:"address"`
		Version int `json:"version"`
	}
	if err := c.get("/v1/organizations/"+orgID, nil, &current); err != nil {
		return fmt.Errorf("reading %s back: %w", comp.Domain, err)
	}
	if current.Address != nil {
		for _, part := range []string{
			current.Address.Line1, current.Address.Line2, current.Address.City,
			current.Address.Region, current.Address.PostalCode, current.Address.Country,
		} {
			if strings.TrimSpace(part) != "" {
				return nil
			}
		}
	}
	// The version guard goes in the If-Match header: the body's `if_version`
	// is accepted and ignored, so a write spelled that way is last-write-wins
	// and would overwrite an address added between the read above and here.
	if err := c.patchGuarded("/v1/organizations/"+orgID, current.Version, jsonBody{"address": body}, nil); err != nil {
		return fmt.Errorf("setting the address on %s: %w", comp.Domain, err)
	}
	return nil
}

func findOrganization(c *client, comp company) (id string, found bool, err error) {
	var page struct {
		Data []struct {
			ID          string `json:"id"`
			DisplayName string `json:"display_name"`
		} `json:"data"`
	}
	query := url.Values{"q": {comp.displayName()}, "limit": {"25"}}
	if err := c.get("/v1/organizations", query, &page); err != nil {
		return "", false, fmt.Errorf("searching for %s: %w", comp.Domain, err)
	}
	for _, row := range page.Data {
		if strings.EqualFold(row.DisplayName, comp.displayName()) {
			return row.ID, true, nil
		}
	}
	return "", false, nil
}
