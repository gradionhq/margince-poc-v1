// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	openapi_types "github.com/oapi-codegen/runtime/types"

	"github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/provenance"
	"github.com/gradionhq/margince/backend/pkg/extension"
	"github.com/gradionhq/margince/backend/pkg/extension/crm"
)

// A job tick is refused before anything else happens, and the refusal is
// checked HERE rather than in the database lane because it is the one that must
// hold with no database at all: a tick reaching a pool to be told no would mean
// the check ran too late.
func TestAJobTickCannotWriteACoreRecord(t *testing.T) {
	// No pool and no transaction, deliberately. If admit ever stopped refusing
	// first, this test would panic rather than pass.
	core := extensionCore{unattended: true}

	_, err := core.Activities().Create(context.Background(), crm.CreateActivityRequest{
		Kind: crm.CreateActivityRequestKindNote, Source: "extension:probe",
	})
	if !errors.Is(err, extension.ErrForbidden) {
		t.Fatalf("err = %v, want ErrForbidden", err)
	}
	if !strings.Contains(err.Error(), "no caller") {
		t.Errorf("the refusal does not say why a tick is different: %v", err)
	}
}

// The refusal classes are a MAPPING, and the property that matters is what does
// not survive it: a unit is other people's code, so the core's own error text
// must not reach it.
func TestThePortMapsRefusalsAndLeaksNoDetail(t *testing.T) {
	internal := errors.New("relation \"person\" violates constraint person_email_key")
	for name, probe := range map[string]struct {
		in   error
		want error
	}{
		"permission denied": {apperrors.ErrPermissionDenied, extension.ErrForbidden},
		"not found":         {apperrors.ErrNotFound, extension.ErrNotFound},
		"version skew":      {apperrors.ErrVersionSkew, extension.ErrConflict},
		"conflict":          {apperrors.ErrConflict, extension.ErrConflict},
		"a field the contract refuses": {
			&activityFieldFault{}, extension.ErrInvalid,
		},
	} {
		t.Run(name, func(t *testing.T) {
			if got := portRefusal(probe.in); !errors.Is(got, probe.want) {
				t.Errorf("portRefusal(%v) = %v, want %v", probe.in, got, probe.want)
			}
		})
	}

	if got := portRefusal(nil); got != nil {
		t.Errorf("portRefusal(nil) = %v, want nil — a mapper that invents an error on the success path refuses every write", got)
	}

	unclassified := portRefusal(internal)
	if unclassified == nil {
		t.Fatal("an unclassified fault was mapped to success")
	}
	if strings.Contains(unclassified.Error(), "person") || strings.Contains(unclassified.Error(), "constraint") {
		t.Errorf("the core's own error text reached the unit: %v", unclassified)
	}
}

// activityFieldFault stands in for any of the modules' typed field refusals: the
// interface is what httperr turns into a 422, and it is what the port reads.
type activityFieldFault struct{}

func (f *activityFieldFault) Error() string { return "kind is required" }
func (f *activityFieldFault) FieldFault() (field, code, message string) {
	return "kind", "required", "kind is required"
}

