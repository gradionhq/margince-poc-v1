// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package webread

import (
	"strconv"
	"strings"
	"time"
)

// robotsPolicy is the REP (RFC 9309) rule set this bot obeys: the union of
// every group addressed to it (by product name, falling back to the union of
// the * groups), longest-match wins, Allow beating Disallow at equal length,
// with `*` (any run) and `$` (end anchor) interpreted as REP operators.
// Crawl-delay is honored: the pacer's own floor is a burst budget (8 in flight,
// 25ms apart), which is far MORE aggressive than a site asking for seconds
// between requests, so ignoring the directive would mean claiming to respect
// robots while walking past the one line that governs rate. Sitemap and unknown
// directives remain out of scope — this type answers "may I read this path",
// and a sitemap is a discovery hint, not a permission.
type robotsPolicy struct {
	rules []robotsRule
	// crawlDelay is the pause the site asks for between requests, zero when it
	// asks for none. Non-standard (it predates RFC 9309 and is absent from it)
	// but widely published and widely honored, so a crawler that ignores it is
	// the impolite one regardless of what the RFC enumerates.
	crawlDelay time.Duration
}

type robotsRule struct {
	path  string
	allow bool
}

// robotsGroup is one User-agent block and its rules.
type robotsGroup struct {
	crawlDelay time.Duration
	agents     []string
	rules      []robotsRule
}

// allows reports whether the policy permits fetching target — the escaped
// path INCLUDING the query, because REP rules may match on `?`. An empty
// policy (no robots.txt, or no group addressing us) allows everything.
func (p robotsPolicy) allows(target string) bool {
	bestLen := -1
	allowed := true
	for _, r := range p.rules {
		if !ruleMatches(r.path, target) {
			continue
		}
		// The longer PATTERN wins (RFC 9309 uses pattern length as
		// specificity); at equal length Allow wins — the strict > on a later
		// Disallow of the same length preserves that because the earlier
		// Allow already holds the slot.
		if len(r.path) > bestLen || (len(r.path) == bestLen && r.allow && !allowed) {
			bestLen = len(r.path)
			allowed = r.allow
		}
	}
	return allowed
}

// ruleMatches interprets a REP path pattern against the target: literal
// segments between `*` wildcards must appear in order, the first anchored at
// the start, and a trailing `$` anchoring the last to the end. A literal
// prefix rule degenerates to strings.HasPrefix.
func ruleMatches(pattern, target string) bool {
	anchored := strings.HasSuffix(pattern, "$")
	if anchored {
		pattern = strings.TrimSuffix(pattern, "$")
	}
	segments := strings.Split(pattern, "*")

	if anchored {
		// The LAST literal segment must end the target (a pattern ending in
		// `*$` — empty last segment — matches any tail). The remaining
		// segments then match inside what precedes that tail, so a first-
		// occurrence scan cannot steal bytes the anchor needs.
		last := segments[len(segments)-1]
		if !strings.HasSuffix(target, last) {
			return false
		}
		if len(segments) == 1 {
			// No `*` at all: the whole pattern must BE the whole target.
			return target == last
		}
		return segmentsMatch(segments[:len(segments)-1], target[:len(target)-len(last)])
	}
	return segmentsMatch(segments, target)
}

// segmentsMatch scans the `*`-separated literal segments left to right, the
// first anchored at the start, each later one at its first occurrence after
// the previous — first-occurrence is safe here because every later segment
// only needs SOME occurrence to its right.
func segmentsMatch(segments []string, target string) bool {
	if !strings.HasPrefix(target, segments[0]) {
		return false
	}
	pos := len(segments[0])
	for _, seg := range segments[1:] {
		if seg == "" {
			continue // consecutive or trailing * — matches any run, incl. empty
		}
		idx := strings.Index(target[pos:], seg)
		if idx < 0 {
			return false
		}
		pos += idx + len(seg)
	}
	return true
}

