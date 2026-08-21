// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// What the REST door stages once it decodes the call into a command: the
// target the resolver names, the refusals the resolver raises, and the
// existence-hiding answer to an id that is not one.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
)

// seamRecord is a record the caller may see, held in OUR system of record —
// the one shape an archive may be staged against.
type seamRecord struct {
	datasource.SystemOfRecordProvider
}

// The RecordArchiverV2 half, so this stub stands for a provider that CAN carry
// an approved version. Without it the stub is a fork's v1-only adapter, which
// staging refuses on purpose — and every archive assertion built on it would
// quietly become a refusal assertion.
func (seamRecord) ArchivableTypes(context.Context) ([]datasource.EntityType, error) {
	return []datasource.EntityType{
		datasource.EntityPerson, datasource.EntityOrganization, datasource.EntityDeal,
		datasource.EntityProject, datasource.EntityRelationship, datasource.EntityActivity,
	}, nil
}

func (seamRecord) RefuseArchive(context.Context, datasource.EntityRef) error { return nil }

func (seamRecord) ArchiveAt(_ context.Context, in datasource.ArchiveInput) (datasource.EntityRef, error) {
	return in.Ref, nil
}

func (seamRecord) Read(_ context.Context, ref datasource.EntityRef) (datasource.Record, error) {
	return datasource.Record{
		Ref:       ref,
		Fields:    json.RawMessage(`{"name":"Acme"}`),
		Version:   4,
		Freshness: datasource.FreshnessInfo{Authoritative: true},
	}, nil
}

// hiddenRecord answers every read the way a row outside the caller's scope
// does: not-found, indistinguishable from absent.
type hiddenRecord struct {
	datasource.SystemOfRecordProvider
}

func (hiddenRecord) Read(_ context.Context, ref datasource.EntityRef) (datasource.Record, error) {
	return datasource.Record{}, fmt.Errorf("record %s: %w", ref.ID, apperrors.ErrNotFound)
}

// mirroredRecord is a record the caller can see whose authority lives in
// another system of record — readable, and unstageable.
type mirroredRecord struct {
	datasource.SystemOfRecordProvider
}

func (mirroredRecord) Read(_ context.Context, ref datasource.EntityRef) (datasource.Record, error) {
	return datasource.Record{Ref: ref, Fields: json.RawMessage(`{"name":"Mirrored"}`)}, nil
}

// archiveRequest is a DELETE against one of the archive routes, carrying the
// {id} the chi router would have bound.
func archiveRequest(path string, id ids.UUID) *http.Request {
	req := httptest.NewRequest(http.MethodDelete, path+"/"+id.String(), nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", id.String())
	return req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
}

// A record type the seam does not serve still stages, with its own type and
// its own id. Six of the twelve archivable types are archived by their own
// module, and the tool's narrow schema is not the operation's vocabulary.
//
// Staged against a provider that fails EVERY read, so a resolver that
// consulted the seam for a tag fails here rather than passing on a lenient stub.
func TestAnArchiveOutsideTheToolSchemaStagesItsOwnTypeAndID(t *testing.T) {
	tagID := ids.NewV7()
	staging := &capturingApprovals{}
	pol := agentPolicy{Op: "archiveTag", Access: accessTool, Tool: "archive_record", RecordType: recordTypeTag}

	stageRefusal(httptest.NewRecorder(), archiveRequest("/v1/tags", tagID), staging, restCommandDeps{records: hiddenRecord{}}, pol, nil)

	if staging.last.TargetType != "tag" || staging.last.TargetID != tagID {
		t.Fatalf("staged target = (%s,%s), want (tag,%s) — a tag is archived over REST whether or not the "+
			"record seam has ever heard of one", staging.last.TargetType, staging.last.TargetID, tagID)
	}
}

// A target the caller cannot see stages NOTHING. The archive would answer the
// same not-found once released, by which point a human has spent a one-shot
// authority on a call that was never going to run.
func TestAnArchiveOfAnUnseeableRecordStagesNothing(t *testing.T) {
	staging := &capturingApprovals{}
	pol := agentPolicy{Op: "archivePerson", Access: accessTool, Tool: "archive_record", RecordType: recordTypePerson}
	rec := httptest.NewRecorder()

	stageRefusal(rec, archiveRequest("/v1/people", ids.NewV7()), staging, restCommandDeps{records: hiddenRecord{}}, pol, nil)

	if rec.Code != http.StatusNotFound {
		t.Errorf("archiving a record the caller cannot see answered %d, want 404 — the refusal must not "+
			"tell a caller that a row they may not see exists", rec.Code)
	}
	if staging.last.Tool != "" {
		t.Errorf("an approval was staged for %q against a record nobody can decide about", staging.last.Tool)
	}
}

// The REST door runs the resolver's GUARDS, not only its subject.
//
// An externally-held target is the one refusal only Guards makes — Subject
// reads the same row and is happy to describe it — so this is what proves the
// door asks both questions rather than the one it needs an answer from. The
// approval could never be released: the decidability probe and the version pin
// both read tables this record has no row in.
func TestAnArchiveOfAnExternallyHeldRecordStagesNothing(t *testing.T) {
	staging := &capturingApprovals{}
	pol := agentPolicy{Op: "archivePerson", Access: accessTool, Tool: "archive_record", RecordType: recordTypePerson}
	rec := httptest.NewRecorder()

	stageRefusal(rec, archiveRequest("/v1/people", ids.NewV7()), staging, restCommandDeps{records: mirroredRecord{}}, pol, nil)

	if staging.last.Tool != "" {
		t.Errorf("an approval was staged for %q against a record whose authority lives elsewhere — nobody "+
			"could ever release it", staging.last.Tool)
	}
	// The concrete status, not merely "something other than 200": httperr maps
	// an unclassified error to 500, so an unrelated fault would satisfy a
	// not-200 assertion while this test reported the guard as proven.
	if rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("an externally-held target answered %d, want %d (unsupported_by_sor) — the refusal a caller "+
			"gets must name why the archive cannot be governed here", rec.Code, http.StatusUnprocessableEntity)
	}
}

