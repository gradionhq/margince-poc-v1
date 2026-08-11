// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package people

// The size_band column is a closed enum; employee_range is the page's own
// phrasing ("25 to 50", "about 120 people", "51-200"). The mapping between
// them REFUSES rather than guesses (PO-AC-N-7's spirit for a derived
// canonical value): a phrase lands in a band only when every headcount it
// could honestly mean falls inside that one band.

import (
	"context"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
)

// sizeBands mirrors the organization.size_band CHECK (0005) in ascending
// order; the top band is open-ended.
var sizeBands = []struct {
	label  string
	lo, hi int
}{
	{"1-10", 1, 10},
	{"11-50", 11, 50},
	{"51-200", 51, 200},
	{"201-500", 201, 500},
	{"501-1000", 501, 1000},
	{"1001-5000", 1001, 5000},
	{"5000+", 5001, math.MaxInt},
}

var (
	// A magnitude word next to a digit ("10k", "2 thousand") means the
	// digits alone are NOT the headcount — always a refusal, never a parse.
	employeeRangeMagnitude = regexp.MustCompile(`(?i)\d\s*(k\b|thousand|tausend|million|mio\b)`)
	employeeRangeNumber    = regexp.MustCompile(`\d[\d,.]*`)
	// Thousands separators are the one punctuation a headcount may carry;
	// "2.5" is a decimal, and a decimal headcount is nobody's clean answer.
	thousandsShaped = regexp.MustCompile(`^\d{1,3}([,.]\d{3})*$|^\d+$`)
)

// sizeBandFromEmployeeRange maps a stated headcount phrase onto the
// size_band enum, reporting false whenever the phrasing does not land
// cleanly in exactly one band.
func sizeBandFromEmployeeRange(text string) (string, bool) {
	trimmed := strings.TrimSpace(text)
	for _, band := range sizeBands {
		if trimmed == band.label {
			return band.label, true
		}
	}
	if employeeRangeMagnitude.MatchString(trimmed) {
		return "", false
	}
	numbers, ok := employeeRangeNumbers(trimmed)
	if !ok {
		return "", false
	}
	// "200+" states a floor with no ceiling: only the open top band can
	// contain all of it.
	if strings.Contains(trimmed, "+") {
		if len(numbers) == 1 && numbers[0] > 5000 {
			return "5000+", true
		}
		return "", false
	}
	switch len(numbers) {
	case 1:
		return bandContaining(numbers[0])
	case 2:
		lowBand, okLow := bandContaining(numbers[0])
		highBand, okHigh := bandContaining(numbers[1])
		if okLow && okHigh && numbers[0] <= numbers[1] && lowBand == highBand {
			return lowBand, true
		}
	}
	return "", false
}

// employeeRangeNumbers extracts the headcounts a phrase states, refusing
// any token whose punctuation is not thousands-shaped.
func employeeRangeNumbers(text string) ([]int, bool) {
	tokens := employeeRangeNumber.FindAllString(text, -1)
	numbers := make([]int, 0, len(tokens))
	for _, token := range tokens {
		token = strings.TrimRight(token, ",.")
		if !thousandsShaped.MatchString(token) {
			return nil, false
		}
		n, err := strconv.Atoi(strings.NewReplacer(",", "", ".", "").Replace(token))
		if err != nil {
			return nil, false
		}
		numbers = append(numbers, n)
	}
	return numbers, len(numbers) > 0
}

func bandContaining(n int) (string, bool) {
	for _, band := range sizeBands {
		if n >= band.lo && n <= band.hi {
			return band.label, true
		}
	}
	return "", false
}

// fillSizeBandFromFacts promotes an accepted employee_range fact onto the
// size_band column — only while the column is empty, so a human's (or an
// import's) standing value is never overwritten, and only when the phrasing
// maps cleanly. On abstention the fact row stays the evidence and the column
// stays fillable by a later, cleaner read.
func fillSizeBandFromFacts(ctx context.Context, tx pgx.Tx, in DeepReadProposal, by string) error {
	for _, f := range in.Facts {
		if f.Category != factCategoryCompany || f.Field != FactEmployeeRange {
			continue
		}
		band, ok := sizeBandFromEmployeeRange(f.Value)
		if !ok {
			return nil
		}
		tag, err := tx.Exec(ctx,
			`UPDATE organization SET size_band = $2 WHERE id = $1 AND size_band IS NULL`,
			in.OrganizationID, band)
		if err != nil {
			return fmt.Errorf("fill size_band: %w", err)
		}
		if tag.RowsAffected() == 1 {
			confidence := f.Confidence
			stamp := storekit.FieldStamp{Field: "size_band", Confidence: &confidence}
			if f.SourceURL != "" {
				stamp.EvidenceRef = &f.SourceURL
			}
			if err := storekit.StampFields(ctx, tx, "organization", in.OrganizationID.UUID,
				companySourceSiteRead, by, []storekit.FieldStamp{stamp}); err != nil {
				return err
			}
		}
		// employee_range is single-value: there is at most one such fact.
		return nil
	}
	return nil
}
