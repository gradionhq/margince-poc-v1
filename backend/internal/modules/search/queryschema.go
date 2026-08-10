// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package search

// `margince://schema/query` — the vocabulary, published rather than implied
// (SEARCH-PARAM-7). A client that has to guess what it may ask will guess
// wrong, and every wrong guess is a refusal that reads like a bug.
//
// The document is composed PER CALLER from the same resolver the validator
// uses, so what it advertises and what the validator admits are one
// computation rather than two that can drift. That also means it is never a
// discovery channel: it lists exactly the record types this principal can
// already read.

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/mcp"
)

// QuerySchemaURI is the resource's stable identity.
const QuerySchemaURI = "margince://schema/query"

// QuerySchemaResource publishes the query vocabulary.
type QuerySchemaResource struct {
	vocab *VocabularyResolver
}

// NewQuerySchemaResource builds the provider over a vocabulary resolver — the
// same one the validator holds, so the published document and the admitted
// plan cannot disagree.
func NewQuerySchemaResource(vocab *VocabularyResolver) *QuerySchemaResource {
	return &QuerySchemaResource{vocab: vocab}
}

// Resources advertises the one document this module publishes.
func (r *QuerySchemaResource) Resources(context.Context) []mcp.Resource {
	return []mcp.Resource{{
		URI:   QuerySchemaURI,
		Name:  "query_vocabulary",
		Title: "Workspace query vocabulary",
		// A vocabulary is a read of what the workspace holds — it names the
		// record types and the workspace's own custom columns — so it is
		// governed by the same scope a record read is.
		RequiredScope: principal.ScopeRead,
		MIMEType:      "application/json",
		// Says what it HOLDS, not when to fetch it. This sentence rides every
		// prompt that lists resources, and an instruction there is one a model
		// acts on: a binding told to read first spent turns fetching vocabularies
		// on goals that needed none.
		Description: "Everything a query plan may say, for you: the record types you can ask " +
			"about, the fields you can name on each, the operators each field admits, and the " +
			"single relationship hop a plan may take. A plan naming anything outside it is " +
			"refused rather than approximated.",
	}}
}

// ReadResource composes the document. An unknown URI answers ErrNotFound,
// matching how every other read on this surface treats something the caller
// cannot see.
func (r *QuerySchemaResource) ReadResource(ctx context.Context, uri string) (mcp.ResourceContents, error) {
	if uri != QuerySchemaURI {
		return mcp.ResourceContents{}, fmt.Errorf("search: resource %q: %w", uri, apperrors.ErrNotFound)
	}
	vocab, err := r.vocab.Resolve(ctx)
	if err != nil {
		return mcp.ResourceContents{}, err
	}
	body, err := json.Marshal(querySchemaDocument(vocab))
	if err != nil {
		return mcp.ResourceContents{}, fmt.Errorf("search: rendering the query vocabulary: %w", err)
	}
	return mcp.ResourceContents{URI: uri, MIMEType: "application/json", Text: string(body)}, nil
}

// querySchemaDoc is the published shape. It is a hand-written wire type
// rather than the internal Vocabulary because the two answer different
// questions: the internal one carries what the validator needs to check a
// plan, this one carries what a caller needs to write one.
type querySchemaDoc struct {
	Version string `json:"version"`
	// Grammar states the three things v1 admits, in the caller's terms, so
	// the shape of a plan does not have to be inferred from the field lists.
	Grammar querySchemaGrammar  `json:"grammar"`
	Targets []querySchemaTarget `json:"targets"`
	// Unavailable declares the operators this deployment publishes but
	// cannot answer, with the code they answer instead. Declaring them is
	// the honest option: omitting `within_radius` sends a caller to a text
	// match on a city name, which quietly returns the wrong answer.
	Unavailable []querySchemaUnavailable `json:"unavailable"`
}

type querySchemaGrammar struct {
	Predicates     string `json:"predicates"`
	SemanticTarget string `json:"semantic_target"`
	Traversal      string `json:"traversal"`
	Limit          string `json:"limit"`
	Refusal        string `json:"refusal"`
}

type querySchemaTarget struct {
	Target    string                `json:"target"`
	Fields    []querySchemaField    `json:"fields"`
	Relations []querySchemaRelation `json:"relations"`
}

type querySchemaField struct {
	Name string   `json:"name"`
	Kind string   `json:"kind"`
	Ops  []string `json:"ops"`
}

// querySchemaRelation is Relation's wire form, and a direct conversion of it
// — a hop has nothing to hide from a caller. The conversion is deliberate:
// adding a member to Relation without deciding whether callers should see it
// stops compiling here.
type querySchemaRelation struct {
	Name   string `json:"name"`
	Target string `json:"target"`
	Via    string `json:"via"`
}

type querySchemaUnavailable struct {
	Op      string `json:"op"`
	Answers string `json:"answers"`
	Because string `json:"because"`
}

// querySchemaDocument renders one caller's resolved vocabulary.
func querySchemaDocument(vocab Vocabulary) querySchemaDoc {
	doc := querySchemaDoc{
		Version: vocab.Version,
		// Never nil: a caller who may read nothing gets an empty list, the
		// same wire shape everyone else gets, rather than a JSON null their
		// client has to special-case.
		Targets: []querySchemaTarget{},
		Grammar: querySchemaGrammar{
			Predicates: `{"field": <name below>, "op": <op the field admits>, "value": <operand>} — ` +
				`or "values": [...] for "in". Clauses are combined with AND.`,
			SemanticTarget: `"similar_to": <free text> — one similarity clause, ranked by the hybrid retriever.`,
			Traversal: `"traverse": {"relation": <relation below>, "where": [...]} — exactly one hop. ` +
				`A second hop is refused; ask it as its own query.`,
			Limit: fmt.Sprintf("%q: 1..%d; omit for the default page.", "limit", maxPlanLimit),
			Refusal: "Anything outside this vocabulary is refused with a typed clarification naming " +
				"what was not understood. Nothing is coerced, and no plan is narrowed to the part " +
				"that was recognised.",
		},
		Unavailable: []querySchemaUnavailable{{
			Op:      OpWithinRadius,
			Answers: CodeDistanceRankingUnavailable,
			Because: "records carry no normalized coordinates yet; ask for a city or region as an exact predicate instead.",
		}},
	}
	for _, target := range vocab.Targets {
		doc.Targets = append(doc.Targets, querySchemaTargetOf(target))
	}
	return doc
}

func querySchemaTargetOf(target TargetVocabulary) querySchemaTarget {
	out := querySchemaTarget{
		Target:    target.Target,
		Fields:    make([]querySchemaField, 0, len(target.Fields)),
		Relations: make([]querySchemaRelation, 0, len(target.Relations)),
	}
	for _, f := range target.Fields {
		out.Fields = append(out.Fields, querySchemaField{Name: f.Name, Kind: string(f.Kind), Ops: f.Ops})
	}
	for _, r := range target.Relations {
		out.Relations = append(out.Relations, querySchemaRelation(r))
	}
	return out
}

var _ mcp.ResourceProvider = (*QuerySchemaResource)(nil)
