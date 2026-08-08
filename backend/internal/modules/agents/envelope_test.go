// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package agents

// What the envelope claims, held against what the surface can actually know.
//
// Three of these are the ratified acceptance criteria in unit form — every
// registered tool advertises all six fields (AC-MCP-7's declaration half, its
// invocation half is the integration lane's), a bounded caller's read says so
// without saying how much (AC-MCP-8), and no result carries a v2 field
// (AC-MCP-9). The rest hold the aggregation rules, which are where an envelope
// stops being descriptive if they are wrong in the flattering direction.

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/authz"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
	"github.com/gradionhq/margince/backend/internal/shared/ports/mcp"
)

// sealedEnvelope decodes one result the way a client reads it.
func sealedEnvelope(t *testing.T, sealed json.RawMessage) Envelope {
	t.Helper()
	var env Envelope
	if err := json.Unmarshal(sealed, &env); err != nil {
		t.Fatalf("result is not an envelope: %v (%s)", err, sealed)
	}
	return env
}

// payloadOf is the tool's own answer, out of the envelope that carries it —
// what a test asserting on a handler's result wants, unchanged by this change.
func payloadOf(t *testing.T, sealed json.RawMessage) json.RawMessage {
	t.Helper()
	return sealedEnvelope(t, sealed).Data
}

// invokeSealed runs one tool through the real registry, so what comes back is
// what a client would be handed rather than what a handler returned.
//
// The AUTHORITY decides how much of the workspace the caller sees, not the
// context: the gate re-derives the granting human's RBAC on every call and
// overwrites the principal's stamped copy with it, which is the whole point of
// re-deriving. A test that set the row scope on the context would be asserting
// against a value the gate throws away.
func invokeSealed(ctx context.Context, t *testing.T, authority authz.Resolver, tool mcp.Tool) json.RawMessage {
	t.Helper()
	r := NewRegistry(nil, auth.NewGate(authority))
	r.Register(tool)
	// No arguments: every tool here answers from what it was built with, so the
	// call carries nothing the envelope could be read off instead of the record.
	out, err := r.Invoke(ctx, tool.Spec().Name, json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("%s: %v", tool.Spec().Name, err)
	}
	return out
}

// unboundedAuthority grants row_scope=all — the caller who sees every row, and
// therefore the one whose empty answer really does mean "nothing exists".
type unboundedAuthority struct{ fullSeatAuthority }

func (unboundedAuthority) EffectiveRBAC(context.Context, ids.UUID, ids.UUID) (authz.RBAC, error) {
	return authz.RBAC{Permissions: principal.Permissions{RowScope: principal.RowScopeAll}}, nil
}

// recordTool answers with the records it was built with, through the same
// newWireRecord every real read rides — so what it proves about the envelope is
// what the surface does, not what this test arranges.
type recordTool struct {
	spec    mcp.ToolSpec
	records []datasource.Record
}

func (r recordTool) Spec() mcp.ToolSpec { return r.spec }

func (r recordTool) Handle(ctx context.Context, _ json.RawMessage) (json.RawMessage, error) {
	return json.Marshal(searchResult(ctx, datasource.SearchResult{Records: r.records}))
}

// derivedTool answers the way an aggregate does: from records it read and does
// not name.
type derivedTool struct {
	spec         mcp.ToolSpec
	alsoNoteZero bool
}

func (d derivedTool) Spec() mcp.ToolSpec { return d.spec }

func (d derivedTool) Handle(ctx context.Context, _ json.RawMessage) (json.RawMessage, error) {
	noteDerivedContent(ctx)
	if d.alsoNoteZero {
		noteEvidence(ctx, datasource.EntityDeal, ids.UUID{})
		noteEvidence(ctx, datasource.EntityDeal, ids.NewV7())
	}
	return json.RawMessage(`{}`), nil
}

func readToolOver(records ...datasource.Record) recordTool {
	spec := objectSpec("read_probe", principal.ScopeRead)
	spec.OutputSchema = schemaFor[SearchRecordsResult]()
	return recordTool{spec: spec, records: records}
}