// The two generated type sets are bridged by JSON, so the bridge has to carry a
// FULL record, not a convenient one: the risk it exists to close is a field that
// crosses in principle and is dropped in fact.
func TestTheBridgeCarriesAWholeActivity(t *testing.T) {
	occurred := time.Date(2026, 8, 13, 9, 30, 0, 0, time.UTC)
	body, subject, capturedBy := "the body", "the subject", "9f1d0c4a-3b2e-4f57-9a10-2c8e6b5d4f31"
	direction := crmcontracts.ActivityDirectionInbound
	activityID, subjectID := ids.NewV7(), ids.NewV7()
	internal := crmcontracts.Activity{
		Id:         openapi_types.UUID(activityID),
		Kind:       crmcontracts.ActivityKindNote,
		Body:       &body,
		Subject:    &subject,
		Direction:  &direction,
		OccurredAt: occurred,
		Source:     "extension:notes",
		CapturedBy: &capturedBy,
		Links: &[]crmcontracts.ActivityLink{{
			EntityType: crmcontracts.ActivityLinkEntityTypePerson,
			EntityId:   openapi_types.UUID(subjectID),
		}},
	}

	published, err := transcode[crm.Activity](internal)
	if err != nil {
		t.Fatalf("transcode: %v", err)
	}
	switch {
	case published.Id != activityID.String():
		t.Errorf("id = %q, want %q", published.Id, activityID)
	case string(published.Kind) != string(internal.Kind):
		t.Errorf("kind = %q, want %q", published.Kind, internal.Kind)
	case published.Body == nil || *published.Body != body:
		t.Errorf("body = %v, want %q", published.Body, body)
	case published.Subject == nil || *published.Subject != subject:
		t.Errorf("subject = %v, want %q", published.Subject, subject)
	case published.Direction == nil || string(*published.Direction) != string(direction):
		t.Errorf("direction = %v, want %q", published.Direction, direction)
	case !published.OccurredAt.Equal(occurred):
		t.Errorf("occurred_at = %v, want %v", published.OccurredAt, occurred)
	case published.Source != internal.Source:
		t.Errorf("source = %q, want %q", published.Source, internal.Source)
	case published.CapturedBy == nil || *published.CapturedBy != capturedBy:
		t.Errorf("captured_by = %v, want %q", published.CapturedBy, capturedBy)
	}
	if published.Links == nil || len(*published.Links) != 1 {
		t.Fatalf("links = %v, want the one the record carries — a nested slice is where a bridge silently loses depth", published.Links)
	}
	if link := (*published.Links)[0]; link.EntityId != subjectID.String() || string(link.EntityType) != "person" {
		t.Errorf("link = %s/%s, want person/%s", link.EntityType, link.EntityId, subjectID)
	}
}

// The request crosses the other way, and the link helper is what a unit uses to
// build it — so the helper's output has to survive the same bridge.
func TestTheBridgeCarriesARequestWithItsLinks(t *testing.T) {
	body := "a filed note"
	request := crm.CreateActivityRequest{
		Kind: crm.CreateActivityRequestKindNote, Body: &body, Source: "extension:notes",
	}.LinkTo(crm.CreateActivityRequestLinksEntityTypeDeal, "7c9e6679-7425-40de-944b-e07fc1f90ae7")

	internal, err := transcode[crmcontracts.CreateActivityRequest](request)
	if err != nil {
		t.Fatalf("transcode: %v", err)
	}
	if internal.Links == nil || len(*internal.Links) != 1 {
		t.Fatalf("links = %v, want the one LinkTo added", internal.Links)
	}
	if link := (*internal.Links)[0]; string(link.EntityType) != "deal" {
		t.Errorf("entity_type = %q, want deal", link.EntityType)
	}
	if internal.Body == nil || *internal.Body != body {
		t.Errorf("body = %v, want %q", internal.Body, body)
	}
}

// LinkTo appends rather than replaces: an activity can name more than one
// subject, and a helper that overwrote the previous one would lose the first
// silently.
func TestLinkToAppends(t *testing.T) {
	request := crm.CreateActivityRequest{Kind: crm.CreateActivityRequestKindNote, Source: "extension:probe"}.
		LinkTo(crm.CreateActivityRequestLinksEntityTypePerson, "7c9e6679-7425-40de-944b-e07fc1f90ae7").
		LinkTo(crm.CreateActivityRequestLinksEntityTypeOrganization, "3f2504e0-4f89-41d3-9a0c-0305e82c3301")
	if request.Links == nil || len(*request.Links) != 2 {
		t.Fatalf("links = %v, want both", request.Links)
	}
}

