// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package agents

// The query_workspace tool (SEARCH-PARAM-7, 🟢): a question with STRUCTURE —
// conditions, a hop, a likeness — compiled to a closed vocabulary, executed,
// and answered with what kind of answer it is.
//
// The plan grammar and its executor live in the search module, which this
// package may not import (ADR-0054 §3), so the composition root injects the
// whole compile-validate-execute path as one function. That is deliberate
// beyond the import rule: the vocabulary is DERIVED per caller from the field
// catalog and the live column catalog, so there is nothing about it this
// package could usefully hold.
//
// WHAT THIS TOOL OWNS is the half the executor cannot: turning the refs it
// answers into records, through the same seam every other read on this surface
// uses. That is where the trust tier is stamped, where the result envelope's
// freshness and evidence are collected, where the caller's object RBAC and row
// scope are applied to the record itself, and where the read is COUNTED against
// MCP-SESS-READS — so a query answering twenty-five rows spends twenty-five of
// the caller's records, not one. A densely-joined answer is the cheapest bulk
// read on a surface that charges per call (A139), and this is the densest read
// the surface has.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
	"github.com/gradionhq/margince/backend/internal/shared/ports/mcp"
)

// The coverage vocabulary, as this surface publishes it. The words are the
// executor's (search-and-retrieval.md, the coverage contract) and are restated
// here because a wire contract belongs to the wire: this package cannot import
// the module that defines them, and a client branches on these three strings.
// TestTheSurfaceAndTheExecutorAgreeOnCoverage in the composition layer — the one
// place that can see both — fails if the two ever diverge.
const (
	// CoverageCompleteExact: every record matching the plan is in the answer.
	CoverageCompleteExact = "complete_exact"
	// CoverageRankedSemantic: a similarity clause ordered the result, so these
	// ranked highest and recall is not guaranteed.
	CoverageRankedSemantic = "ranked_semantic"
	// CoveragePartialDegraded: something in the plan could not be answered as
	// asked. The notes say which part.
	CoveragePartialDegraded = "partial_degraded"
)

// CodeRowUnreadable is this tool's own note: a record the plan admitted could
// not be read back when the answer was assembled.
//
// It exists because the two steps are separate reads and the world moves
// between them — a record archived, or an authority narrowed, in the moment
// between selection and hydration. Dropping such a row in silence would hand
// back a short answer that claims to be complete, which is the exact narrowing
// this feature exists to prevent.
const CodeRowUnreadable = "row_unreadable_after_selection"

// jsonNull is the literal a JSON null decodes to inside a json.RawMessage.
// encoding/json produces the same nil RawMessage for an ABSENT member, so the
// two are told apart by the bytes rather than by the Go value.
var jsonNull = []byte("null")

// QueryRunner compiles, validates and executes ONE plan document, answering
// the records it admitted as references.
//
// It answers refs rather than records because the module behind it may not
// import a sibling to shape one. Hydration is this tool's job, and doing it
// here is what puts every row through the surface's own read path.
type QueryRunner func(ctx context.Context, plan json.RawMessage) (QueryAnswer, error)

// QueryAnswer is one executed plan, as the executor reports it.
type QueryAnswer struct {
	Refs []QueryRef
	// Coverage is one of the three classes above — how exhaustively the plan
	// was answered, which a caller must read before trusting the row set.
	Coverage string
	// Notes are the machine-readable reasons the coverage is what it is.
	Notes []QueryNote
	// Narrative is the executed plan in plain language, so a caller can check
	// that the question answered is the question asked.
	Narrative string
	Limit     int
}

// QueryRef is one admitted record, before it is read.
type QueryRef struct {
	Type     string
	ID       ids.UUID
	Score    float64
	Evidence []QueryEvidence
}

// RegisterQueryTool joins query_workspace to the surface once a runner exists —
// the same conditional registration the other injected-engine tools take. An
// installation whose executor is unwired serves no query tool rather than one
// that refuses every call.
func RegisterQueryTool(r *Registry, p datasource.SystemOfRecordProvider, run QueryRunner) {
	if run == nil {
		return
	}
	r.Register(queryWorkspace{p: p, run: run})
}

type queryWorkspace struct {
	p   datasource.SystemOfRecordProvider
	run QueryRunner
}

