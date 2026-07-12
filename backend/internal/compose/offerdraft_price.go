// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The offer-drafting orchestrator's price-grounding ladder (arc 4b,
// delta 1 cont'd; split out of offerdraft.go to keep each file under one
// concept): resolving a candidate's price, and the exact-decimal and
// evidence-matching primitives that ladder depends on. offerdraft.go
// stays the orchestration/evidence-gate concept; this file is the
// money-specific rules that gate applies once a line has already earned
// its citation.

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"strconv"
	"strings"

	"github.com/gradionhq/margince/backend/internal/modules/deals"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// resolvePrice is the price-grounding ladder (features/07 §8b, poc-1's
// price_grounded convention, OFFER-AC-14): a price the evidence itself
// STATES outranks a rate-card lookup, which outranks the honest zero
// sentinel — a price is NEVER guessed. The conversation rung only fires
// when the amount itself is present in the cited snippet
// (priceEvidencedInSnippet); the rate-card rung only fires when the
// matched product is real, still live, AND priced in the offer's OWN
// currency — a same-id product in a different currency must never be
// stamped grounded with a wrong-currency amount. A hallucinated/stale
// product_id or a currency mismatch just fails to ground, same as
// omitting the field. The one true error: GetProduct failing for a
// reason OTHER than "no such product" (a real infra/permission fault),
// which propagates rather than being masked as "could not ground".
func (d offerDrafter) resolvePrice(ctx context.Context, c offerLineCandidate, snippet, currency string, line *deals.StagedOfferLineInput) error {
	if c.ConversationPriceMinor != nil && *c.ConversationPriceMinor >= 0 &&
		priceEvidencedInSnippet(snippet, *c.ConversationPriceMinor, currency) {
		line.UnitPriceMinor = *c.ConversationPriceMinor
		line.PriceGrounded = true
		return nil
	}
	if productID := strings.TrimSpace(c.ProductID); productID != "" {
		if id, err := ids.ParseAs[ids.ProductKind](productID); err == nil {
			product, err := d.deals.GetProduct(ctx, id, storekit.LiveOnly)
			switch {
			case err == nil && product.Currency == currency:
				line.UnitPriceMinor = product.UnitPriceMinor
				line.PriceGrounded = true
				return nil
			case err == nil, errors.Is(err, apperrors.ErrNotFound):
				// Wrong currency or a stale/hallucinated id: fails to
				// ground, falls through to the zero sentinel below —
				// the description/evidence already passed the gate, so
				// the line still stages, just ungrounded.
			default:
				// A real infra/permission fault, not a grounding verdict:
				// this ladder's job is grounding, not fault isolation, so
				// it must not be silently relabeled "ungrounded".
				return err
			}
		}
	}
	line.UnitPriceMinor = 0
	line.PriceGrounded = false
	return nil
}

// validDecimal parses a wire decimal string using the SAME exact-decimal
// grammar the store enforces (deals' ratFromDecimal, offer_totals.go):
// only digits, '.', and '-' — no scientific notation, no NaN/Inf, no
// underscore digit separators, no hex floats. strconv.ParseFloat accepts
// all of those, which let a candidate pass this gate and then fail
// AddStagedOfferLines' stricter parser, erroring the WHOLE staging batch
// (500) instead of dropping the one bad line — this mirrors the store's
// acceptance exactly so nothing that passes here can fail there. Returns
// the original string (the seam below wants the exact decimal text, not
// a re-rendered float), the parsed value for the caller's own bound
// checks, and whether it passed.
func validDecimal(s string, lo, hi float64) (string, float64, bool) {
	s = strings.TrimSpace(s)
	for _, r := range s {
		if (r < '0' || r > '9') && r != '.' && r != '-' {
			return "", 0, false
		}
	}
	rat, ok := new(big.Rat).SetString(s)
	if !ok {
		return "", 0, false
	}
	v, _ := rat.Float64()
	if v < lo || v > hi {
		return "", 0, false
	}
	return s, v, true
}

// currencyMinorDigits is the ISO 4217 decimal-places exception table.
// Most currencies — including EUR/USD, the only two this workspace
// exercises today — carry 2 minor-unit digits, but the standard's
// zero- and three-digit exceptions are well known and cheap to honor
// rather than blindly assuming /100 for every currency code when
// rendering a price's major-unit form for evidence matching.
var currencyMinorDigits = map[string]int{
	"BIF": 0, "CLP": 0, "DJF": 0, "GNF": 0, "ISK": 0, "JPY": 0, "KMF": 0,
	"KRW": 0, "PYG": 0, "RWF": 0, "UGX": 0, "VND": 0, "VUV": 0, "XAF": 0,
	"XOF": 0, "XPF": 0,
	"BHD": 3, "IQD": 3, "JOD": 3, "KWD": 3, "LYD": 3, "OMR": 3, "TND": 3,
}

// minorUnitDigits returns how many minor-unit digits a currency code
// carries, defaulting to 2 (the common case, and the only shape unknown
// codes can honestly be assumed to have).
func minorUnitDigits(currency string) int {
	if d, ok := currencyMinorDigits[strings.ToUpper(strings.TrimSpace(currency))]; ok {
		return d
	}
	return 2
}

// priceEvidencedInSnippet is the conversation-price rung's evidence
// check (OFFER-AC-14): the price the model claims the customer discussed
// must actually appear in what it cited, not merely ride along with some
// unrelated citation. It checks the minor-unit integer verbatim (the
// wire shape the model itself reports, e.g. "20000") and the currency's
// major-unit rendering in both the plain ("200") and zero-padded decimal
// ("200.00") forms a human conversation would actually use — never a
// guess, just the honest textual forms the same number can take.
func priceEvidencedInSnippet(snippet string, priceMinor int64, currency string) bool {
	if priceMinor < 0 {
		return false
	}
	if strings.Contains(snippet, strconv.FormatInt(priceMinor, 10)) {
		return true
	}
	digits := minorUnitDigits(currency)
	if digits == 0 {
		return false // the minor integer above IS the major form for a zero-decimal currency
	}
	scale := int64(1)
	for i := 0; i < digits; i++ {
		scale *= 10
	}
	whole, frac := priceMinor/scale, priceMinor%scale
	plain := strconv.FormatInt(whole, 10)
	full := fmt.Sprintf("%d.%0*d", whole, digits, frac)
	return strings.Contains(snippet, plain) || strings.Contains(snippet, full)
}
