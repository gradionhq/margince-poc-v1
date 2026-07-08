// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package values

import (
	"database/sql/driver"
	"fmt"
	"regexp"
	"strings"
)

// e164 is the wire form the schema documents for person_phone: "+",
// a non-zero country digit, 8–15 digits total.
var e164 = regexp.MustCompile(`^\+[1-9][0-9]{7,14}$`)

// Phone is an E.164-normalized number. Separators are formatting, so
// parsing strips them; a leading 00 is the international-dialing
// spelling of +. A number WITHOUT a country prefix is rejected rather
// than guessed — region inference needs carrier metadata this
// dependency-free kernel deliberately does not carry.
type Phone struct{ s string }

var phoneSeparators = strings.NewReplacer(" ", "", "(", "", ")", "", ".", "", "-", "", "/", "", "\t", "")

func ParsePhone(raw string) (Phone, error) {
	cleaned := phoneSeparators.Replace(strings.TrimSpace(raw))
	if cleaned == "" {
		return Phone{}, &ParseError{Field: "phone", Code: "phone_empty", Message: "a phone number is required"}
	}
	if strings.HasPrefix(cleaned, "00") {
		cleaned = "+" + cleaned[2:]
	}
	if !strings.HasPrefix(cleaned, "+") {
		return Phone{}, &ParseError{Field: "phone", Code: "phone_needs_country_code",
			Message: "the number needs its country prefix (+49…, 0049…)"}
	}
	if !e164.MatchString(cleaned) {
		return Phone{}, &ParseError{Field: "phone", Code: "phone_malformed",
			Message: "not an E.164 number (+ and 8–15 digits)"}
	}
	return Phone{s: cleaned}, nil
}

func (p Phone) String() string { return p.s }
func (p Phone) IsZero() bool   { return p.s == "" }

func (p Phone) Value() (driver.Value, error) { return p.s, nil }

//craft:ignore naked-any sql.Scanner mandates the any source parameter
func (p *Phone) Scan(src any) error {
	switch v := src.(type) {
	case string:
		p.s = v
	case []byte:
		p.s = string(v)
	default:
		return fmt.Errorf("values: cannot scan %T into Phone", src)
	}
	return nil
}