// recordAt is a row a person typed, which is the only provenance that earns T1.
func recordAt(entity datasource.EntityType, syncedAt time.Time, authoritative bool) datasource.Record {
	return recordWrittenBy(entity, syncedAt, authoritative, "human:"+ids.NewV7().String())
}

func recordWrittenBy(entity datasource.EntityType, syncedAt time.Time, authoritative bool, capturedBy string) datasource.Record {
	return datasource.Record{
		Ref:       datasource.EntityRef{Type: entity, ID: ids.NewV7()},
		Fields:    json.RawMessage(`{"source":"ui:person-form","captured_by":"` + capturedBy + `"}`),
		Freshness: datasource.FreshnessInfo{LastSyncedAt: syncedAt, Authoritative: authoritative},
	}
}

// AC-MCP-7, the declaration half: every tool the product ships advertises the
// six fields. The invocation half — that a real handler over a real database
// populates them — is the integration lane's, because only a real call can
// prove it.
func TestEveryToolAdvertisesTheWholeEnvelope(t *testing.T) {
	for _, spec := range fullRegistry(t).Specs() {
		var declared struct {
			Properties map[string]json.RawMessage `json:"properties"`
			Required   []string                   `json:"required"`
		}
		if err := json.Unmarshal(spec.OutputSchema, &declared); err != nil {
			t.Fatalf("%s: output schema is unreadable: %v", spec.Name, err)
		}
		for _, field := range []string{
			"schema_version", "trace_id", "freshness", "trust", "evidence", "warnings", "data",
		} {
			if _, ok := declared.Properties[field]; !ok {
				t.Errorf("%s advertises no %q — a client cannot read what the schema does not name", spec.Name, field)
			}
			if !slicesContains(declared.Required, field) {
				t.Errorf("%s declares %q optional, so a client must branch on its absence", spec.Name, field)
			}
		}
	}
}

// AC-MCP-9, asserted NEGATIVELY so the deferral cannot erode by accident: an
// evaluative word freezes its meaning for every surface that follows the moment
// a client branches on it.
//
// `omitted_sections` still has no producer and is banned outright. `coverage`
// has one — the query executor — so the ban became a condition rather than a
// prohibition: a tool may publish it only by ENUMERATING the classes it can
// answer with. That keeps the exception on the tool that earns it instead of on
// a list in this test, where a second tool could join by editing the test.
func TestNoResultSchemaCarriesADeferredEnvelopeField(t *testing.T) {
	registry := fullRegistry(t)
	for _, spec := range registry.Specs() {
		if strings.Contains(string(spec.OutputSchema), `"omitted_sections"`) {
			t.Errorf("%s advertises %q, which BYO-RES-3 defers until something produces it — "+
				"a client that branches on it freezes a word this build cannot yet mean",
				spec.Name, "omitted_sections")
		}
		if !strings.Contains(string(spec.OutputSchema), `"coverage"`) {
			continue
		}
		registry.mu.RLock()
		tool := registry.tools[spec.Name]
		registry.mu.RUnlock()
		producer, ok := tool.(coverageProducer)
		if !ok {
			t.Errorf("%s advertises \"coverage\" without declaring the classes it can answer with — "+
				"an evaluative word with no enumerated meaning is the deferral BYO-RES-3 made", spec.Name)
			continue
		}
		if len(producer.CoverageClasses()) == 0 {
			t.Errorf("%s declares an EMPTY coverage vocabulary, so the word it publishes means nothing", spec.Name)
		}
	}
}

// coverageProducer is the declaration that earns a tool the right to publish
// `coverage`: the closed set of classes its result can carry.
//
// What this gate enforces is the DECLARATION — that a tool publishing the word
// says which values it can mean. Holding the handler to it is each producer's
// own obligation, owed as a refusal test beside the tool
// (TestQueryWorkspaceRefusesACoverageClassItDoesNotPublish is the first).
type coverageProducer interface {
	CoverageClasses() []string
}

