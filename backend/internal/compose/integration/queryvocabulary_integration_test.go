// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// The query vocabulary as a client actually reaches it: over the composed
// /mcp mount, with a real passport, a real workspace and the real custom-field
// catalog behind it. The unit suite in modules/search proves the derivation;
// this proves the WIRING — that compose injects the provider, that the
// transport publishes it, and that the catalog read rides a workspace-bound
// transaction rather than an unbound one that would silently answer nothing.

import (
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/compose"
	"github.com/gradionhq/margince/backend/internal/compose/integration/apptest"
	"github.com/gradionhq/margince/backend/internal/modules/search"
)

// The published document as a CLIENT decodes it — declared here rather than
// reused from the search package, so a renamed wire member fails this test
// instead of travelling through it unnoticed.
type vocabularyDoc struct {
	Version     string                  `json:"version"`
	Targets     []vocabularyTarget      `json:"targets"`
	Unavailable []vocabularyUnavailable `json:"unavailable"`
}

type vocabularyTarget struct {
	Target    string               `json:"target"`
	Fields    []vocabularyField    `json:"fields"`
	Relations []vocabularyRelation `json:"relations"`
}

type vocabularyField struct {
	Name string   `json:"name"`
	Kind string   `json:"kind"`
	Ops  []string `json:"ops"`
}

type vocabularyRelation struct {
	Name   string `json:"name"`
	Target string `json:"target"`
	Via    string `json:"via"`
}

type vocabularyUnavailable struct {
	Op      string `json:"op"`
	Answers string `json:"answers"`
}

func TestTheComposedMCPMountPublishesTheQueryVocabulary(t *testing.T) {
	env := setupConnector(t)
	bearer := readPassport(t, env.AppEnv, "query vocabulary reader")

	// The catalogue advertises it, which is how a client finds it at all.
	listed := mcpRPC(env.AppEnv, t, bearer, `{"jsonrpc":"2.0","id":1,"method":"resources/list"}`)
	if !strings.Contains(listed, search.QuerySchemaURI) {
		t.Fatalf("resources/list does not advertise %s: %s", search.QuerySchemaURI, listed)
	}

	doc := readVocabulary(env.AppEnv, t, bearer)
	if doc.Version != search.PlanVersion {
		t.Fatalf("published vocabulary is version %q, want %q", doc.Version, search.PlanVersion)
	}

	// The derivation survives the trip: the deal record's own contract fields
	// and its derived organization hop are both there.
	deal := dealVocabulary(t, doc)
	if !slices.ContainsFunc(deal.Fields, func(f vocabularyField) bool {
		return f.Name == "amount_minor" && f.Kind == "number"
	}) {
		t.Error("the published deal vocabulary has no amount_minor number field")
	}
	if !slices.ContainsFunc(deal.Relations, func(r vocabularyRelation) bool {
		return r.Name == "organization" && r.Via == "organization_id"
	}) {
		t.Error("the published deal vocabulary has no derived organization hop")
	}

	// The declared-but-unanswerable operator arrives declared (SEARCH-AC-17).
	if !slices.ContainsFunc(doc.Unavailable, func(u vocabularyUnavailable) bool {
		return u.Op == "within_radius" && u.Answers == search.CodeDistanceRankingUnavailable
	}) {
		t.Error("within_radius is not published as unavailable, so a caller cannot tell it apart from one that works")
	}
}

// SEARCH-AC-15 against the REAL catalog: a field added through the admin
// surface is in the published vocabulary on the next read, and a retired one
// is gone — with no deploy, no cache bust, and no edit to any list.
func TestACustomFieldReachesThePublishedVocabularyWithoutADeploy(t *testing.T) {
	// The schema pool is the owner-privileged connection the add-field engine
	// runs its one ALTER TABLE on; without it the create answers 501 and this
	// test would be asserting against a surface that is not mounted.
	env := setupConnectorWith(t, compose.WithSchemaPool(SchemaPool(t)))
	bearer := readPassport(t, env.AppEnv, "custom field reader")

	before := readVocabulary(env.AppEnv, t, bearer)
	if fieldNamed(dealVocabulary(t, before), "cf_renewal_risk") {
		t.Fatal("the field under test already exists in the vocabulary")
	}

	status, created, problem := createCustomField(t, env.AppEnv, apptest.AnyMap{
		"object": "deal", "label": "Renewal risk", "type": "text", "source": "ui",
	})
	if status != http.StatusCreated {
		t.Fatalf("adding a custom field → %d %+v", status, problem)
	}
	if created.ColumnName != "cf_renewal_risk" {
		t.Fatalf("the engine named the column %q; this test asks the vocabulary for cf_renewal_risk", created.ColumnName)
	}

	added := readVocabulary(env.AppEnv, t, bearer)
	if !fieldNamed(dealVocabulary(t, added), "cf_renewal_risk") {
		t.Fatal("a custom field active in the catalog is not in the published vocabulary")
	}

	var retired customFieldWire
	if status := env.Call(t, "POST", "/v1/custom-fields/"+created.ID+"/retire", nil, nil, &retired); status != http.StatusOK {
		t.Fatalf("retiring the custom field → %d", status)
	}
	if fieldNamed(dealVocabulary(t, readVocabulary(env.AppEnv, t, bearer)), "cf_renewal_risk") {
		t.Error("a retired custom field is still published; the vocabulary is cached rather than derived")
	}
}

