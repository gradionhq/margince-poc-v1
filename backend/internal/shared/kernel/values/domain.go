// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package values

import (
	"database/sql/driver"
	"fmt"
	"net/url"
	"strings"
)

// Domain is a normalized registrable host: lowercased, no scheme, no
// leading www., no port, no path, no trailing dot — the organization
// domain convention (org_domain_norm), parsed once instead of
// re-trimmed at every capture surface.
type Domain struct{ s string }

// ParseDomain accepts a bare host or a full URL and reduces it to the
// normalized host. IDN input is passed through as given (punycode or
// unicode); the kernel does not transcode without x/net.
func ParseDomain(raw string) (Domain, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return Domain{}, &ParseError{Field: "domain", Code: "domain_empty", Message: "a domain is required"}
	}
	host := trimmed
	if strings.Contains(host, "://") {
		u, err := url.Parse(host)
		if err != nil || u.Hostname() == "" {
			return Domain{}, &ParseError{Field: "domain", Code: "domain_malformed", Message: "not a resolvable URL or host name"}
		}
		host = u.Hostname()
	} else if i := strings.IndexAny(host, "/?#"); i >= 0 {
		host = host[:i]
	}
	host = strings.ToLower(host)
	host = strings.TrimPrefix(host, "www.")
	if h, _, ok := strings.Cut(host, ":"); ok {
		host = h
	}
	host = strings.TrimSuffix(host, ".")

	if !validHost(host) {
		return Domain{}, &ParseError{Field: "domain", Code: "domain_malformed",
			Message: "not a host name (labels of letters, digits and inner hyphens, with a dot)"}
	}
	return Domain{s: host}, nil
}

// validHost checks RFC-1123 label shape and requires a dot (a bare
// single label is a LAN name, not an organization's domain).
func validHost(host string) bool {
	labels := strings.Split(host, ".")
	if len(labels) < 2 {
		return false
	}
	for _, l := range labels {
		if len(l) == 0 || len(l) > 63 || l[0] == '-' || l[len(l)-1] == '-' {
			return false
		}
		for _, r := range l {
			if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '-' {
				return false
			}
		}
	}
	// An all-numeric final label is an IP fragment, not a TLD.
	return strings.Trim(labels[len(labels)-1], "0123456789") != ""
}

func (d Domain) String() string { return d.s }
func (d Domain) IsZero() bool   { return d.s == "" }

func (d Domain) Value() (driver.Value, error) { return d.s, nil }

//craft:ignore naked-any sql.Scanner mandates the any source parameter
func (d *Domain) Scan(src any) error {
	switch v := src.(type) {
	case string:
		d.s = v
	case []byte:
		d.s = string(v)
	default:
		return fmt.Errorf("values: cannot scan %T into Domain", src)
	}
	return nil
}