// parseRobots reads the robots.txt body and keeps the most specific group
// addressing this bot: a group naming our product token beats the * group;
// with neither, everything is allowed. Group structure per RFC 9309: one or
// more User-agent lines, then the rules until the next User-agent block.
func parseRobots(body string) robotsPolicy {
	var groups []robotsGroup
	var current *robotsGroup
	inAgentRun := false

	for _, raw := range strings.Split(body, "\n") {
		line := strings.TrimSpace(raw)
		if i := strings.IndexByte(line, '#'); i >= 0 {
			line = strings.TrimSpace(line[:i])
		}
		if line == "" {
			continue
		}
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		key = strings.ToLower(strings.TrimSpace(key))
		value = strings.TrimSpace(value)

		switch key {
		case "user-agent":
			// Consecutive User-agent lines address ONE group; a User-agent
			// after rules starts the next group.
			if !inAgentRun {
				groups = append(groups, robotsGroup{})
				current = &groups[len(groups)-1]
				inAgentRun = true
			}
			current.agents = append(current.agents, strings.ToLower(value))
		case "allow", "disallow":
			inAgentRun = false
			if current == nil {
				continue // rules before any User-agent line address nobody
			}
			if value == "" {
				// "Disallow:" (empty) means allow-all — representable as an
				// empty rule set, so record nothing.
				continue
			}
			current.rules = append(current.rules, robotsRule{path: value, allow: key == "allow"})
		case "crawl-delay":
			inAgentRun = false
			if current == nil {
				continue // a delay before any User-agent line addresses nobody
			}
			current.crawlDelay = parseCrawlDelay(value)
		default:
			// Sitemap and unknown directives: not this policy's job.
			inAgentRun = false
		}
	}

	return selectPolicy(groups)
}

// selectPolicy builds the rule set this bot obeys: the UNION of every group
// naming its product (RFC 9309 §2.2.1 — matching groups combine, so a later
// Disallow in a second group addressed to us cannot be bypassed by returning
// only the first), falling back to the union of the * groups, and with
// neither, everything is allowed.
func selectPolicy(groups []robotsGroup) robotsPolicy {
	var named, wildcard []robotsRule
	var namedDelay, wildcardDelay time.Duration
	// Whether a group ADDRESSED us at all, which is not the same as whether it
	// carried rules: a group whose only directive is Crawl-delay still speaks
	// to this bot, and reading its presence off a nil rule slice would discard
	// the rate it asked for.
	var addressed bool
	for i := range groups {
		namesUs, isWildcard := false, false
		for _, agent := range groups[i].agents {
			namesUs = namesUs || strings.Contains(agent, robotsAgentProduct)
			isWildcard = isWildcard || agent == "*"
		}
		// A group listing both our product and * addresses us by name — the
		// whole agent list decides, not whichever line happens to come first.
		switch {
		case namesUs:
			addressed = true
			named = append(named, groups[i].rules...)
			// The LONGEST delay across matching groups wins: combining groups
			// must not let a second one buy a faster rate than the first asked
			// for, the same way a second Disallow cannot be bypassed.
			namedDelay = max(namedDelay, groups[i].crawlDelay)
		case isWildcard:
			wildcard = append(wildcard, groups[i].rules...)
			wildcardDelay = max(wildcardDelay, groups[i].crawlDelay)
		}
	}
	if addressed {
		return robotsPolicy{rules: named, crawlDelay: namedDelay}
	}
	return robotsPolicy{rules: wildcard, crawlDelay: wildcardDelay}
}

// maxHonoredCrawlDelay bounds what a site can ask for. A crawl runs under a wall
// clock, so an unbounded delay would not make the read polite — it would make it
// impossible, and the read would end as a timeout that reads like the site was
// broken. Past this the delay is capped and the crawl reads fewer pages.
const maxHonoredCrawlDelay = 10 * time.Second

// parseCrawlDelay reads the directive's value: seconds, integer or decimal.
// Anything unparseable, negative, or absent is no delay — a value we cannot
// read is not a licence to invent one, in either direction.
func parseCrawlDelay(value string) time.Duration {
	seconds, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
	if err != nil || seconds <= 0 {
		return 0
	}
	delay := time.Duration(seconds * float64(time.Second))
	return min(delay, maxHonoredCrawlDelay)
}
