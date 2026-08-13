// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package webread

// Honoring the rate a site publishes.
//
// The pacer's own floor is a burst budget — eight requests in flight, 25ms
// apart — which is far more aggressive than a site asking for seconds between
// them. A crawler that ignores Crawl-delay while claiming to respect robots.txt
// is walking past the one directive that governs rate, and on a site behind bot
// protection it is also how the read gets refused.

import (
	"testing"
	"time"
)

func TestParseRobotsReadsCrawlDelay(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		body string
		want time.Duration
	}{
		{
			name: "a whole number of seconds",
			body: "User-agent: *\nCrawl-delay: 10\n",
			want: 10 * time.Second,
		},
		{
			name: "a decimal value",
			body: "User-agent: *\nCrawl-delay: 0.5\n",
			want: 500 * time.Millisecond,
		},
		{
			name: "a group naming this bot outranks the wildcard",
			body: "User-agent: *\nCrawl-delay: 1\n\nUser-agent: " + robotsAgentProduct + "\nCrawl-delay: 4\n",
			want: 4 * time.Second,
		},
		{
			// Same reasoning as a second Disallow: combining groups must not let
			// a later one buy back a faster rate than an earlier one asked for.
			name: "the longest delay across matching groups wins",
			body: "User-agent: " + robotsAgentProduct + "\nCrawl-delay: 2\n\nUser-agent: " + robotsAgentProduct + "\nCrawl-delay: 7\n",
			want: 7 * time.Second,
		},
		{
			name: "an absurd value is capped rather than obeyed",
			body: "User-agent: *\nCrawl-delay: 86400\n",
			want: maxHonoredCrawlDelay,
		},
		{
			name: "a value we cannot read is no delay, not an invented one",
			body: "User-agent: *\nCrawl-delay: soon\n",
			want: 0,
		},
		{
			name: "a negative value is not a licence to go faster",
			body: "User-agent: *\nCrawl-delay: -5\n",
			want: 0,
		},
		{
			name: "no directive is no delay",
			body: "User-agent: *\nDisallow: /private\n",
			want: 0,
		},
		{
			name: "a delay before any user-agent line addresses nobody",
			body: "Crawl-delay: 9\nUser-agent: *\nDisallow: /x\n",
			want: 0,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := parseRobots(tc.body).crawlDelay; got != tc.want {
				t.Errorf("crawlDelay = %v, want %v", got, tc.want)
			}
		})
	}
}

// A Crawl-delay line must not disturb the rules around it: the directive is
// unknown to RFC 9309, and mishandling it could silently drop a Disallow.
func TestCrawlDelayDoesNotDisturbTheRules(t *testing.T) {
	t.Parallel()
	policy := parseRobots("User-agent: *\nCrawl-delay: 3\nDisallow: /private\nAllow: /private/ok\n")
	if policy.crawlDelay != 3*time.Second {
		t.Errorf("crawlDelay = %v, want 3s", policy.crawlDelay)
	}
	if policy.allows("/private/secret") {
		t.Error("/private/secret is allowed, but the group disallows it")
	}
	if !policy.allows("/private/ok") {
		t.Error("/private/ok is refused, but a longer Allow permits it")
	}
	if !policy.allows("/public") {
		t.Error("/public is refused, but no rule covers it")
	}
}

func TestPacerSlowToOnlyEverSlows(t *testing.T) {
	t.Parallel()
	pacer := NewPacer()
	if pacer.interval != pacerMinInterval {
		t.Fatalf("a fresh pacer holds %v, want the burst budget %v", pacer.interval, pacerMinInterval)
	}
	pacer.SlowTo(2 * time.Second)
	if pacer.interval != 2*time.Second {
		t.Errorf("interval = %v after asking for 2s, want 2s", pacer.interval)
	}
	// A site asking for less than the crawl already allows is not granting
	// permission to speed up, and neither is a second, smaller ask.
	pacer.SlowTo(time.Millisecond)
	if pacer.interval != 2*time.Second {
		t.Errorf("interval = %v after a smaller ask, want it held at 2s", pacer.interval)
	}
	pacer.SlowTo(0)
	if pacer.interval != 2*time.Second {
		t.Errorf("interval = %v after a zero ask, want it held at 2s", pacer.interval)
	}
}