func (t queryWorkspace) Spec() mcp.ToolSpec {
	return mcp.ToolSpec{
		Name: "query_workspace", Title: "Query the workspace", Version: toolVersionV1,
		Description:   queryWorkspaceCopy.render(),
		RequiredScope: principal.ScopeRead, Tier: mcp.TierAutoExecute,
		// The plan document is NOT re-declared here. Its grammar is published
		// at margince://schema/query, derived per caller from the field catalog
		// and the live column catalog — a second copy of it in this schema
		// would be a hand-maintained list of exactly the thing that is derived,
		// and it would go stale the first time a workspace added a field.
		InputSchema: schema(`{"type":"object","required":["plan"],"properties":{
			"plan":{"type":"object","description":"A query plan, in the grammar published at margince://schema/query. Read that resource for the record types, fields, operators and relationships this workspace admits: a name outside it is refused, never guessed at."}},
			"additionalProperties":false}`),
		OutputSchema: schemaFor[QueryWorkspaceResult](),
	}
}

// CoverageClasses is the closed set this tool's `coverage` field can hold.
//
// Declaring it is what earns the exception to BYO-RES-3's deferral: the
// evaluative word ships on the ONE tool that produces it, with its meaning
// enumerated, rather than on an envelope where it would have to mean something
// for every tool on the surface. The handler refuses a class outside this set
// instead of putting an unknown word in front of a client that branches on it.
func (t queryWorkspace) CoverageClasses() []string {
	return []string{CoverageCompleteExact, CoverageRankedSemantic, CoveragePartialDegraded}
}

func (t queryWorkspace) Handle(ctx context.Context, in json.RawMessage) (json.RawMessage, error) {
	var args struct {
		Plan json.RawMessage `json:"plan"`
	}
	if err := decodeArgs(in, &args); err != nil {
		return nil, err
	}
	// An absent or null `plan` is a mistake in the CALL, and is named as one.
	// Passing it on would answer with the grammar's refusal about a malformed
	// document, sending a caller to re-read a vocabulary their mistake was
	// never in.
	//
	// An empty OBJECT is not this case: `{}` is a document the caller wrote, so
	// it goes to the grammar, which names the members it is missing. That is the
	// more actionable of the two answers, and it is the grammar's to give.
	if trimmed := bytes.TrimSpace(args.Plan); len(trimmed) == 0 || bytes.Equal(trimmed, jsonNull) {
		return nil, &BadArgsError{Cause: errors.New("`plan` is required and takes a query plan object; margince://schema/query publishes the grammar it is written in")}
	}
	answer, err := t.run(ctx, args.Plan)
	if err != nil {
		return nil, err
	}
	if !slices.Contains(t.CoverageClasses(), answer.Coverage) {
		return nil, fmt.Errorf("crmagents: the query executor answered coverage %q, which query_workspace does not publish", answer.Coverage)
	}
	result, err := t.hydrate(ctx, answer)
	if err != nil {
		return nil, err
	}
	return json.Marshal(result)
}

