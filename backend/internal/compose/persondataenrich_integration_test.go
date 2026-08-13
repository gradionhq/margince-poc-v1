// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package compose

// The automatic-enrichment consumer end to end: a person.created envelope
// reaches it and a run exists for that person — admitted, fenced, frozen and
// reserved, with the submit job committed beside it.
//
// It lives in package compose rather than compose/integration because the
// cross-module binding it exercises (bindProviderDomain) is unexported: a
// test that hand-assembled the callbacks would prove the assembly it wrote,
// not the one production wires.
//
// The two swallowed refusals are the point of the other cases. Auto-enrich
// switched off and no provider connected are configurations a customer chose,
// and a consumer that errored on them would wedge the group and log a failure
// on every contact created in a workspace that never wanted this.

import (
	"context"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/modules/integrations"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/keyvault"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/events"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/ports/provider"
)

type providerConsumerEnv struct {
	consumer *PersonDataEnrich
	personID ids.UUID
	owner    *pgx.Conn
	enqueued *int
	// queue calls the run service directly, so a test can see the refusal
	// the consumer is designed to swallow.
	queue func() (provider.Run, error)
}

// setupProviderConsumer seeds one workspace, one person and one connected
// provider in the named mode, and wires the consumer over the real binding.
func setupProviderConsumer(t *testing.T, mode string) *providerConsumerEnv {
	t.Helper()
	ownerDSN, appDSN := os.Getenv("MARGINCE_TEST_DSN"), os.Getenv("MARGINCE_TEST_APP_DSN")
	if ownerDSN == "" || appDSN == "" {
		t.Fatal("MARGINCE_TEST_DSN / MARGINCE_TEST_APP_DSN not set — run `make db-up` (integration tests fail loudly, they never skip)")
	}
	ctx := context.Background()
	owner, err := pgx.Connect(ctx, ownerDSN)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := owner.Close(context.Background()); err != nil {
			t.Errorf("closing the owner connection: %v", err)
		}
	})

	e := &providerConsumerEnv{owner: owner, enqueued: new(int), personID: ids.NewV7()}
	// EXACTLY one workspace, because the consumer resolves its own through
	// InstallationDB and that refuses while more than one exists (ADR-0061
	// §3). The template starts empty, so this seeds the singleton and removes
	// it again — a leftover second workspace would fail every later test in
	// this binary for a reason that has nothing to do with them.
	ws := ids.NewV7()
	if _, err := owner.Exec(ctx,
		`INSERT INTO workspace (id, slug) VALUES ($1, $2)`, ws, "pde-"+ws.String()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		// ARCHIVED, not deleted. A queued run writes an audit row, audit_log
		// is append-only by constraint, and it carries the workspace FK — so
		// the row cannot be removed and neither can the workspace. Archiving
		// is what the installation resolver actually reads
		// (`archived_at IS NULL`), so it restores the singleton the next test
		// needs without falsifying the audit trail.
		//
		// The runs go first regardless: they are installation-global, and a
		// leftover would fall into the next test's due-sweep.
		for _, stmt := range []string{
			`DELETE FROM provider_run_reservation WHERE run_id IN
			   (SELECT r.id FROM provider_run r JOIN person p ON p.id = r.person_id WHERE p.workspace_id = $1)`,
			`DELETE FROM provider_run WHERE person_id IN (SELECT id FROM person WHERE workspace_id = $1)`,
			`UPDATE workspace SET archived_at = now() WHERE id = $1`,
		} {
			if _, err := owner.Exec(context.Background(), stmt, ws); err != nil {
				t.Errorf("cleaning the test workspace: %v", err)
			}
		}
	})
	if _, err := owner.Exec(ctx, `
		INSERT INTO person (id, workspace_id, full_name, first_name, last_name, source, captured_by)
		VALUES ($1, $2, 'Anna Muster', 'Anna', 'Muster', 'manual', 'human:test')`,
		e.personID, ws); err != nil {
		t.Fatal(err)
	}
	// The connection is installation-wide (one row per provider), so upsert:
	// two environments in one binary must not collide on a singleton.
	if _, err := owner.Exec(ctx, `
		INSERT INTO provider_connection
		       (id, provider, status, mode, preset, categories, automatic_individual_create)
		VALUES (gen_random_uuid(), 'surfe', 'connected', $1, 'full',
		        ARRAY['professional_email'], true)
		ON CONFLICT (provider) DO UPDATE
		   SET status = 'connected', mode = $1, automatic_individual_create = true`, mode); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := owner.Exec(context.Background(),
			`DELETE FROM provider_connection WHERE provider = 'surfe'`); err != nil {
			t.Errorf("cleaning the connection: %v", err)
		}
	})

	pool, err := database.NewPool(ctx, appDSN)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	reg, err := integrations.NewRegistry(integrations.NewOfflineProvider(0, time.Now))
	if err != nil {
		t.Fatal(err)
	}
	store, err := integrations.NewStore(
		database.BindTo(pool, ids.From[ids.WorkspaceKind](ws)), keyvault.NewMemory(), reg, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	// The REAL binding, plus a counting enqueue: a queued run with no job is
	// the failure QueueRun refuses to commit, and only a counter proves it.
	bound := bindProviderDomain(store).WithSubmitEnqueue(
		func(context.Context, pgx.Tx, string, string) error {
			*e.enqueued++
			return nil
		})
	e.consumer = NewPersonDataEnrich(pool, bound, slog.New(slog.NewTextHandler(os.Stderr, nil)))
	e.queue = func() (provider.Run, error) {
		return bound.QueueRun(e.consumer.systemContext(ctx, personCreatedEnvelope(e.personID), ws),
			provider.QueueInput{PersonID: e.personID.String(), Trigger: provider.TriggerAutomaticCreate})
	}
	return e
}

