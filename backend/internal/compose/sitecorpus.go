// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The corpus ranking the profile lane's excerpt selection rides:
// identity-dense pages first (legal > about > team > contact >
// offerings), boilerplate archives last, crawl order breaking ties.

import (
	"sort"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
)

// corpusRank orders pages by fact density — same ranking the crawler's
// candidate priority uses, boilerplate last, the landing page ranked
// with the fact kinds (it states positioning).
func corpusRank(page crawlPage) int {
	if boilerplatePath(page.URL) {
		return priBoilerplate
	}
	if pri, ok := kindPriority[page.Kind]; ok {
		return pri
	}
	if page.Kind == crmcontracts.SiteReadPageKindHome {
		return kindPriority[crmcontracts.SiteReadPageKindContact]
	}
	return priOther
}

// sortPagesByCorpusRank stable-sorts pages most-dense-first; stability
// keeps crawl order as the tiebreak, so downstream selection stays
// deterministic.
func sortPagesByCorpusRank(pages []crawlPage) {
	sort.SliceStable(pages, func(i, j int) bool {
		return corpusRank(pages[i]) > corpusRank(pages[j])
	})
}