// hydrate turns the executor's refs into records through the datasource seam.
//
// Every record on the way out goes through newWireRecord, which is the ONE
// place a record becomes output on this surface: the overlay trust tier is
// stamped there, the envelope's freshness and evidence are collected there, and
// the record is counted against the caller's read bound there. A tool that
// assembled rows itself would answer with an envelope claiming nothing had been
// read, and would serve every one of those records for free.
//
// THE HOP IS A RECORD TOO. Its id and title come out of the executor's own
// statement, so passing them straight through would put a record on the wire
// that the seam never saw: its content would ride out untainted by the trust
// tier, the envelope would not name it among the records the answer rests on,
// and nothing would re-check that the caller may still read it. It is read
// back like any other record, through a cache — a page of deals at one
// organization is one hop read, not one per row.
func (t queryWorkspace) hydrate(ctx context.Context, answer QueryAnswer) (QueryWorkspaceResult, error) {
	result := QueryWorkspaceResult{
		Rows:         make([]QueryWorkspaceRow, 0, len(answer.Refs)),
		Coverage:     answer.Coverage,
		Notes:        append(make([]QueryNote, 0, len(answer.Notes)), answer.Notes...),
		ExecutedPlan: answer.Narrative,
		Limit:        answer.Limit,
	}
	hops := hopCache{seen: map[datasource.EntityRef]hopRead{}}
	var dropped bool
	for _, ref := range answer.Refs {
		record, readable, err := t.read(ctx, ref.Type, ref.ID)
		if err != nil {
			return QueryWorkspaceResult{}, err
		}
		if !readable {
			dropped = true
			continue
		}
		// A hop that can no longer be read takes its row with it. The hop is
		// part of why the row was selected, so serving the row without it would
		// tell the caller that a deal sits at an organization they may not know
		// exists — the disclosure the hop's own row scope refused at selection.
		evidence, admitted, err := t.hydrateEvidence(ctx, ref.Evidence, &hops)
		if err != nil {
			return QueryWorkspaceResult{}, err
		}
		if !admitted {
			dropped = true
			continue
		}
		result.Rows = append(result.Rows, QueryWorkspaceRow{
			Record: record, Score: ref.Score, Evidence: evidence,
		})
	}
	if dropped {
		result.Coverage = CoveragePartialDegraded
		result.Notes = append(result.Notes, QueryNote{
			Code: CodeRowUnreadable,
			// No COUNT. How many records a caller may not read is the size of
			// what was withheld, and stating it is the side channel
			// existence-hiding exists to close — the same rule the envelope's
			// row_scope_filtered warning keeps. That it happened is what the
			// caller needs; how often is not theirs.
			Detail: "at least one record the plan matched could not be read back and is not among these rows; " +
				"re-run the plan to see the current answer",
		})
	}
	return result, nil
}

// read fetches one record through the seam.
//
// The BOOL is the verdict, kept separate from the error because the two mean
// different things to the caller. False is a definite answer — archived, or an
// authority narrowed since the plan ran — and the row is dropped. An error is
// the ABSENCE of an answer, and is returned: reporting an unreachable store as
// a partial result would describe an infrastructure fault as a property of the
// caller's data, and they would act on the rows that did come back.
func (t queryWorkspace) read(ctx context.Context, recordType string, id ids.UUID) (wireRecord, bool, error) {
	record, err := t.p.Read(ctx, datasource.EntityRef{Type: datasource.EntityType(recordType), ID: id})
	if err != nil {
		if errors.Is(err, apperrors.ErrNotFound) || errors.Is(err, apperrors.ErrPermissionDenied) {
			return wireRecord{}, false, nil
		}
		return wireRecord{}, false, err
	}
	return newWireRecord(ctx, record), true, nil
}

// hopRead is one hop read and its verdict, so an unreadable hop is
// remembered as a verdict rather than as a missing map entry — the two are
// indistinguishable otherwise, and a second row sharing the hop would ask again.
type hopRead struct {
	record   wireRecord
	readable bool
}

// hopCache holds the hop records one answer has already read. A plan's rows
// commonly share a hop — every deal at one organization — so without it a page
// of 200 rows would read the same organization 200 times over.
type hopCache struct {
	seen map[datasource.EntityRef]hopRead
}

// hydrateEvidence reads each hop record back through the seam. The bool is
// false when ANY hop is no longer readable, which drops the row that rested on
// it.
func (t queryWorkspace) hydrateEvidence(ctx context.Context, refs []QueryEvidence, hops *hopCache) ([]QueryEvidence, bool, error) {
	out := make([]QueryEvidence, 0, len(refs))
	for _, ref := range refs {
		key := datasource.EntityRef{Type: datasource.EntityType(ref.RecordType), ID: ref.ID}
		hop, cached := hops.seen[key]
		if !cached {
			record, readable, err := t.read(ctx, ref.RecordType, ref.ID)
			if err != nil {
				return nil, false, err
			}
			hop = hopRead{record: record, readable: readable}
			hops.seen[key] = hop
		}
		if !hop.readable {
			return nil, false, nil
		}
		// The title stays the executor's: it came out of a statement carrying
		// the hop's own row scope, so it is already the caller's to read, and
		// re-deriving it here would need each record type's display-title rule
		// — knowledge that belongs to the branch that declares it, not to this
		// package. What the seam read adds is what the statement could not: the
		// trust tier, the envelope's evidence entry, and a re-check that the
		// caller may still read the record at all.
		out = append(out, QueryEvidence{
			Relation: ref.Relation, RecordType: ref.RecordType, ID: ref.ID,
			Title: ref.Title, TrustTier: hop.record.TrustTier,
		})
	}
	return out, true, nil
}
