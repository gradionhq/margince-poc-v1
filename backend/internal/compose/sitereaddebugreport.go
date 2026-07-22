// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The debug report's shape and its builders, split from the run loop
// (sitereaddebug.go): the JSON structures the `worker siteread`
// subcommand emits, the projection helpers that fill them from the
// pipeline's internals, and the debug-only wrong-company signal.

import (
	"fmt"
	"net/url"
	"strings"
	"unicode/utf8"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/modules/people"
)

// DebugCaps echoes the caps the run enforced.
type DebugCaps struct {
	MaxPages int   `json:"max_pages"`
	MaxBytes int   `json:"max_bytes"`
	WallMs   int64 `json:"wall_ms"`
}

// DebugCrawl is the crawl half of the report: what was fetched, what
// was skipped, and why the walk stopped.
type DebugCrawl struct {
	Pages         []DebugPage `json:"pages"`
	Skipped       []DebugSkip `json:"skipped"`
	StoppedReason string      `json:"stopped_reason,omitempty"`
	TotalBytes    int         `json:"total_bytes"`
	DurationMs    int64       `json:"duration_ms"`
}

// DebugPage is one fetched page.
type DebugPage struct {
	URL     string `json:"url"`
	Kind    string `json:"kind"`
	Bytes   int    `json:"bytes"`
	Runes   int    `json:"runes"`
	FetchMs int64  `json:"fetch_ms"`
	// Extracted marks whether the model lane reached this page before
	// any midway failure.
	Extracted bool `json:"extracted"`
	// Text is the page's reduced prose — only when the run asked for it
	// (SiteReadDebugOptions.IncludePageText).
	Text string `json:"text,omitempty"`
}

// DebugSkip is one recorded skip and its reason.
type DebugSkip struct {
	URL    string `json:"url"`
	Reason string `json:"reason"`
}

// DebugExtraction is the extraction half of the report: what survived
// the gates, what the merges decided, and what was dropped.
type DebugExtraction struct {
	Fields []DebugField  `json:"fields"`
	Facts  []DebugFact   `json:"facts"`
	People []DebugPerson `json:"people"`
	// LegalEntities is the gated legal-entity census — the abstention's
	// inspectable input.
	LegalEntities []DebugLegalEntity `json:"legal_entities"`
	// Dropped is every finding a gate refused, with its reason — the
	// silent-loss channel made visible for tuning.
	Dropped []DebugDrop `json:"dropped"`
}

// DebugLegalEntity is one entity a legal page names.
type DebugLegalEntity struct {
	Name              string `json:"name"`
	RegisteredAddress string `json:"registered_address,omitempty"`
	RegisterNumber    string `json:"register_number,omitempty"`
	EvidenceSnippet   string `json:"evidence_snippet,omitempty"`
	SourceURL         string `json:"source_url"`
}

// DebugDrop is one gate rejection: what the model claimed on which
// page, and why the gate refused it.
type DebugDrop struct {
	PageURL         string `json:"page_url"`
	Lane            string `json:"lane"`
	Field           string `json:"field,omitempty"`
	Value           string `json:"value,omitempty"`
	EvidenceSnippet string `json:"evidence_snippet,omitempty"`
	Reason          string `json:"reason"`
}

// DebugField is one merged company field with its evidence.
type DebugField struct {
	Field           string  `json:"field"`
	Value           string  `json:"value"`
	Confidence      float32 `json:"confidence"`
	EvidenceSnippet string  `json:"evidence_snippet"`
	SourceURL       string  `json:"source_url"`
}

// DebugFact is one merged category fact with its evidence.
type DebugFact struct {
	Category        string  `json:"category"`
	Field           string  `json:"field"`
	Value           string  `json:"value"`
	ValueKey        string  `json:"value_key,omitempty"`
	Confidence      float32 `json:"confidence"`
	EvidenceSnippet string  `json:"evidence_snippet"`
	SourceURL       string  `json:"source_url"`
}

// DebugPerson is one published person the people gate kept.
type DebugPerson struct {
	Name            string `json:"name"`
	Role            string `json:"role"`
	PublishedEmail  string `json:"published_email,omitempty"`
	LinkedinURL     string `json:"linkedin_url,omitempty"`
	EvidenceSnippet string `json:"evidence_snippet"`
	SourceURL       string `json:"source_url"`
}

// DebugModelCall is one model call's telemetry, in call order.
type DebugModelCall struct {
	PageURL      string `json:"page_url"`
	Lane         string `json:"lane"`
	LatencyMs    int64  `json:"latency_ms"`
	InputTokens  int    `json:"input_tokens"`
	OutputTokens int    `json:"output_tokens"`
	Error        string `json:"error,omitempty"`
}