// The envelope reports the WORST freshness behind an answer, not the best: a
// caller asking how current a result is, is asking about its stalest part.
func TestFreshnessReportsTheOldestContributingRecord(t *testing.T) {
	oldest := time.Date(2026, 3, 1, 9, 0, 0, 0, time.UTC)
	newest := time.Date(2026, 8, 1, 9, 0, 0, 0, time.UTC)

	env := sealedEnvelope(t, invokeSealed(readingAgent(), t, unboundedAuthority{}, readToolOver(
		recordAt(datasource.EntityPerson, newest, true),
		recordAt(datasource.EntityDeal, oldest, true),
	)))

	if env.Freshness.LastSyncedAt == nil || !env.Freshness.LastSyncedAt.Equal(oldest) {
		t.Errorf("last_synced_at = %v, want the oldest contributing record's %v", env.Freshness.LastSyncedAt, oldest)
	}
	if !env.Freshness.Authoritative {
		t.Error("two native records answered as non-authoritative")
	}
	if env.Trust != trustInternal {
		t.Errorf("trust = %q, want %q for native records", env.Trust, trustInternal)
	}
}

// One mirror-backed record taints the whole answer. The alternative — reporting
// the majority, or the anchor — would let an agent act on external content
// believing it came from the workspace.
func TestOneMirrorBackedRecordTaintsTheAnswer(t *testing.T) {
	env := sealedEnvelope(t, invokeSealed(readingAgent(), t, unboundedAuthority{}, readToolOver(
		recordAt(datasource.EntityPerson, time.Date(2026, 8, 1, 9, 0, 0, 0, time.UTC), true),
		recordAt(datasource.EntityOrganization, time.Date(2026, 8, 1, 9, 0, 0, 0, time.UTC), false),
	)))

	if env.Freshness.Authoritative {
		t.Error("authoritative stayed true with a mirror-backed record in the answer")
	}
	if env.Trust != trustExternal {
		t.Errorf("trust = %q, want %q — a mirror-backed record is external content", env.Trust, trustExternal)
	}
}

// An answer that carried no content at all is product-generated. It is the
// HIGHEST tier, which is why noteDerivedContent exists: a report over live rows
// that landed here would have had its trust raised.
func TestAnAnswerOverNoRecordIsSystemTrusted(t *testing.T) {
	env := sealedEnvelope(t, invokeSealed(readingAgent(), t, unboundedAuthority{}, readToolOver()))

	if env.Trust != trustSystem {
		t.Errorf("trust = %q, want %q when nothing was read", env.Trust, trustSystem)
	}
	if env.Freshness.LastSyncedAt != nil {
		t.Errorf("last_synced_at = %v, want it absent when no record contributed", env.Freshness.LastSyncedAt)
	}
	if !env.Freshness.Authoritative {
		t.Error("an answer with no mirror content reported itself non-authoritative")
	}
}

// An answer BUILT from records whose provenance this call never read lands at
// t2, with an empty evidence list. Landing at t0 instead would raise the trust
// of every row it summed — much of which is the capture firehose — which is the
// one move the envelope may never make.
func TestContentDerivedFromUnreadRecordsIsUntrusted(t *testing.T) {
	spec := objectSpec("aggregate_probe", principal.ScopeRead)
	spec.OutputSchema = json.RawMessage(`{"type":"object"}`)

	env := sealedEnvelope(t, invokeSealed(readingAgent(), t, unboundedAuthority{},
		derivedTool{spec: spec}))

	if env.Trust != trustExternal {
		t.Errorf("trust = %q, want %q — an aggregate is made of rows whose provenance this call never read",
			env.Trust, trustExternal)
	}
	if len(env.Evidence) != 0 {
		t.Errorf("evidence = %v, want it empty: there is no record reference to follow to a summed row", env.Evidence)
	}
}

