// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package capture

// The shipped consumer-mail baseline as a READ surface (CAP-PARAM-5). The
// workspace list an operator edits is only ever a delta against the shipped
// dataset, so judging whether a domain needs an `extra` entry — or a `never`
// carve-out — starts with seeing what the baseline already says. This answers
// that without a page-through of 8 700 rows: a count, and a filtered slice.

import (
	"context"
	"strings"

	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/freemail"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// BaselinePageSize caps how many baseline domains one search answer carries.
// The count says how many matched; the slice exists to be read, not exported.
const BaselinePageSize = 50

// BaselineSearch is one answer about the shipped baseline.
type BaselineSearch struct {
	// Domains is the first BaselinePageSize matches, alphabetical.
	Domains []string
	// Matched is how many baseline domains the filter matched in total.
	Matched int
	// Total is the size of the whole shipped baseline.
	Total int
}

// SearchBaseline filters the shipped baseline by a case-insensitive substring.
// An empty filter matches everything, so the first page plus the counts still
// tell the operator what the list is. Gated on the same read every capture
// surface uses — the baseline is source data, but WHERE it is consulted is
// this workspace's capture posture.
func SearchBaseline(ctx context.Context, q string) (BaselineSearch, error) {
	if err := auth.Require(ctx, freemailDomainObject, principal.ActionRead); err != nil {
		return BaselineSearch{}, err
	}
	needle := strings.ToLower(strings.TrimSpace(q))
	all := freemail.Domains()
	out := BaselineSearch{Total: len(all)}
	for _, domain := range all {
		if needle != "" && !strings.Contains(domain, needle) {
			continue
		}
		out.Matched++
		if len(out.Domains) < BaselinePageSize {
			out.Domains = append(out.Domains, domain)
		}
	}
	return out, nil
}
