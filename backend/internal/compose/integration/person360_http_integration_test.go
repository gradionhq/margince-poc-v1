// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// The person page's three HTTP surfaces, driven through the real handlers.
//
// The service tests beside this one prove the assembly; these prove the wire:
// the status a refusal answers with, the shape the client parses, and the
// view-ack's monotonicity, which is the one thing a GET must never do for
// itself.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gradionhq/margince/backend/internal/compose/person360"
	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/modules/ai"
	"github.com/gradionhq/margince/backend/internal/modules/consent"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// nativeWorkspace is the overlay predicate for a workspace on its own system
// of record — which every fixture here is.
func nativeWorkspace(context.Context) (bool, error) { return false, nil }

func personHandlers(e *Env) person360.Handlers {
	return person360.NewHandlers(
		person360.NewService(e.Pool, e.People, consent.NewStore(e.Pool),
			ai.NewFeedbackStore(e.Pool), func() time.Time { return roomFixedNow }),
		nativeWorkspace,
	)
}

// call drives one handler and returns the status and body.
func call(ctx context.Context, method, path string,
	run func(http.ResponseWriter, *http.Request),
) (int, []byte) {
	rec := httptest.NewRecorder()
	run(rec, httptest.NewRequest(method, path, nil).WithContext(ctx))
	return rec.Code, rec.Body.Bytes()
}

// A contact outside row scope is a 404 on every surface. An empty 200 would
// confirm the record exists and only its contents are withheld.
func TestPerson360SurfacesAllRefuseAForeignContactWith404(t *testing.T) {
	e := Setup(t)
	theirs := e.SeedPerson(t, "Their Contact", &e.Rep3)
	rep := e.As(e.Rep1, []ids.UUID{e.Team1}, roomPerms)
	h := personHandlers(e)
	id := crmcontracts.Id(theirs)

	for name, run := range map[string]func(http.ResponseWriter, *http.Request){
		"the composite read": func(w http.ResponseWriter, r *http.Request) { h.GetPerson360(w, r, id) },
		"the profile-field sidecar": func(w http.ResponseWriter, r *http.Request) {
			h.GetPersonProfileFields(w, r, id)
		},
		"the view acknowledgement": func(w http.ResponseWriter, r *http.Request) {
			h.AcknowledgePersonView(w, r, id)
		},
	} {
		if code, _ := call(rep, http.MethodGet, "/v1/people/"+theirs.String(), run); code != http.StatusNotFound {
			t.Errorf("%s → %d, want 404", name, code)
		}
	}
}

// The composite read answers one JSON object the client can parse, with the
// root record present and the omitted list always an array — never null, which
// a client would have to special-case.
func TestGetPerson360AnswersOneParseableComposite(t *testing.T) {
	e := Setup(t)
	mine := e.SeedPerson(t, "Anna Weber", &e.Rep1)
	rep := e.As(e.Rep1, []ids.UUID{e.Team1}, roomPerms)
	h := personHandlers(e)

	code, body := call(rep, http.MethodGet, "/v1/people/"+mine.String()+"/360",
		func(w http.ResponseWriter, r *http.Request) { h.GetPerson360(w, r, crmcontracts.Id(mine)) })
	if code != http.StatusOK {
		t.Fatalf("360 → %d, want 200", code)
	}
	var page crmcontracts.Person360
	if err := json.Unmarshal(body, &page); err != nil {
		t.Fatalf("the composite did not parse: %v", err)
	}
	if page.Person.Id != crmcontracts.Id(mine) {
		t.Error("the page came back about a different record")
	}
	if page.SectionsOmitted == nil {
		t.Error("sections_omitted is null; a client would have to special-case it")
	}
	if page.AsOf.IsZero() {
		t.Error("as_of is unset, so nothing says which moment the sections describe")
	}
}

// The baseline moves forward ONLY on the acknowledgement. A GET that advanced
// it would destroy the very "what changed" answer the caller opened the page
// to read, and make a prefetch indistinguishable from a visit.
func TestTheViewBaselineMovesOnlyOnTheAcknowledgement(t *testing.T) {
	e := Setup(t)
	mine := e.SeedPerson(t, "Anna Weber", &e.Rep1)
	rep := e.As(e.Rep1, []ids.UUID{e.Team1}, roomPerms)
	h := personHandlers(e)

	baseline := func() int {
		var n int
		if err := OwnerConn(t).QueryRow(context.Background(),
			`SELECT count(*) FROM user_record_view WHERE entity_type = 'person' AND entity_id = $1`,
			mine).Scan(&n); err != nil {
			t.Fatalf("reading the baseline: %v", err)
		}
		return n
	}

	if code, _ := call(rep, http.MethodGet, "/v1/people/"+mine.String()+"/360",
		func(w http.ResponseWriter, r *http.Request) { h.GetPerson360(w, r, crmcontracts.Id(mine)) }); code != http.StatusOK {
		t.Fatalf("360 → %d", code)
	}
	if baseline() != 0 {
		t.Fatal("reading the page moved the visit baseline; the next read's since-last-visit is now empty")
	}

	code, body := call(rep, http.MethodPost, "/v1/people/"+mine.String()+"/view-ack",
		func(w http.ResponseWriter, r *http.Request) { h.AcknowledgePersonView(w, r, crmcontracts.Id(mine)) })
	if code != http.StatusOK {
		t.Fatalf("view-ack → %d, want 200", code)
	}
	var ack crmcontracts.RecordViewAck
	if err := json.Unmarshal(body, &ack); err != nil {
		t.Fatalf("the acknowledgement did not parse: %v", err)
	}
	if baseline() != 1 {
		t.Error("the acknowledgement did not record a visit")
	}

	// Twice is once: the upsert is monotonic, so a second tab cannot rewind a
	// newer mark or accumulate rows.
	if code, _ := call(rep, http.MethodPost, "/v1/people/"+mine.String()+"/view-ack",
		func(w http.ResponseWriter, r *http.Request) { h.AcknowledgePersonView(w, r, crmcontracts.Id(mine)) }); code != http.StatusOK {
		t.Fatalf("second view-ack → %d", code)
	}
	if baseline() != 1 {
		t.Error("a second acknowledgement wrote a second baseline row")
	}
}

// The sidecar answers {data: []} — an empty list when nothing has been
// enriched, which is a different fact from the endpoint refusing. The list is
// never null: a client would have to special-case that.
func TestGetPersonProfileFieldsAnswersAnArrayWhenNothingIsEnriched(t *testing.T) {
	e := Setup(t)
	mine := e.SeedPerson(t, "Anna Weber", &e.Rep1)
	rep := e.As(e.Rep1, []ids.UUID{e.Team1}, roomPerms)
	h := personHandlers(e)

	code, body := call(rep, http.MethodGet, "/v1/people/"+mine.String()+"/profile-fields",
		func(w http.ResponseWriter, r *http.Request) { h.GetPersonProfileFields(w, r, crmcontracts.Id(mine)) })
	if code != http.StatusOK {
		t.Fatalf("profile-fields → %d, want 200", code)
	}
	var out struct {
		Data []crmcontracts.PersonProfileField `json:"data"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("the sidecar did not parse: %v", err)
	}
	if out.Data == nil {
		t.Error("the sidecar answered a null list rather than an empty one")
	}
}
