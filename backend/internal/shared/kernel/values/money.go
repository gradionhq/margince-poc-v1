// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package values

import "regexp"

// iso4217 mirrors the schema's currency CHECKs (deal/product/offer/
// workspace all validate ^[A-Z]{3}$) — one Go spelling for the four
// SQL copies.
var iso4217 = regexp.MustCompile(`^[A-Z]{3}$`)

// ValidCurrency answers whether s has the shape of an ISO-4217 code, for the
// callers that hold a currency string without minting a Money out of it — the
// FX-rate pair, the installation's base-currency setting.
//
// Exported because both of those had written their own: one a rune loop, one a
// verbatim copy of the regexp var above under the same name. Three spellings of
// one predicate is three places to correct when the schema's CHECK moves, and
// the copy nobody remembers is the one that keeps admitting what the database
// then refuses.
//
// It is a SHAPE check, not a membership check: it says the string looks like a
// currency code, not that ISO assigns it. That is deliberately the same claim
// the schema's CHECK makes, so Go and the database agree about what they admit
// rather than one refusing what the other stored.
func ValidCurrency(s string) bool { return iso4217.MatchString(s) }

// Money binds an amount in minor units to its ISO-4217 currency. The
// fields are unexported so a half-money — the amount-without-currency
// row that silently skips the FX freeze — is unrepresentable. The zero
// value means "no amount", matching the schema's both-NULL state.
// Money carries no Valuer/Scanner: it persists as the two columns the
// schema defines, destructured explicitly at each store.
type Money struct {
	amountMinor int64
	currency    string
}

func NewMoney(amountMinor int64, currency string) (Money, error) {
	if !ValidCurrency(currency) {
		return Money{}, &ParseError{Field: "currency", Code: "currency_malformed",
			Message: "currency is the three-letter ISO-4217 code, uppercase"}
	}
	return Money{amountMinor: amountMinor, currency: currency}, nil
}

func (m Money) AmountMinor() int64 { return m.amountMinor }
func (m Money) Currency() string   { return m.currency }
func (m Money) IsZero() bool       { return m == Money{} }

// Add refuses to blend currencies — converting silently would fabricate
// a number (P11); the caller converts explicitly or errors out.
func (m Money) Add(o Money) (Money, error) {
	if m.currency != o.currency {
		return Money{}, &ParseError{Field: "currency", Code: "currency_mismatch",
			Message: "cannot add " + o.currency + " to " + m.currency + " without an explicit conversion"}
	}
	return Money{amountMinor: m.amountMinor + o.amountMinor, currency: m.currency}, nil
}
