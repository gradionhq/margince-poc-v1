// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package people

import (
	"strings"
	"unicode"

	"golang.org/x/text/runes"
	"golang.org/x/text/transform"
	"golang.org/x/text/unicode/norm"
)

// The string metric behind the dedupe fuzzy tier (PO-F-1/PO-F-2
// `name_sim`). Pinned to standard Jaro-Winkler with prefix scale p=0.1,
// max prefix 4, and no boost threshold (PO-PARAM-JW-1) over casefolded,
// unaccented input (PO-PARAM-JW-2) — pinned so the spec's worked
// examples stay reproducible against this code.
const (
	jaroWinklerPrefixScale = 0.1
	jaroWinklerMaxPrefix   = 4
)

// legalSuffixes is PO-PARAM-1: the trailing tokens org-name
// normalization strips so "Acme Inc" and "Acme GmbH" both reduce to
// "acme" and meet at the fuzzy tier for a human to judge.
var legalSuffixes = map[string]bool{
	"inc": true, "llc": true, "ltd": true, "gmbh": true, "ag": true,
	"sa": true, "sas": true, "bv": true, "oy": true, "plc": true,
	"co": true, "corp": true, "kg": true, "ug": true,
}

// normalizeName casefolds and unaccents (PO-PARAM-JW-2). Both sides of
// every comparison run through it, so the metric stays internally
// consistent; it is deliberately not required to agree rune-for-rune
// with Postgres f_unaccent, which only narrows the candidate set.
func normalizeName(s string) string {
	t := transform.Chain(norm.NFD, runes.Remove(runes.In(unicode.Mn)), norm.NFC)
	folded, _, err := transform.String(t, s)
	if err != nil {
		// Decomposition failure means a malformed rune, not an outage:
		// compare what we were given rather than dropping the candidate.
		folded = s
	}
	return strings.ToLower(strings.TrimSpace(folded))
}

// normalizeOrgName is normalizeName plus the PO-PARAM-1 legal-suffix
// strip, applied only to the trailing token: "Co" inside "Coca Co" is a
// name, "Co" at the end is a suffix.
func normalizeOrgName(s string) string {
	fields := strings.Fields(normalizeName(strings.ReplaceAll(s, ",", " ")))
	for len(fields) > 1 {
		last := strings.Trim(fields[len(fields)-1], ".")
		if !legalSuffixes[last] {
			break
		}
		fields = fields[:len(fields)-1]
	}
	return strings.Join(fields, " ")
}

// nameSimilarity is `name_sim`: Jaro-Winkler over normalized input,
// in [0,1].
func nameSimilarity(a, b string) float64 {
	return jaroWinkler(normalizeName(a), normalizeName(b))
}

// jaroWinkler applies the Winkler prefix boost unconditionally — the
// pinned variant has no boost threshold, so a low-Jaro pair with a
// shared prefix still gains (PO-PARAM-JW-1).
func jaroWinkler(a, b string) float64 {
	j := jaro(a, b)
	if j == 0 {
		return 0
	}
	ra, rb := []rune(a), []rune(b)
	prefix := 0
	for prefix < len(ra) && prefix < len(rb) && prefix < jaroWinklerMaxPrefix && ra[prefix] == rb[prefix] {
		prefix++
	}
	return j + float64(prefix)*jaroWinklerPrefixScale*(1-j)
}

// jaro is the standard Jaro similarity: matches inside a half-length
// window, discounted by half the transpositions among them.
func jaro(a, b string) float64 {
	ra, rb := []rune(a), []rune(b)
	if len(ra) == 0 && len(rb) == 0 {
		return 1
	}
	if len(ra) == 0 || len(rb) == 0 {
		return 0
	}

	window := max(len(ra), len(rb))/2 - 1
	if window < 0 {
		window = 0
	}

	matchedA := make([]bool, len(ra))
	matchedB := make([]bool, len(rb))
	matches := 0
	for i, r := range ra {
		lo := max(0, i-window)
		hi := min(len(rb)-1, i+window)
		for j := lo; j <= hi; j++ {
			if matchedB[j] || rb[j] != r {
				continue
			}
			matchedA[i], matchedB[j] = true, true
			matches++
			break
		}
	}
	if matches == 0 {
		return 0
	}

	// Transpositions: matched runes that pair up out of order. Each such
	// pair is counted twice by this walk, hence the halving below.
	transpositions := 0
	k := 0
	for i := range ra {
		if !matchedA[i] {
			continue
		}
		for !matchedB[k] {
			k++
		}
		if ra[i] != rb[k] {
			transpositions++
		}
		k++
	}

	m := float64(matches)
	return (m/float64(len(ra)) + m/float64(len(rb)) + (m-float64(transpositions)/2)/m) / 3
}
