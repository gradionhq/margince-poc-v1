// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package values

import (
	"errors"
	"testing"
)

func wantParseError(t *testing.T, err error, code string) {
	t.Helper()
	var pe *ParseError
	if !errors.As(err, &pe) {
		t.Fatalf("err = %v, want a *ParseError(%s)", err, code)
	}
	if pe.Code != code {
		t.Fatalf("code = %s, want %s", pe.Code, code)
	}
}

func TestParseEmail(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want string
		code string
	}{
		{in: "Ada.Lovelace@Example.COM", want: "ada.lovelace@example.com"},
		{in: "  ada@example.com  ", want: "ada@example.com"},
		{in: "Ada <ada@example.com>", code: "email_malformed"},
		{in: "not-an-email", code: "email_malformed"},
		{in: "", code: "email_empty"},
		{in: "a@b@c", code: "email_malformed"},
	} {
		got, err := ParseEmail(tc.in)
		if tc.code != "" {
			wantParseError(t, err, tc.code)
			continue
		}
		if err != nil {
			t.Fatalf("ParseEmail(%q): %v", tc.in, err)
		}
		if got.String() != tc.want {
			t.Errorf("ParseEmail(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
	e, err := ParseEmail("ada@sub.example.com")
	if err != nil || e.Domain() != "sub.example.com" {
		t.Errorf("Domain() = %q (%v), want sub.example.com", e.Domain(), err)
	}
}

func TestParsePhone(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want string
		code string
	}{
		{in: "+49 (30) 1234-5678", want: "+493012345678"},
		{in: "0049 30 12345678", want: "+493012345678"},
		{in: "+1 415.555.0123", want: "+14155550123"},
		{in: "030 12345678", code: "phone_needs_country_code"},
		{in: "+0 123456789", code: "phone_malformed"},
		{in: "+49 12", code: "phone_malformed"},
		{in: "", code: "phone_empty"},
	} {
		got, err := ParsePhone(tc.in)
		if tc.code != "" {
			wantParseError(t, err, tc.code)
			continue
		}
		if err != nil {
			t.Fatalf("ParsePhone(%q): %v", tc.in, err)
		}
		if got.String() != tc.want {
			t.Errorf("ParsePhone(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestParseDomain(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want string
		code string
	}{
		{in: "Example.COM", want: "example.com"},
		{in: "www.example.com", want: "example.com"},
		{in: "https://www.Example.com:8443/path?q=1", want: "example.com"},
		{in: "example.com.", want: "example.com"},
		{in: "sub.example.co.uk", want: "sub.example.co.uk"},
		{in: "example.com/pricing", want: "example.com"},
		{in: "localhost", code: "domain_malformed"},
		{in: "-bad.example.com", code: "domain_malformed"},
		{in: "127.0.0.1", code: "domain_malformed"},
		{in: "", code: "domain_empty"},
	} {
		got, err := ParseDomain(tc.in)
		if tc.code != "" {
			wantParseError(t, err, tc.code)
			continue
		}
		if err != nil {
			t.Fatalf("ParseDomain(%q): %v", tc.in, err)
		}
		if got.String() != tc.want {
			t.Errorf("ParseDomain(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestMoney(t *testing.T) {
	if _, err := NewMoney(100, "eur"); err == nil {
		t.Error("lowercase currency must not parse")
	}
	if _, err := NewMoney(100, "EURO"); err == nil {
		t.Error("four-letter currency must not parse")
	}
	m, err := NewMoney(2500, "EUR")
	if err != nil || m.AmountMinor() != 2500 || m.Currency() != "EUR" {
		t.Fatalf("NewMoney = %+v (%v)", m, err)
	}
	sum, err := m.Add(mustMoney(t, 500, "EUR"))
	if err != nil || sum.AmountMinor() != 3000 {
		t.Errorf("Add same currency = %+v (%v), want 3000", sum, err)
	}
	if _, err := m.Add(mustMoney(t, 500, "USD")); err == nil {
		t.Error("Add across currencies must refuse")
	}
	if !(Money{}).IsZero() || m.IsZero() {
		t.Error("IsZero must hold for the zero value only")
	}
}

func mustMoney(t *testing.T, amount int64, cur string) Money {
	t.Helper()
	m, err := NewMoney(amount, cur)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestParseSlug(t *testing.T) {
	if _, err := ParseSlug("Acme Corp"); err == nil {
		t.Error("spaces/uppercase must not parse")
	}
	if _, err := ParseSlug("-lead"); err == nil {
		t.Error("leading hyphen must not parse")
	}
	s, err := ParseSlug("acme-corp-2")
	if err != nil || s.String() != "acme-corp-2" {
		t.Fatalf("ParseSlug = %q (%v)", s, err)
	}
}

// TestValueObjectDBSeams exercises the driver.Valuer / sql.Scanner /
// IsZero surface every value object carries so it can round-trip through
// storekit. A broken Scan here silently corrupts a persisted email/phone/
// domain/slug, so each seam is pinned: Value emits the canonical string,
// Scan accepts both the string and the []byte the pgx text protocol hands
// back and rejects an unsupported source type, and IsZero reports empty.
func TestValueObjectDBSeams(t *testing.T) {
	email, _ := ParseEmail("ada@example.com")
	phone, _ := ParsePhone("+493012345678")
	domain, _ := ParseDomain("example.com")
	slug, _ := ParseSlug("acme-corp")

	// Value() emits the canonical string form on each type.
	if v, err := email.Value(); err != nil || v != "ada@example.com" {
		t.Fatalf("Email.Value = %v (%v)", v, err)
	}
	if v, err := phone.Value(); err != nil || v != "+493012345678" {
		t.Fatalf("Phone.Value = %v (%v)", v, err)
	}
	if v, err := domain.Value(); err != nil || v != "example.com" {
		t.Fatalf("Domain.Value = %v (%v)", v, err)
	}
	if v, err := slug.Value(); err != nil || v != "acme-corp" {
		t.Fatalf("Slug.Value = %v (%v)", v, err)
	}

	// Scan() accepts the string form and the []byte form, and rejects junk.
	var e Email
	if err := e.Scan("bob@example.com"); err != nil || e.String() != "bob@example.com" {
		t.Fatalf("Email.Scan(string) = %q (%v)", e, err)
	}
	if err := e.Scan([]byte("cid@example.com")); err != nil || e.String() != "cid@example.com" {
		t.Fatalf("Email.Scan([]byte) = %q (%v)", e, err)
	}
	if err := e.Scan(42); err == nil {
		t.Fatal("Email.Scan(int) must reject an unsupported source type")
	}
	var p Phone
	if err := p.Scan([]byte("+441234567890")); err != nil || p.String() != "+441234567890" {
		t.Fatalf("Phone.Scan([]byte) = %q (%v)", p, err)
	}
	if err := p.Scan(0.5); err == nil {
		t.Fatal("Phone.Scan(float) must reject an unsupported source type")
	}
	var d Domain
	if err := d.Scan([]byte("gradion.com")); err != nil || d.String() != "gradion.com" {
		t.Fatalf("Domain.Scan([]byte) = %q (%v)", d, err)
	}
	if err := d.Scan(nil); err == nil {
		t.Fatal("Domain.Scan(nil) must reject an unsupported source type")
	}
	var s Slug
	if err := s.Scan("team-x"); err != nil || s.String() != "team-x" {
		t.Fatalf("Slug.Scan(string) = %q (%v)", s, err)
	}
	if err := s.Scan(struct{}{}); err == nil {
		t.Fatal("Slug.Scan(struct) must reject an unsupported source type")
	}

	// IsZero reports the empty value on each type, false once parsed.
	if !(Email{}).IsZero() || !(Phone{}).IsZero() || !(Domain{}).IsZero() || !(Slug{}).IsZero() {
		t.Fatal("zero value objects must report IsZero()==true")
	}
	if email.IsZero() || phone.IsZero() || domain.IsZero() || slug.IsZero() {
		t.Fatal("parsed value objects must report IsZero()==false")
	}
}

func TestParseTimezone(t *testing.T) {
	tz, err := ParseTimezone("Europe/Berlin")
	if err != nil || tz.String() != "Europe/Berlin" {
		t.Fatalf("ParseTimezone = %q (%v)", tz, err)
	}
	if loc, err := tz.Location(); err != nil || loc.String() != "Europe/Berlin" {
		t.Errorf("Location = %v (%v)", loc, err)
	}
	if _, err := ParseTimezone("Mars/Olympus"); err == nil {
		t.Error("unknown zone must not parse")
	}
	if _, err := ParseTimezone("Local"); err == nil {
		t.Error("Local must not parse — it means a different zone per host")
	}
}