// personCreatedEnvelope is what the outbox relay delivers.
func personCreatedEnvelope(personID ids.UUID) events.Envelope {
	return events.Envelope{
		EventID:    ids.NewV7(),
		Type:       "person.created",
		OccurredAt: time.Now().UTC(),
		Entity:     events.EntityRef{Type: "person", ID: personID},
		Trace:      events.Trace{CorrelationID: ids.NewV7()},
	}
}

func (e *providerConsumerEnv) runsForPerson(t *testing.T) int {
	t.Helper()
	var runs int
	if err := e.owner.QueryRow(context.Background(),
		`SELECT count(*) FROM provider_run WHERE person_id = $1`, e.personID).Scan(&runs); err != nil {
		t.Fatal(err)
	}
	return runs
}

// A connected provider set to automatic-on-create: the event buys the data.
func TestPersonCreatedQueuesAnEnrichmentRun(t *testing.T) {
	e := setupProviderConsumer(t, "automatic_on_create")

	if err := e.consumer.HandleEvent(context.Background(), personCreatedEnvelope(e.personID)); err != nil {
		t.Fatal(err)
	}
	// The consumer swallows configuration refusals by design, so a silent
	// no-op here would be indistinguishable from a broken fixture. Ask the
	// service directly for the refusal it gave.
	if _, err := e.queue(); err != nil {
		t.Fatalf("QueueRun refused the fixture: %v", err)
	}

	var state, skipReason string
	if err := e.owner.QueryRow(context.Background(),
		`SELECT state, coalesce(skip_reason, '') FROM provider_run WHERE person_id = $1`,
		e.personID).Scan(&state, &skipReason); err != nil {
		t.Fatalf("no run was queued for a newly created person: %v", err)
	}
	if skipReason != "" {
		t.Fatalf("the run was skipped (%s) rather than queued — the fixture trips a fence it was not meant to", skipReason)
	}
	if state != string(provider.RunQueued) {
		t.Errorf("the run is %s, want queued — the consumer must not call the provider on the event lane", state)
	}
	if *e.enqueued != 1 {
		t.Errorf("%d submit jobs were committed, want 1 — a queued run with no job would sit in the live-run index forever, blocking every later attempt", *e.enqueued)
	}
}

// Auto-enrich switched off: the refusal is the configuration working, so the
// consumer reports success and buys nothing.
func TestPersonCreatedWithAutoEnrichOffBuysNothingAndDoesNotError(t *testing.T) {
	e := setupProviderConsumer(t, "on_demand")

	if err := e.consumer.HandleEvent(context.Background(), personCreatedEnvelope(e.personID)); err != nil {
		t.Fatalf("a switched-off toggle surfaced as a failure, which would wedge the consumer group: %v", err)
	}
	if runs := e.runsForPerson(t); runs != 0 {
		t.Errorf("%d runs exist in a workspace with auto-enrich off — the customer's credits were spent against their own setting", runs)
	}
}

// Only person.created buys. An edit must not re-purchase.
func TestOnlyPersonCreatedTriggersAPurchase(t *testing.T) {
	e := setupProviderConsumer(t, "automatic_on_create")
	env := personCreatedEnvelope(e.personID)
	env.Type = "person.updated"

	if err := e.consumer.HandleEvent(context.Background(), env); err != nil {
		t.Fatal(err)
	}
	if runs := e.runsForPerson(t); runs != 0 {
		t.Error("an edit bought data: every typo fixed on a contact would charge the customer again")
	}
}
