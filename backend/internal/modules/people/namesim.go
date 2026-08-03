// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package people

import (
	"strings"
	"unicode"

	"golang.org/x/text/cases"
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

// legalConnectives join the halves of a COMPOUND legal form: "GmbH & Co. KG"
// is one form, and a strip that halted on the ampersand left "basecom gmbh &"
// as the key — a name no account is stored under.
//
// They are NOT suffixes in their own right, and treating them as such was
// wrong: it collapsed "Research and" to "research" and "Miller und" to
// "miller", so two unrelated accounts could meet at the same key. A connective
// is consumed only when a real suffix has already been stripped and another
// one follows it — which is exactly the compound case and nothing else.
var legalConnectives = map[string]bool{"&": true, "und": true, "and": true}

// normalizeName casefolds and unaccents (PO-PARAM-JW-2). Both sides of
// every comparison run through it, so the metric stays internally
// consistent; it is deliberately not required to agree rune-for-rune
// with Postgres f_unaccent, which only narrows the candidate set.
// Casefold is Unicode FULL folding, not ToLower — Straße and STRASSE
// must compare equal (ß folds to ss), and the DACH market meets that
// pair daily. A fresh Caser per call: cases.Caser is stateful and not
// safe for concurrent use.
func normalizeName(s string) string {
	t := transform.Chain(norm.NFD, runes.Remove(runes.In(unicode.Mn)), norm.NFC)
	unaccented, _, err := transform.String(t, s)
	if err != nil {
		// Decomposition failure means a malformed rune, not an outage:
		// compare what we were given rather than dropping the candidate.
		unaccented = s
	}
	return strings.TrimSpace(cases.Fold().String(unaccented))
}

// NormalizeOrgName is normalizeName plus the PO-PARAM-1 legal-suffix
// strip, applied only to the trailing token: "Co" inside "Coca Co" is a
// name, "Co" at the end is a suffix.
//
// The strip never consumes the whole name: "Co" alone stays "co", because a
// company may BE its suffix and an empty key would collide with every other
// empty key.
func NormalizeOrgName(s string) string {
	fields := strings.Fields(normalizeName(strings.ReplaceAll(s, ",", " ")))
	strippedOne := false
	for len(fields) > 1 {
		last := strings.Trim(fields[len(fields)-1], ".")
		switch {
		case legalSuffixes[last]:
			strippedOne = true
		case legalConnectives[last] && strippedOne && len(fields) > 2 &&
			legalSuffixes[strings.Trim(fields[len(fields)-2], ".")]:
			// A connective BETWEEN two legal forms — the "GmbH & Co. KG"
			// case. Consumed only here, so a company whose name simply ends
			// in "and" keeps it.
		default:
			return strings.Join(fields, " ")
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
// nameScoringMaxRunes bounds what the similarity metric compares.
//
// jaro is quadratic in the longer input — measured on this code, two 60 000-rune
// names take a full second and the cost grows with the square — while
// `display_name` is `text` with no maxLength in the contract, so one create can
// hand it a megabyte. That scoring runs inside the writing transaction holding
// the organization-name lock, so an unbounded score pins a pool connection and
// every organization-name writer in the workspace behind it.
//
// The cap lives HERE and not in normalizeName, which looked like the tidier
// place and is not: normalizeName also produces exact-match and grouping keys
// (orgMatchKeys in linkedinimport.go, the promotion sweep's name buckets), and
// truncating there would make two distinct names compare EQUAL as keys past the
// bound — a match, not a capped score. Capping the metric changes only how
// similar two things are said to be, which is all this bound is entitled to do.
//
// 256 runes is far past any real company or person name. A maxLength in the
// contract is the complete answer and is raised upstream.
const nameScoringMaxRunes = 256

// boundedForScoring caps one side of a comparison. Two names that differ only
// past the bound score as identical, which for names this long is the same
// answer any metric would give.
func boundedForScoring(s string) string {
	if r := []rune(s); len(r) > nameScoringMaxRunes {
		return string(r[:nameScoringMaxRunes])
	}
	return s
}

func jaroWinkler(a, b string) float64 {
	a, b = boundedForScoring(a), boundedForScoring(b)
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
