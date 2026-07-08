// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package values

import (
	"database/sql/driver"
	"fmt"
	"regexp"
	"strings"
)

// slugShape is the workspace-slug contract: lowercase alphanumeric
// runs joined by single hyphens (the subdomain-safe form).
var slugShape = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// Slug is a normalized URL/subdomain identifier.
type Slug struct{ s string }

func ParseSlug(raw string) (Slug, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" || len(trimmed) > 63 || !slugShape.MatchString(trimmed) {
		return Slug{}, &ParseError{Field: "slug", Code: "slug_malformed",
			Message: "a slug is 1–63 chars of lowercase letters, digits and single inner hyphens"}
	}
	return Slug{s: trimmed}, nil
}

func (s Slug) String() string { return s.s }
func (s Slug) IsZero() bool   { return s.s == "" }

func (s Slug) Value() (driver.Value, error) { return s.s, nil }

//craft:ignore naked-any sql.Scanner mandates the any source parameter
func (s *Slug) Scan(src any) error {
	switch v := src.(type) {
	case string:
		s.s = v
	case []byte:
		s.s = string(v)
	default:
		return fmt.Errorf("values: cannot scan %T into Slug", src)
	}
	return nil
}