// A malformed id is a miss, not a parse failure: "that is not a uuid" and
// "there is no such row" must read alike, or the shape of an id tells a caller
// which rows exist.
func TestAMalformedArchiveIDAnswersNotFound(t *testing.T) {
	staging := &capturingApprovals{}
	pol := agentPolicy{Op: "archiveTag", Access: accessTool, Tool: "archive_record", RecordType: recordTypeTag}

	req := httptest.NewRequest(http.MethodDelete, "/v1/tags/not-a-uuid", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "not-a-uuid")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	rec := httptest.NewRecorder()
	stageRefusal(rec, req, staging, restCommandDeps{records: seamRecord{}}, pol, nil)

	if rec.Code != http.StatusNotFound {
		t.Errorf("a malformed archive id answered %d, want 404", rec.Code)
	}
	if staging.last.Tool != "" {
		t.Errorf("an approval was staged for %q against an id that names no row", staging.last.Tool)
	}
}

// The decoder itself, on the same fault: the command layer answers the
// not-found sentinel, so every door that decodes through it hides existence
// the same way rather than each mapping the parse failure for itself.
func TestArchiveCommandRefusesAMalformedIDAsAMiss(t *testing.T) {
	req := httptest.NewRequest(http.MethodDelete, "/v1/tags/not-a-uuid", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "not-a-uuid")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	_, err := archiveCommand(agentPolicy{Op: "archiveTag", RecordType: recordTypeTag}, restCommandDeps{records: seamRecord{}}, req, nil)
	if !errors.Is(err, apperrors.ErrNotFound) {
		t.Errorf("decoding a malformed id answered %v, want the not-found sentinel", err)
	}
}

// The fail-closed refusal covers the resolver branch too.
//
// A concrete target with no record type is a row the approvals surface cannot
// scope: it probes a target's own/team visibility by type, so an untyped row
// would show its summary and proposed change to everyone holding the object
// grant. The check used to sit inside the route walk, which is the one path a
// resolved target never takes — so it is applied to the answer, whichever
// branch produced it.
func TestAResolvedTargetWithNoRecordTypeIsRefused(t *testing.T) {
	staging := &capturingApprovals{}
	// The operation decodes into a command, and declares no record type — the
	// shape a decoder wired to an untyped operation would produce.
	pol := agentPolicy{Op: "archiveTag", Access: accessTool, Tool: "archive_record", RecordType: ""}
	rec := httptest.NewRecorder()

	stageRefusal(rec, archiveRequest("/v1/tags", ids.NewV7()), staging, restCommandDeps{records: seamRecord{}}, pol, nil)

	if staging.last.Tool != "" {
		t.Errorf("an approval was staged for %q with a concrete target and no type — no inbox can scope it, "+
			"and everyone holding the object grant could decide it", staging.last.Tool)
	}
	if rec.Code != http.StatusForbidden {
		t.Errorf("an untyped concrete target answered %d, want 403", rec.Code)
	}
}
