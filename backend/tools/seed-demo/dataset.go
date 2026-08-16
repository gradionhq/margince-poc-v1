// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package main

// Reading the demo dataset: one accepted.json per company, exactly as the
// site reader left it after a human reviewed the extraction.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// company is one reviewed site read — the shape the dataset's accept.py
// writes. Only the parts the seeder uses are named; the file carries drop
// reports and review notes besides, which belong to the review loop.
type company struct {
	Domain  string        `json:"domain"`
	SeedURL string        `json:"seed_url"`
	Fields  []field       `json:"fields"`
	Facts   []fact        `json:"facts"`
	People  []datasetPers `json:"people"`
}

type field struct {
	Field string `json:"field"`
	Value string `json:"value"`
}

type fact struct {
	Category string `json:"category"`
	Field    string `json:"field"`
	Value    string `json:"value"`
}

// datasetPers is a person the company's own website published.
//
// PublishedEmail is almost always empty and SynthesizedEmail almost always
// is not: European company sites print names and job titles but not personal
// addresses. The two fields stay separate all the way through so nothing
// downstream can mistake an invented address for one the page carried — see
// the dataset's STATE.md for why they are invented at all, and the rule that
// nothing is ever sent to one.
type datasetPers struct {
	Name             string `json:"name"`
	Role             string `json:"role"`
	PublishedEmail   string `json:"published_email"`
	SynthesizedEmail string `json:"synthesized_email"`
	IsSynthesized    bool   `json:"email_is_synthesized"`
	SourceURL        string `json:"source_url"`
}

// email is the address to file for this person, and whether it was invented.
func (p datasetPers) email() (string, bool) {
	if p.PublishedEmail != "" {
		return p.PublishedEmail, false
	}
	return p.SynthesizedEmail, p.IsSynthesized
}

// value returns the named profile field, or "" when the read did not find it.
func (c company) value(name string) string {
	for _, f := range c.Fields {
		if f.Field == name {
			return f.Value
		}
	}
	return ""
}

// displayName is what the company is called, falling back to its domain: a
// read that found no name still describes a real company worth having.
func (c company) displayName() string {
	if name := c.value("display_name"); name != "" {
		return name
	}
	return c.Domain
}

// loadDataset reads every reviewed company, in domain order so a partial run
// with -limit is reproducible rather than filesystem-ordered.
func loadDataset(root string, limit int) ([]company, error) {
	results := filepath.Join(root, "datasets", "v1", "siteresults")
	entries, err := os.ReadDir(results)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w (is -dataset pointing at the demo dataset checkout?)", results, err)
	}

	var domains []string
	for _, entry := range entries {
		if entry.IsDir() {
			domains = append(domains, entry.Name())
		}
	}
	sort.Strings(domains)

	var out []company
	for _, domain := range domains {
		path := filepath.Join(results, domain, "accepted.json")
		// The dataset path is an operator-supplied flag on a developer tool,
		// which is the whole point: the data lives in a separate private
		// checkout whose location only the operator knows.
		raw, err := os.ReadFile(path) //nolint:gosec // G304: the dataset root is a deliberate operator-supplied flag
		if os.IsNotExist(err) {
			// Crawled but not yet reviewed. Un-accepted companies stay out of
			// the demo by design — that is what accepting is for.
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("reading %s: %w", path, err)
		}
		var c company
		if err := json.Unmarshal(raw, &c); err != nil {
			return nil, fmt.Errorf("parsing %s: %w", path, err)
		}
		if c.Domain == "" {
			c.Domain = domain
		}
		out = append(out, c)
		if limit > 0 && len(out) == limit {
			break
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no accepted companies under %s — run the dataset's accept.py first", results)
	}
	return out, nil
}

// splitName divides a printed name into the first/last pair the person API
// takes. A single-word name keeps the whole thing as the last name, which is
// what a mononym is.
func splitName(full string) (first, last string) {
	parts := strings.Fields(full)
	switch len(parts) {
	case 0:
		return "", ""
	case 1:
		return "", parts[0]
	default:
		return parts[0], parts[len(parts)-1]
	}
}
