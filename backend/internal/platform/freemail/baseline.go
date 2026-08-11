// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package freemail

// The pinned consumer-domain baseline (CAP-PARAM-5): a vendored public dataset
// plus this repo's own pins. Both are source constants — additions land through
// the spec, or through a workspace's own extra list, never as an ad-hoc edit
// here. data/README.md carries the dataset's provenance, its license, and the
// upstream defects the sanitizer below handles.

import (
	_ "embed"
	"slices"
	"strings"
	"sync"
)

//go:embed data/providers.txt
var vendoredProviders string

// pinnedBaseline are the domains this repo carries itself: everything the
// vendored dataset misses. Keeping them pinned rather than folding them into
// the vendored file means a re-sync is a clean overwrite that cannot silently
// drop one of them.
var pinnedBaseline = []string{
	// Absent from the dataset entirely.
	"fastmail.com",
	"posteo.de",
	"tutanota.com",
	"tuta.io",
	"duck.com",
	"gmx.ch",
	"ziggo.nl",
	// Present upstream but unreachable: a missing newline glued it to the
	// preceding entry (data/README.md, line 8758).
	"0-mail.com",
}

// baseline parses the vendored dataset once and unions it with the pins.
var baseline = sync.OnceValue(func() map[string]struct{} {
	set := make(map[string]struct{}, strings.Count(vendoredProviders, "\n")+len(pinnedBaseline))
	for line := range strings.Lines(vendoredProviders) {
		if domain, ok := sanitize(line); ok {
			set[domain] = struct{}{}
		}
	}
	for _, domain := range pinnedBaseline {
		if d := normalize(domain); d != "" {
			set[d] = struct{}{}
		}
	}
	return set
})

// sortedBaseline is the same set as one alphabetical list, materialized once —
// the read surface that shows an operator what the shipped list contains.
var sortedBaseline = sync.OnceValue(func() []string {
	set := baseline()
	out := make([]string, 0, len(set))
	for domain := range set {
		out = append(out, domain)
	}
	slices.Sort(out)
	return out
})

// Domains returns the shipped baseline, alphabetical. The copy is the
// caller's own — handing out the memoized slice would let one caller's sort
// or reslice corrupt the process-wide baseline for every workspace.
func Domains() []string {
	return slices.Clone(sortedBaseline())
}

// sanitize turns one dataset line into a usable domain, reporting false for a
// line that cannot be one. The vendored file is byte-identical to upstream, so
// its defects are handled here rather than by editing the copy: blank lines and
// comments carry nothing, an entry with no dot cannot be a mail domain, and a
// Unicode entry has to reach punycode to match what a mail header carries.
func sanitize(line string) (string, bool) {
	domain := normalize(line)
	if domain == "" || strings.HasPrefix(domain, "#") {
		return "", false
	}
	if !strings.Contains(domain, ".") {
		return "", false
	}
	return domain, true
}