// A handler is HANDED a context per invocation, so it can keep one. Passing a
// retained context to a Core verb must not carry the identity that came with
// it: the verb re-binds the invocation's own authority over whatever it is
// given, and what a handler passes contributes cancellation and its own values
// and nothing else.
//
// This is the one defect that would have defeated the port's whole claim, so it
// is asserted on the value the write actually runs under rather than on the
// absence of a symptom.
func TestACoreVerbRebindsTheInvocationsAuthorityOverAHandlersContext(t *testing.T) {
	invocation := principal.WithActor(context.Background(), principal.Principal{
		Type: principal.PrincipalHuman, ID: "the-live-caller",
	})
	// What a unit could keep from an earlier, higher-privileged call.
	retained := principal.WithActor(context.Background(), principal.Principal{
		Type: principal.PrincipalHuman, ID: "an-earlier-admin",
	})

	var sawUnderTheWrite string
	core := extensionCore{authority: func(ctx context.Context) (context.Context, error) {
		// The Runtime's own scoped, standing in: it rebinds from the
		// invocation, never from what it is handed.
		if actor, bound := principal.Actor(invocation); bound {
			ctx = principal.WithActor(ctx, actor)
		}
		if actor, bound := principal.Actor(ctx); bound {
			sawUnderTheWrite = actor.ID
		}
		return ctx, nil
	}}

	bound, err := core.authorised(retained)
	if err != nil {
		t.Fatalf("authorised: %v", err)
	}
	if sawUnderTheWrite != "the-live-caller" {
		t.Errorf("the write would run as %q, want the invocation's own caller", sawUnderTheWrite)
	}
	if actor, ok := principal.Actor(bound); !ok || actor.ID != "the-live-caller" {
		t.Errorf("the bound context carries %v, want the live caller", actor)
	}
}

// A Core built without the invocation's authority refuses rather than falling
// back to the caller's context, because the fallback is exactly the swap above.
func TestACoreWithNoAuthorityRefuses(t *testing.T) {
	_, err := extensionCore{}.authorised(context.Background())
	if err == nil {
		t.Fatal("a core with no authority accepted a context")
	}
	if !strings.Contains(err.Error(), "authority") {
		t.Errorf("the refusal does not name what is missing: %v", err)
	}
}

// The attribution is BOUND BY PRODUCTION, which is the half a test that binds
// it itself cannot show.
//
// storekit's own test proves the merge rule against a context it constructs;
// this one proves the context is constructed at all, by calling the Runtime's
// scoped — the same function every capability goes through — and reading what
// it produced. Without it, the whole attribution story could ship inert and
// green, which is exactly how it was found.
func TestTheRuntimeBindsTheUnitsAttribution(t *testing.T) {
	invocation := principal.WithWorkspaceID(
		principal.WithActor(context.Background(), principal.Principal{
			Type: principal.PrincipalHuman, ID: "the-caller",
		}), ids.NewV7())

	// The binding a served call has, minus a database: scoped refuses on an
	// unwired ROLE before it binds anything, and &pgxpool.Pool{} is a handle
	// nothing on this path dials. What is under test is the context scoped
	// builds, not what a connection would do with it.
	rt := runtimeFor(invocation, "notes", "1.0.0", "tool/file_note",
		extensionRuntimeBinding{pool: &pgxpool.Pool{}})
	// A handler's own context, carrying none of the invocation's facts.
	bound, err := rt.scoped(context.Background())
	if err != nil {
		t.Fatalf("scoped: %v", err)
	}

	attribution, stamped := provenance.ExtensionFrom(bound)
	if !stamped {
		t.Fatal("no attribution was bound, so every core write this unit makes is anonymous in the audit log")
	}
	if attribution.Unit != "notes" || attribution.Version != "1.0.0" || attribution.Via != "tool/file_note" {
		t.Errorf("attribution = %+v, want the unit, its declared version and the surface the call arrived on", attribution)
	}
	// And the rest of the invocation's authority, since the same call binds it.
	if actor, ok := principal.Actor(bound); !ok || actor.ID != "the-caller" {
		t.Errorf("actor = %v, want the invocation's own", actor)
	}
}