// A record stored NATIVELY can still be untrusted: authoritative says where it
// lives, provenance says who wrote it, and a mailbox sync writing into our own
// store is the case both halves have to be read to catch.
func TestACapturedRecordIsUntrustedEvenWhenItIsOurs(t *testing.T) {
	for name, capturedBy := range map[string]string{
		"a connector sync":          "connector:gmail",
		"an agent":                  "agent:overnight",
		"a system job":              "system:retention",
		"provenance we cannot read": "",
	} {
		t.Run(name, func(t *testing.T) {
			env := sealedEnvelope(t, invokeSealed(readingAgent(), t, unboundedAuthority{}, readToolOver(
				recordWrittenBy(datasource.EntityPerson, time.Now(), true, capturedBy))))

			if env.Trust != trustExternal {
				t.Errorf("trust = %q for a record written by %s, want %q — reporting it as first-party "+
					"is the laundering the never-launder rule forbids", env.Trust, name, trustExternal)
			}
			if _, warned := warningNamed(env, warningUntrustedContent); !warned {
				t.Errorf("a t2 answer carries no untrusted-content warning: %v — the tier is a token a "+
					"client has to know to look for, and the instruction is what the doctrine asks for", env.Warnings)
			}
		})
	}
}

// A record whose fields this cannot read at all is the same answer as one written
// by a machine: unvouched-for, and therefore untrusted. It is worth its own case
// because the failure is silent — an unreadable stamp looks exactly like an
// absent one, and the tempting reading of both is "probably fine".
func TestARecordWhoseProvenanceCannotBeReadIsUntrusted(t *testing.T) {
	unreadable := datasource.Record{
		Ref:       datasource.EntityRef{Type: datasource.EntityPerson, ID: ids.NewV7()},
		Fields:    json.RawMessage(`["not an object"]`),
		Freshness: datasource.FreshnessInfo{Authoritative: true},
	}

	env := sealedEnvelope(t, invokeSealed(readingAgent(), t, unboundedAuthority{}, readToolOver(unreadable)))

	if env.Trust != trustExternal {
		t.Errorf("trust = %q for a record with no readable provenance, want %q", env.Trust, trustExternal)
	}
}

// And the provenance rides the evidence, so a caller can check the tier rather
// than take it.
func TestEvidenceCarriesTheProvenanceTheRowWasStampedWith(t *testing.T) {
	env := sealedEnvelope(t, invokeSealed(readingAgent(), t, unboundedAuthority{}, readToolOver(
		recordWrittenBy(datasource.EntityPerson, time.Now(), true, "connector:gmail"))))

	if len(env.Evidence) != 1 {
		t.Fatalf("evidence = %v, want the one record that was read", env.Evidence)
	}
	if env.Evidence[0].CapturedBy != "connector:gmail" || env.Evidence[0].Source == "" {
		t.Errorf("evidence = %+v, want the writer and the source the row was stamped with", env.Evidence[0])
	}
}

// A zero id is not a record. It reaches noteEvidence from an optional argument a
// caller left out, and an evidence entry pointing at the zero uuid would send a
// reader to a record that does not exist.
func TestAZeroIDIsNotEvidence(t *testing.T) {
	spec := objectSpec("zero_evidence_probe", principal.ScopeRead)
	spec.OutputSchema = json.RawMessage(`{"type":"object"}`)

	env := sealedEnvelope(t, invokeSealed(readingAgent(), t, unboundedAuthority{},
		derivedTool{spec: spec, alsoNoteZero: true}))

	if len(env.Evidence) != 1 {
		t.Fatalf("evidence = %v, want only the real record beside the zero id that was also offered", env.Evidence)
	}
	if env.Evidence[0].RecordID.IsZero() {
		t.Errorf("evidence carries the zero id: %v", env.Evidence)
	}
}

// A record read twice in one call is one piece of evidence, and every record
// served is in the list — that is what "no unsourced element" means at this
// granularity.
func TestEvidenceNamesEveryRecordOnceAndOnlyOnce(t *testing.T) {
	twice := recordAt(datasource.EntityPerson, time.Time{}, true)

	env := sealedEnvelope(t, invokeSealed(readingAgent(), t, unboundedAuthority{}, readToolOver(
		twice, twice, recordAt(datasource.EntityDeal, time.Time{}, true),
	)))

	if len(env.Evidence) != 2 {
		t.Fatalf("evidence = %v, want the two distinct records", env.Evidence)
	}
	if env.Evidence[0].RecordID != twice.Ref.ID || env.Evidence[0].RecordType != datasource.EntityPerson {
		t.Errorf("evidence[0] = %v, want the person that was read", env.Evidence[0])
	}
}

