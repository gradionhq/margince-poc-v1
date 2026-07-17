// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package webread

import "strings"

// robotsPolicy is the subset of REP (RFC 9309) this bot needs: the rule group
// addressed to it (by product token, falling back to *), longest-match wins,
// Allow beating Disallow at equal length. Crawl-delay/sitemap directives are
// ignored here — pacing is the crawler's own, stricter policy.
type robotsPolicy struct {
	rules []robotsRule
}

type robotsRule struct {
	path  string
	allow bool
}

// robotsGroup is one User-agent block and its rules.
type robotsGroup struct {
	agents []string
	rules  []robotsRule
}

// allows reports whether the policy permits fetching path. An empty policy
// (no robots.txt, or no group addressing us) allows everything.
func (p robotsPolicy) allows(path string) bool {
	bestLen := -1
	allowed := true
	for _, r := range p.rules {
		if !strings.HasPrefix(path, r.path) {
			continue
		}
		// Longest match wins; at equal length Allow wins (the REP tiebreak),
		// which the strict > on a later Disallow of the same length preserves
		// because the earlier Allow already holds the slot.
		if len(r.path) > bestLen || (len(r.path) == bestLen && r.allow && !allowed) {
			bestLen = len(r.path)
			allowed = r.allow
		}
	}
	return allowed
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
		default:
			// Sitemap, Crawl-delay, unknown directives: not this policy's job.
			inAgentRun = false
		}
	}

	return selectPolicy(groups)
}

// selectPolicy picks the group this bot obeys: the one naming its product
// wins over *, and with neither, everything is allowed.
func selectPolicy(groups []robotsGroup) robotsPolicy {
	var wildcard *robotsGroup
	for i := range groups {
		for _, agent := range groups[i].agents {
			if strings.Contains(agent, robotsAgentProduct) {
				return robotsPolicy{rules: groups[i].rules}
			}
			if agent == "*" && wildcard == nil {
				wildcard = &groups[i]
			}
		}
	}
	if wildcard != nil {
		return robotsPolicy{rules: wildcard.rules}
	}
	return robotsPolicy{}
}
