// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// The fold agrees with the writer.
//
// The decode structs in person360 are hand-written against a JSON shape a
// DIFFERENT module produces: the adapter normalizes a provider answer,
// people.WriteProviderClaims stores that value verbatim, and the fold reads
// it back. Nothing in the type system connects those three, so a renamed
// field or a changed shape renders a blank page in silence — the failure this
// test exists to make loud.
//
// It runs the OFFLINE provider's real result through the real writer, then
// reads the real Person360 section, so all three agree or this fails.

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/compose/person360"
	"github.com/gradionhq/margince/backend/internal/modules/ai"
	"github.com/gradionhq/margince/backend/internal/modules/consent"
	"github.com/gradionhq/margince/backend/internal/modules/integrations"
	"github.com/gradionhq/margince/backend/internal/modules/people"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/ports/provider"
)

// completeOneRun drives the offline provider end to end for a subject and
// writes what it returns through the real claim writer.
func completeOneRun(t *testing.T, e *Env, personID ids.UUID) string {
	t.Helper()
	fake := integrations.NewOfflineProvider(0, time.Now)
	cred := provider.Credential("test-key")
	sub, err := fake.Submit(context.Background(), cred, provider.Request{
		CorrelationID: ids.NewV7().String(),
		Identifiers:   provider.PersonIdentifiers{FirstName: "Anna", LastName: "Muster", CompanyName: "Example GmbH"},
		Categories:    []provider.Category{"professional_email", "mobile"},
	})
	if err != nil {
		t.Fatal(err)
	}
	status, err := fake.Poll(context.Background(), cred, sub.ProviderJobID)
	if err != nil {
		t.Fatal(err)
	}
	if status.Outcome != provider.OutcomeCompleted || status.Result == nil {
		t.Fatalf("the offline provider answered %s with no result — the fixture cannot prove anything", status.Outcome)
	}

	var runID string
	admin := e.Admin()
	if err := database.WithWorkspaceTx(admin, e.Pool, func(tx pgx.Tx) error {
		if err := tx.QueryRow(context.Background(), `
			INSERT INTO provider_run
			  (subject_kind, person_id, provider, trigger, state, input_fingerprint,
			   external_correlation_id, connection_version, connection_epoch,
			   configuration_snapshot, requested_categories, completed_at)
			VALUES ('person', $1, 'surfe', 'manual', 'completed', $2,
			        gen_random_uuid(), 1, 1, '{}'::jsonb,
			        ARRAY['professional_email','mobile'], now())
			RETURNING id::text`, personID, "fp-fold-"+personID.String()).Scan(&runID); err != nil {
			return err
		}
		return people.WriteProviderClaims(admin, tx, runID, personID.String(), "surfe",
			status.Result.Claims, time.Now().UTC())
	}); err != nil {
		t.Fatal(err)
	}
	return runID
}

// connectProvider puts the singleton connection into the connected state the
// section reads.
func connectProvider(t *testing.T, e *Env) {
	t.Helper()
	if err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(context.Background(), `
			INSERT INTO provider_connection
			       (id, provider, status, mode, preset, categories)
			VALUES (gen_random_uuid(), 'surfe', 'connected', 'automatic_on_create', 'full',
			        ARRAY['professional_email','mobile'])
			ON CONFLICT (provider) DO UPDATE SET status = 'connected'`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
			_, err := tx.Exec(context.Background(), `DELETE FROM provider_connection WHERE provider = 'surfe'`)
			return err
		}); err != nil {
			t.Errorf("cleaning the connection: %v", err)
		}
	})
}

func person360Service(e *Env) *person360.Service {
	reg, _ := integrations.NewRegistry(integrations.NewOfflineProvider(0, time.Now))
	return person360.NewService(e.Pool, people.NewStore(e.DB()), e.Deals,
		consent.NewStore(e.DB()), ai.NewFeedbackStore(e.DB()), time.Now).WithProviders(reg)
}