// AC-MCP-8, the unit half. A bounded caller is told that filtering applies; an
// unbounded one is not, so "nothing I can see" and "nothing exists" are two
// different documents. Neither carries a count — a count is the side channel
// existence-hiding exists to close, and this test reads the whole result to say
// so rather than only the warning.
func TestABoundedReadSaysSoWithoutSayingHowMuch(t *testing.T) {
	sealed := invokeSealed(readingAgent(), t, fullSeatAuthority{}, readToolOver())
	env := sealedEnvelope(t, sealed)

	warning, ok := warningNamed(env, warningRowScopeFiltered)
	if !ok {
		t.Fatalf("a row-scoped caller's empty read carries no %q warning: %v", warningRowScopeFiltered, env.Warnings)
	}
	for _, digit := range []string{"0", "1", "2", "3", "4", "5", "6", "7", "8", "9"} {
		if strings.Contains(warning.Message, digit) {
			t.Errorf("the warning message carries the number %q: %q — a count is the disclosure this rule forbids",
				digit, warning.Message)
		}
	}

	unbounded := sealedEnvelope(t, invokeSealed(readingAgent(), t, unboundedAuthority{}, readToolOver()))
	if _, present := warningNamed(unbounded, warningRowScopeFiltered); present {
		t.Error("an unbounded caller's empty read claims filtering — then no empty answer can ever mean 'nothing exists'")
	}
}

// Whether an answer was filtered is a question about the QUERY, never about the
// passport scope the tool asked for. A tool under the write or draft scope reads
// row-scoped records too — draft_follow_ups_for sweeps deals, draft_email reads a
// thread — and gating the warning on a read scope left exactly those answers
// looking complete to a bounded caller.
func TestABoundedCallerIsWarnedWhateverScopeTheToolAsksFor(t *testing.T) {
	spec := objectSpec("write_probe", principal.ScopeWrite)
	spec.OutputSchema = schemaFor[SearchRecordsResult]()
	env := sealedEnvelope(t, invokeSealed(scopedAgentCtx(principal.ScopeWrite), t, fullSeatAuthority{},
		recordTool{spec: spec, records: []datasource.Record{recordAt(datasource.EntityPerson, time.Time{}, true)}}))

	if _, present := warningNamed(env, warningRowScopeFiltered); !present {
		t.Errorf("a bounded caller's write-scoped answer claims no bound: %v", env.Warnings)
	}
}

// The trace id is the request's, not a fresh one per tool call: it is what makes
// the call findable beside the audit row and the event the same request wrote.
func TestTheTraceIDIsTheRequestsOwnCorrelationID(t *testing.T) {
	minted := ids.NewV7()
	ctx := principal.WithCorrelationID(readingAgent(), minted)

	env := sealedEnvelope(t, invokeSealed(ctx, t, unboundedAuthority{}, readToolOver()))

	if env.TraceID != minted.String() {
		t.Errorf("trace_id = %q, want the correlation id the caller arrived with %q", env.TraceID, minted)
	}
}

// And a call that arrived without one is BOUND a trace rather than handed an
// empty string: the id is what the handler's own writes stamp on their audit row
// and outbox event, so reporting nothing would also leave those rows untraceable.
func TestACallWithNoCorrelationIDIsGivenOne(t *testing.T) {
	env := sealedEnvelope(t, invokeSealed(readingAgent(), t, unboundedAuthority{}, readToolOver()))

	if env.TraceID == "" {
		t.Fatal("trace_id is empty on a call that arrived without a correlation id")
	}
	if _, err := ids.Parse(env.TraceID); err != nil {
		t.Errorf("trace_id = %q, which is not an id anything can be looked up by: %v", env.TraceID, err)
	}
}

// The version a tool declares is the version its result reports. They are the
// same statement, and a client that cannot compare them cannot tell a shape
// change from a data change.
func TestSchemaVersionIsTheToolsDeclaredVersion(t *testing.T) {
	env := sealedEnvelope(t, invokeSealed(readingAgent(), t, unboundedAuthority{}, readToolOver()))

	if env.SchemaVersion != testToolVersion {
		t.Errorf("schema_version = %q, want the tool's declared %q", env.SchemaVersion, testToolVersion)
	}
}

