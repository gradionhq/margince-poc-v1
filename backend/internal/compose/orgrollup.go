// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// Pure mechanics behind GET /organizations/{id}/hierarchy-rollup
// (RD-T04): the RBAC-aware BFS prune over the parent→children org
// graph, calendar-quarter bounds in the workspace timezone, and the
// two rounding rules (win-weighted value, FX base conversion) the
// rollup's totals are built from. No DB and no HTTP live here — the
// gated tree walk and measures are in orgrollupread.go, the HTTP
// handler arrives with the transport slice.

import (
	"fmt"
	"math/big"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// orgTreeNode is one row of the flattened organization hierarchy:
// enough to walk parent→children and to name a node the caller can't
// see into.
type orgTreeNode struct {
	id          ids.UUID
	parentID    *ids.UUID
	displayName string
}

// restrictedNode is a hierarchy node the caller cannot read, disclosed
// by identity and name only — never by its figures, and never by its
// subtree's.
type restrictedNode struct {
	ID          ids.UUID
	DisplayName string
}

// pruneUnreadable walks the org tree breadth-first from rootID over the
// parent→children adjacency nodes encodes, splitting it into the
// RBAC-readable set the rollup sums and the restricted set it discloses
// without summing. The root itself is never disclosed as restricted —
// an unreadable root is a 404 at the HTTP layer (rootReadable=false
// signals that), not a member of any list.
//
// A node the caller can't read is the deepest point a branch is
// visited: its children are never inspected, so a grandchild behind a
// restricted node is neither included nor separately disclosed. Because
// readable is consulted fresh for every node, a live grant that flips a
// node back to readable pulls its whole readable subtree back in on the
// very next call — pruneUnreadable holds no memory of a prior result.
func pruneUnreadable(rootID ids.UUID, nodes []orgTreeNode, readable func(ids.UUID) bool) (included []ids.UUID, restricted []restrictedNode, rootReadable bool) {
	included = []ids.UUID{}
	restricted = []restrictedNode{}
	if !readable(rootID) {
		return included, restricted, false
	}

	childrenByParent := make(map[ids.UUID][]orgTreeNode, len(nodes))
	for _, n := range nodes {
		if n.parentID == nil {
			continue
		}
		childrenByParent[*n.parentID] = append(childrenByParent[*n.parentID], n)
	}

	included = append(included, rootID)
	queue := []ids.UUID{rootID}
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		for _, child := range childrenByParent[current] {
			if !readable(child.id) {
				restricted = append(restricted, restrictedNode{ID: child.id, DisplayName: child.displayName})
				continue
			}
			included = append(included, child.id)
			queue = append(queue, child.id)
		}
	}
	return included, restricted, true
}

// currentQuarterBounds returns the calendar quarter [start, end) that
// now falls in, evaluated in loc — the workspace timezone, not UTC, so
// a moment shortly after midnight UTC can still belong to the prior
// quarter (and year) for a workspace west of Greenwich.
func currentQuarterBounds(now time.Time, loc *time.Location) (start, end time.Time) {
	local := now.In(loc)
	quarterStartMonth := time.Month(((int(local.Month())-1)/3)*3 + 1)
	start = time.Date(local.Year(), quarterStartMonth, 1, 0, 0, 0, 0, loc)
	end = start.AddDate(0, 3, 0)
	return start, end
}

// weightedValue rounds baseMinor × winProbability/100 half away from
// zero, in EXACT big.Int arithmetic — never a native int64 multiply.
// amount_minor is contract-unbounded, so baseMinor×winProbability can
// exceed int64 before the division ever runs; a silent wraparound there
// would put a wrong number in a money total. The overflow check mirrors
// convertToBase's: a result outside int64's range refuses loudly rather
// than truncating.
func weightedValue(baseMinor int64, winProbability int) (int64, error) {
	product := new(big.Int).Mul(big.NewInt(baseMinor), big.NewInt(int64(winProbability)))
	rounded := bigDivRoundHalfAwayFromZero(product, big.NewInt(100))
	if !rounded.IsInt64() {
		return 0, fmt.Errorf("weighted pipeline value for a %d-minor-unit amount at %d%% exceeds the representable money range; correct the deal amount before retrying the rollup",
			baseMinor, winProbability)
	}
	return rounded.Int64(), nil
}

// convertToBase rounds amountMinor × rate half away from zero, in EXACT
// decimal arithmetic over the rate's stored numeric digits (Int × 10^Exp)
// — never float64, so the open-pipeline conversion carries the same
// exactness Postgres ROUND over numeric gives closed-won, and an amount
// past float64's 2^53 exact-integer ceiling cannot lose a minor unit. A
// non-finite rate or an overflowing result refuses loudly: both would
// otherwise put a silently wrong number in a money total.
func convertToBase(amountMinor int64, rate pgtype.Numeric) (int64, error) {
	if !rate.Valid || rate.NaN || rate.InfinityModifier != pgtype.Finite {
		return 0, fmt.Errorf("stored FX rate is not a finite number; correct the fx_rate row before retrying the rollup")
	}
	product := new(big.Int).Mul(big.NewInt(amountMinor), rate.Int)
	if rate.Exp >= 0 {
		product.Mul(product, pow10(int64(rate.Exp)))
	} else {
		product = bigDivRoundHalfAwayFromZero(product, pow10(int64(-rate.Exp)))
	}
	if !product.IsInt64() {
		return 0, fmt.Errorf("converted amount exceeds the representable money range in the base currency")
	}
	return product.Int64(), nil
}

// bigDivRoundHalfAwayFromZero is divRoundHalfAwayFromZero over big
// integers: numerator/denominator with the quotient rounded half away
// from zero. denominator is always a positive power of ten here.
func bigDivRoundHalfAwayFromZero(numerator, denominator *big.Int) *big.Int {
	negative := numerator.Sign() < 0
	quotient, remainder := new(big.Int).QuoRem(numerator, denominator, new(big.Int))
	remainder.Abs(remainder)
	remainder.Lsh(remainder, 1) // 2·|remainder| ≥ denominator ⇔ the dropped fraction is ≥ half
	if remainder.Cmp(denominator) < 0 {
		return quotient
	}
	if negative {
		return quotient.Sub(quotient, big.NewInt(1))
	}
	return quotient.Add(quotient, big.NewInt(1))
}

// pow10 returns 10^exp as a big integer; exp is a numeric's scale
// magnitude, always small and never negative here.
func pow10(exp int64) *big.Int {
	return new(big.Int).Exp(big.NewInt(10), big.NewInt(exp), nil)
}

// FXRateUnavailableError reports that the rollup needed a stored FX
// rate for currency as of a point in time and none was on file — the
// system never invents a rate=1 fallback, per formulas §11. Exported so
// the HTTP layer and the integration suites match it via errors.As and
// map it to 422 fx_rate_unavailable.
type FXRateUnavailableError struct {
	Currency string
	AsOf     time.Time
}

func (e FXRateUnavailableError) Error() string {
	return fmt.Sprintf("no stored FX rate for %s as of %s; record today's rate for %s before retrying the rollup",
		e.Currency, e.AsOf.Format(time.DateOnly), e.Currency)
}
