// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// Package persondata is the seam licensed person-data providers plug into
// (ADR-0096 Decision 4).
//
// MARGINCE NEVER SCRAPES A PERSON. There is no GDPR-defensible way for this
// product to crawl the public web about a natural person on its own authority,
// so first-party scraping of people is out permanently — and the crawl
// machinery that reads COMPANY sites deliberately does not extend here. What a
// provider brings that we cannot is its own lawful basis and its own contracts;
// that is the whole reason this is a port and not an implementation.
//
// The surface ships with NO provider registered, and that is a named state
// rather than a defect: the drawer renders "no data provider yet connected",
// the run endpoint answers the same, and nothing pretends to be searching.
// Connecting a provider later is a provider implementation, not a change to
// any of the guarantees below.
//
// Four guarantees bind every provider's consumers, unchanged by which provider
// is plugged in:
//
//	Every claim carries citations. A claim whose source cannot be opened is
//	dropped, never shown — the reader must be able to check it.
//
//	Nothing is written before an explicit human save. A run STAGES; the
//	record changes only when somebody reviews and accepts.
//
//	Correct and dismiss are available on every claim, and a human's verdict
//	is final against the provider.
//
//	Art. 50 disclosure rides the surface: the reader is told this is
//	AI-assisted and reads from public sources.
package persondata

import (
	"context"
	"errors"
	"time"
)

// ErrNoProvider is the honest empty state, returned when no provider is
// registered. It is a fact about this installation's configuration, not a
// failure of the request — a surface renders it as "not connected yet" and
// offers no retry, because retrying changes nothing.
var ErrNoProvider = errors.New("persondata: no person-data provider is connected")

// Provider is one licensed source of public information about a person.
type Provider interface {
	// Name identifies the provider to a reader, because a claim's
	// trustworthiness depends on who said it.
	Name() string

	// Research returns what the provider can say about this subject, with the
	// citations that make each claim checkable. It performs no writes: the
	// caller stages the result and a human decides what lands.
	Research(ctx context.Context, subject Subject) (Result, error)
}

// Subject is what we may tell a provider about the person we are asking about.
//
// Deliberately narrow. A provider is sent the minimum that identifies a
// business contact — not our correspondence, not our deal, not what they said
// to us. Sending relationship context to a third party to get public facts back
// would be the egress this port exists to bound.
type Subject struct {
	FullName string
	// Employer and Title disambiguate a common name. Both are optional; a
	// provider that needs more than this is asking for more than we send.
	Employer string
	Title    string
}

// Result is one research run, staged.
type Result struct {
	Claims []Claim
	// SourcesRead is how many documents the provider consulted, which is what
	// lets the drawer say "6 sources read · 12 cited claims" honestly — the two
	// numbers are different questions and a surface that showed one as both
	// would overstate the work.
	SourcesRead int
}

// Claim is one statement about the person, with what it was read from.
type Claim struct {
	// Body is the claim in plain words.
	Body string
	// Confidence lets a reader weigh a claim the provider is unsure of. A
	// provider that cannot express confidence returns ConfidenceUnstated
	// rather than a number it invented.
	Confidence Confidence
	// Sources are non-empty by contract: a claim with no citation is dropped
	// by the consumer, so a provider returning one has only wasted a row.
	Sources []Source
}

// Confidence is how sure the provider is.
type Confidence string

const (
	// ConfidenceHigh is a claim the provider stands behind.
	ConfidenceHigh Confidence = "high"
	// ConfidenceMedium is a claim worth phrasing as a question.
	ConfidenceMedium Confidence = "medium"
	// ConfidenceUnstated is what a provider returns when it has no basis for
	// a confidence at all — distinct from low, which is a judgement.
	ConfidenceUnstated Confidence = "unstated"
)

// Source is one document a claim was read from.
type Source struct {
	// Label names the source the way a reader would ("Company site", "Public
	// bio"), because a bare URL tells them nothing about whether to trust it.
	Label string
	// URL is where to check it. A source the reader cannot open is not
	// evidence, so a provider that cannot supply one supplies no claim.
	URL string
	// Quote is the passage the claim was read from, verbatim. It is what makes
	// "check it" a real action rather than an invitation to read a whole page.
	Quote  string
	ReadAt time.Time
}

// Registry holds the configured provider, or none.
//
// A struct rather than a bare interface so "not connected" is representable
// without a nil check at every call site — a nil interface is exactly the shape
// that turns a configuration fact into a panic.
type Registry struct {
	provider Provider
}

// NewRegistry binds the configured provider. Passing nil is the supported
// no-provider configuration, not a defect.
func NewRegistry(provider Provider) *Registry {
	return &Registry{provider: provider}
}

// Connected answers whether research can run at all, so a surface can render
// its honest state without provoking an error to find out.
func (r *Registry) Connected() bool {
	return r != nil && r.provider != nil
}

// Research runs the configured provider, or reports that there is none.
func (r *Registry) Research(ctx context.Context, subject Subject) (Result, error) {
	if !r.Connected() {
		return Result{}, ErrNoProvider
	}
	return r.provider.Research(ctx, subject)
}

// ProviderName names the connected provider for the disclosure line, or is
// empty when none is connected.
func (r *Registry) ProviderName() string {
	if !r.Connected() {
		return ""
	}
	return r.provider.Name()
}