// A version-less tool is refused at the door, because the alternative is a
// result that reports an empty schema_version on every call it ever serves.
func TestRegisteringAVersionLessToolIsRefused(t *testing.T) {
	defer func() {
		if recovered := recover(); recovered == nil {
			t.Fatal("a tool with no Version registered — its results would carry an empty schema_version")
		}
	}()
	spec := objectSpec("versionless", principal.ScopeRead)
	spec.Version = ""
	NewRegistry(nil, nil).Register(echoTool{spec: spec, out: json.RawMessage(`{}`)})
}

// The envelope's own schema is DERIVED, so the tool's declared shape has to
// arrive intact underneath it — including the members the deriver does not
// model, which a hand-written extension schema is free to use.
func TestTheDeclaredShapeSurvivesBeingWrapped(t *testing.T) {
	inner := json.RawMessage(`{"type":"object","properties":{"quote":{"type":"string"}},` +
		`"required":["quote"],"additionalProperties":false}`)

	wrapped, err := envelopedSchema(inner)
	if err != nil {
		t.Fatalf("wrapping a hand-written schema: %v", err)
	}
	var shape struct {
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal(wrapped, &shape); err != nil {
		t.Fatalf("wrapped schema is unreadable: %v", err)
	}
	if got := string(shape.Properties["data"]); got != string(inner) {
		t.Errorf("data schema = %s, want the tool's own bytes %s", got, inner)
	}
}

// The splice refuses a schema it cannot carry a result in, rather than serving
// one that advertises no payload at all. envelopedSchema's own argument is
// derived from a type here and can never be either of these, which is why the
// two arms are exercised directly.
func TestSplicingIntoASchemaThatCannotCarryAResultIsRefused(t *testing.T) {
	for name, envelope := range map[string]string{
		"unreadable":              `{"type":`,
		"missing its data member": `{"type":"object","properties":{"trust":{"type":"string"}}}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := spliceResultSchema(json.RawMessage(envelope), json.RawMessage(`{"type":"object"}`)); err == nil {
				t.Error("the splice answered a schema, so a tool would advertise a result shape nothing can satisfy")
			}
		})
	}
}

// A tool served from a spec read at call time could report one version while
// tools/list advertised another, and a client comparing them would see a shape
// change nothing had made. The spec a call is served from is the registered one.
func TestAResultReportsTheVersionTheSurfaceWasRegisteredWith(t *testing.T) {
	drifting := &driftingTool{spec: objectSpec("drifting", principal.ScopeRead)}
	r := NewRegistry(nil, auth.NewGate(unboundedAuthority{}))
	r.Register(drifting)
	drifting.spec.Version = "9.9.9-after-registration"

	out, err := r.Invoke(readingAgent(), "drifting", json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("drifting: %v", err)
	}

	if got := sealedEnvelope(t, out).SchemaVersion; got != testToolVersion {
		t.Errorf("schema_version = %q, want the registered %q — a result and tools/list must describe one surface",
			got, testToolVersion)
	}
}

// driftingTool answers a different spec after registration than during it.
type driftingTool struct{ spec mcp.ToolSpec }

func (d *driftingTool) Spec() mcp.ToolSpec { return d.spec }

func (d *driftingTool) Handle(context.Context, json.RawMessage) (json.RawMessage, error) {
	return json.RawMessage(`{}`), nil
}

func warningNamed(env Envelope, code string) (Warning, bool) {
	for _, warning := range env.Warnings {
		if warning.Code == code {
			return warning, true
		}
	}
	return Warning{}, false
}

func slicesContains(haystack []string, needle string) bool {
	for _, candidate := range haystack {
		if candidate == needle {
			return true
		}
	}
	return false
}

// readingAgent is the caller these tests dispatch as; how much of the workspace
// it sees is the authority's answer, not this context's.
func readingAgent() context.Context { return scopedAgentCtx(principal.ScopeRead) }