// A URI this surface does not serve answers the protocol's not-found, which
// is also what a document the caller cannot see answers.
func TestAnUnservedResourceURIIsRefusedOverTheTransport(t *testing.T) {
	env := setupConnector(t)
	bearer := readPassport(t, env.AppEnv, "resource prober")

	out := mcpRPC(env.AppEnv, t, bearer,
		`{"jsonrpc":"2.0","id":9,"method":"resources/read","params":{"uri":"margince://schema/secrets"}}`)
	if !strings.Contains(out, `"error"`) || !strings.Contains(out, "-32002") {
		t.Fatalf("read of an unserved URI → %s, want a resource-not-found error", out)
	}
}

// readPassport mints a read-scoped passport, which is the credential every
// exchange in this file presents. It answers the bare token rather than a
// header map: these exchanges post JSON-RPC bodies, so the header set differs
// from the REST helper's next to it.
func readPassport(t *testing.T, e *apptest.AppEnv, label string) string {
	t.Helper()
	var minted struct {
		Token string `json:"token"`
	}
	if status := e.Call(t, "POST", "/v1/passports", apptest.AnyMap{
		"label": label, "scopes": []string{"read"},
	}, nil, &minted); status != http.StatusCreated || minted.Token == "" {
		t.Fatalf("issue passport → %d", status)
	}
	return minted.Token
}

// mcpRPC posts one JSON-RPC exchange to the composed /mcp mount and returns
// the raw body — the errors this file asserts on live in the envelope, not in
// a decoded result.
func mcpRPC(e *apptest.AppEnv, t *testing.T, bearer, payload string) string {
	t.Helper()
	got := mcpRaw(e, t, http.MethodPost, "/mcp", payload, map[string]string{
		"Content-Type": "application/json", "Authorization": "Bearer " + bearer,
	})
	if got.StatusCode != http.StatusOK {
		t.Fatalf("POST /mcp → %d %s", got.StatusCode, got.Body)
	}
	return got.Body
}

// readVocabulary fetches and decodes the published document.
func readVocabulary(e *apptest.AppEnv, t *testing.T, bearer string) vocabularyDoc {
	t.Helper()
	body := mcpRPC(e, t, bearer,
		`{"jsonrpc":"2.0","id":2,"method":"resources/read","params":{"uri":"`+search.QuerySchemaURI+`"}}`)

	var envelope struct {
		Result struct {
			Contents []struct {
				Text string `json:"text"`
			} `json:"contents"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(body), &envelope); err != nil {
		t.Fatalf("resources/read body is not JSON-RPC: %v (%s)", err, body)
	}
	if len(envelope.Result.Contents) != 1 {
		t.Fatalf("resources/read returned %d content blocks: %s", len(envelope.Result.Contents), body)
	}
	var doc vocabularyDoc
	if err := json.Unmarshal([]byte(envelope.Result.Contents[0].Text), &doc); err != nil {
		t.Fatalf("the published vocabulary is not JSON: %v", err)
	}
	return doc
}

// dealVocabulary is the one target every assertion in this file reads: the
// deal record carries both halves of the derivation (contract fields and, in
// the custom-field test, a workspace column) plus a derived hop.
func dealVocabulary(t *testing.T, doc vocabularyDoc) vocabularyTarget {
	t.Helper()
	i := slices.IndexFunc(doc.Targets, func(target vocabularyTarget) bool { return target.Target == "deal" })
	if i < 0 {
		t.Fatal(`the published vocabulary has no "deal" target`)
	}
	return doc.Targets[i]
}

func fieldNamed(target vocabularyTarget, name string) bool {
	return slices.ContainsFunc(target.Fields, func(f vocabularyField) bool { return f.Name == name })
}