// Everything the offline provider returns reaches the page. Each assertion
// names a claim key whose decode struct could silently stop matching.
func TestTheProviderSectionRendersWhatTheAdapterActuallyReturned(t *testing.T) {
	e := Setup(t)
	connectProvider(t, e)
	personID := seedSubject(t, e)
	runID := completeOneRun(t, e, personID)

	page, err := person360Service(e).Assemble(e.Admin(), ids.From[ids.PersonKind](personID))
	if err != nil {
		t.Fatal(err)
	}
	profile := page.ProviderProfile
	if profile == nil {
		t.Fatal("the person page carries no provider section at all")
	}
	if profile.State != "completed" {
		t.Errorf("the section state is %q, want completed", profile.State)
	}
	if len(profile.Emails) != 1 || string(profile.Emails[0].Value) != "a.muster@example.com" {
		t.Errorf("emails = %+v, want the one address the provider returned — the decode struct no longer matches what the writer stores", profile.Emails)
	}
	// The provider omits email_type even under the professional cascade, so
	// the platform labels it from the request and marks the source.
	if profile.Emails[0].EmailTypeSource == nil || *profile.Emails[0].EmailTypeSource != "requested_cascade" {
		t.Error("the address is not marked as labeled-from-request: a request-context label must never masquerade as one the provider returned")
	}
	if len(profile.MobilePhones) != 1 || profile.MobilePhones[0].Value != "+49 170 0000000" {
		t.Errorf("mobile_phones = %+v, want the one number the provider returned", profile.MobilePhones)
	}
	if profile.MobilePhones[0].Confidence == nil {
		t.Error("the mobile number lost its confidence: the reader cannot tell a certain number from a guessed one")
	}
	if profile.LinkedinUrl == nil {
		t.Error("the LinkedIn URL did not survive the fold")
	}
	if profile.CurrentEmployment == nil || profile.CurrentEmployment.JobTitle == nil {
		t.Errorf("current_employment = %+v, want the bought title", profile.CurrentEmployment)
	}
	if len(profile.JobHistory) != 1 {
		t.Fatalf("job_history has %d entries, want 1", len(profile.JobHistory))
	}
	// The provider sends "2019-01" and empty strings for what it lacks.
	if profile.JobHistory[0].StartedAt == nil {
		t.Error("the job-history start date was dropped: the provider sends YYYY-MM and the page shows an undated role")
	}
	if profile.JobHistory[0].LinkedinUrl != nil {
		t.Error("an empty-string LinkedIn URL became a value: the provider's empty string is an absent field, not a blank link")
	}
	if len(profile.Departments) != 1 || len(profile.Seniorities) != 1 {
		t.Errorf("departments=%v seniorities=%v, want one each", profile.Departments, profile.Seniorities)
	}
	if profile.Location == nil {
		t.Error("the location did not survive the fold")
	}

	// The run provenance the page shows: which run, and what nobody asked for.
	if profile.LatestRun == nil || profile.LatestRun.Id.String() != runID {
		t.Errorf("latest_run = %+v, want the run that bought this", profile.LatestRun)
	}
	if profile.ContributingRuns == nil || len(*profile.ContributingRuns) != 1 {
		t.Errorf("contributing_runs = %+v, want the one run the section drew on", profile.ContributingRuns)
	}
	// The fixture requested two of the six categories, so four were not asked
	// for — the difference between "we asked and they had nothing" and "we
	// never asked".
	if len(profile.CategoriesNotRequested) == 0 {
		t.Error("categories_not_requested is empty, which asserts every category was requested — a blank field then reads as 'the provider had nothing'")
	}
}

// Disconnecting stops new egress; it does not delete what was bought. The
// page must not claim the platform knows nothing while showing purchased
// data.
func TestDisconnectingLeavesThePurchasedDataVisibleAndCallsItStale(t *testing.T) {
	e := Setup(t)
	connectProvider(t, e)
	personID := seedSubject(t, e)
	completeOneRun(t, e, personID)

	if err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(context.Background(),
			`UPDATE provider_connection SET status = 'disconnected', credential_ref = NULL WHERE provider = 'surfe'`)
		return err
	}); err != nil {
		t.Fatal(err)
	}

	page, err := person360Service(e).Assemble(e.Admin(), ids.From[ids.PersonKind](personID))
	if err != nil {
		t.Fatal(err)
	}
	profile := page.ProviderProfile
	if profile == nil {
		t.Fatal("the person page carries no provider section")
	}
	if len(profile.Emails) == 0 {
		t.Error("the purchased address vanished on disconnect: disconnecting is not deleting")
	}
	if profile.State == "not_connected" {
		t.Error("the page says no provider is connected while showing purchased contact details — it must read stale, not deny what it is displaying")
	}
}
