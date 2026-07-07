// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package values

import (
	"database/sql/driver"
	"fmt"
	"net/mail"
	"strings"
)

// Email is a normalized address: parsed once, stored lowercased — the
// same convention the schema enforces (person_email_norm/lead_email_norm
// CHECKs), so dedupe by address can never miss on case.
type Email struct{ s string }

// ParseEmail accepts a bare addr-spec (no display name — "Ada <a@b>" is
// a UI artifact, not an address) and returns it lowercased.
func ParseEmail(raw string) (Email, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return Email{}, &ParseError{Field: "email", Code: "email_empty", Message: "an email address is required"}
	}
	addr, err := mail.ParseAddress(trimmed)
	if err != nil || !strings.EqualFold(addr.Address, trimmed) {
		return Email{}, &ParseError{Field: "email", Code: "email_malformed",
			Message: "not a plain email address (user@domain, no display name)"}
	}
	return Email{s: strings.ToLower(addr.Address)}, nil
}

func (e Email) String() string { return e.s }
func (e Email) IsZero() bool   { return e.s == "" }

// Domain is the part after the last @ — the org-key derivation input.
func (e Email) Domain() string {
	at := strings.LastIndex(e.s, "@")
	if at < 0 {
		return ""
	}
	return e.s[at+1:]
}

func (e Email) Value() (driver.Value, error) { return e.s, nil }

func (e *Email) Scan(src any) error {
	switch v := src.(type) {
	case string:
		e.s = v
	case []byte:
		e.s = string(v)
	default:
		return fmt.Errorf("values: cannot scan %T into Email", src)
	}
	return nil
}