// debugCrawl projects one crawl onto the report shape. extracted names
// the pages whose corpus chunk completed before any midway failure.
func debugCrawl(crawl siteCrawl, extracted []crawlPage, includeText bool, durationMs int64) DebugCrawl {
	out := DebugCrawl{TotalBytes: crawl.TotalBytes, DurationMs: durationMs}
	if crawl.Stopped != nil {
		out.StoppedReason = string(*crawl.Stopped)
	}
	for _, s := range crawl.Skipped {
		out.Skipped = append(out.Skipped, DebugSkip{URL: s.URL, Reason: string(s.Reason)})
	}
	wasExtracted := map[string]bool{}
	for _, page := range extracted {
		wasExtracted[page.URL] = true
	}
	for _, page := range crawl.Pages {
		entry := DebugPage{
			URL:       page.URL,
			Kind:      string(page.Kind),
			Bytes:     page.Bytes,
			Runes:     utf8.RuneCountInString(page.Text),
			FetchMs:   page.FetchDur.Milliseconds(),
			Extracted: wasExtracted[page.URL],
		}
		if includeText {
			entry.Text = page.Text
		}
		out.Pages = append(out.Pages, entry)
	}
	return out
}

// debugLegalEntities projects the gated census for the report.
func debugLegalEntities(entities []corpusLegalEntity) []DebugLegalEntity {
	out := make([]DebugLegalEntity, 0, len(entities))
	for _, e := range entities {
		out = append(out, DebugLegalEntity(e))
	}
	return out
}

// wrongCompanySignal flags a merged legal_name that shares not a single
// token with the seed's host or the extracted display name — the shape
// a cross-entity Impressum grab takes. Advisory only: plenty of real
// companies trade under a name their legal entity does not carry.
func wrongCompanySignal(seedURL string, merged []evidencedField) string {
	var legalName, displayName string
	for _, f := range merged {
		switch f.Field {
		case string(crmcontracts.ColdStartFieldFieldLegalName):
			legalName = f.Value
		case string(crmcontracts.ColdStartFieldFieldDisplayName):
			displayName = f.Value
		}
	}
	if legalName == "" {
		return ""
	}
	parsed, err := url.Parse(seedURL)
	if err != nil {
		return ""
	}
	reference := normalizeEvidence(parsed.Host + " " + displayName)
	for _, token := range strings.Fields(normalizeEvidence(legalName)) {
		if legalEntityNoise[token] || len(token) < 3 {
			continue
		}
		if strings.Contains(reference, token) {
			return ""
		}
	}
	return fmt.Sprintf("possible wrong company: legal_name %q shares no token with the domain %s or the display name %q",
		legalName, parsed.Host, displayName)
}

// legalEntityNoise: legal-form suffixes that match nothing about
// identity — every GmbH overlaps every other GmbH.
var legalEntityNoise = map[string]bool{
	"gmbh": true, "mbh": true, "ag": true, "ug": true, "kg": true, "ohg": true, "gbr": true,
	"co.": true, "co": true, "se": true, "ltd": true, "ltd.": true, "inc": true, "inc.": true,
	"llc": true, "pte": true, "pte.": true, "corp": true, "corp.": true, "bv": true, "b.v.": true,
	"sa": true, "s.a.": true, "sarl": true, "srl": true, "&": true, "und": true, "and": true,
}

// debugProposal is byte-for-byte what siteDeepReadWorker.stage would
// marshal, minus the identities a DB-less run does not have (zero
// organization and read ids). Nil when nothing survived — the staged
// path stages nothing then, too.
func debugProposal(seedURL string, mergedFields []evidencedField, mergedFacts []people.DeepReadFact) *people.DeepReadProposal {
	if len(mergedFields)+len(mergedFacts) == 0 {
		return nil
	}
	return &people.DeepReadProposal{
		SourceURL: seedURL,
		Fields:    deepReadFields(mergedFields),
		Facts:     mergedFacts,
	}
}

func debugFields(fields []evidencedField) []DebugField {
	out := make([]DebugField, 0, len(fields))
	for _, f := range fields {
		out = append(out, DebugField{
			Field: f.Field, Value: f.Value, Confidence: f.Confidence,
			EvidenceSnippet: f.EvidenceSnippet, SourceURL: f.SourceURL,
		})
	}
	return out
}

func debugFacts(facts []people.DeepReadFact) []DebugFact {
	out := make([]DebugFact, 0, len(facts))
	for _, f := range facts {
		out = append(out, DebugFact{
			Category: f.Category, Field: f.Field, Value: f.Value, ValueKey: f.ValueKey,
			Confidence: f.Confidence, EvidenceSnippet: f.EvidenceSnippet, SourceURL: f.SourceURL,
		})
	}
	return out
}

func debugPeople(persons []sitePerson) []DebugPerson {
	out := make([]DebugPerson, 0, len(persons))
	for _, p := range persons {
		out = append(out, DebugPerson{
			Name: p.Name, Role: p.Role, PublishedEmail: p.PublishedEmail,
			LinkedinURL: p.LinkedinURL, EvidenceSnippet: p.EvidenceSnippet, SourceURL: p.SourceURL,
		})
	}
	return out
}
