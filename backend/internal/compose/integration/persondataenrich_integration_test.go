// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// The automatic-enrichment consumer end to end: a person.created envelope
// reaches it and a run exists for that person — admitted, fenced, frozen and
// reserved, with the submit job committed beside it.
//
// WHO created the person decides which of the customer's two toggles governs
// the purchase, and whether there is one at all. That is the subject of most
// of these cases: the same event is emitted by four writers, and an agent
// creating a contact must not reach through it what the REST policy denies
// the agent at the door.

import (
	"context"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/compose"
	"github.com/gradionhq/margince/backend/internal/modules/integrations"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/keyvault"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/events"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/ports/provider"
)

type providerConsumerEnv struct {
	env      *Env
	consumer *compose.PersonDataEnrich
	personID ids.UUID
	enqueued *int
}

// setupProviderConsumer seeds a subject and a connected provider in the named
// mode, and wires the consumer over the REAL cross-module binding.
func setupProviderConsumer(t *testing.T, mode string) *providerConsumerEnv {
	t.Helper()
	e := Setup(t)
	c := &providerConsumerEnv{env: e, enqueued: new(int), personID: seedSubject(t, e)}

	if err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(context.Background(), `
			INSERT INTO provider_connection
			       (id, provider, status, mode, preset, categories, automatic_individual_create)
			VALUES (gen_random_uuid(), 'surfe', 'connected', $1, 'full',
			        ARRAY['professional_email'], true)
			ON CONFLICT (provider) DO UPDATE
			   SET status = 'connected', mode = $1, automatic_individual_create = true`, mode)
		return err
	}); err != nil {
		t.Fatal(err)
	}

	reg, err := integrations.NewRegistry(integrations.NewOfflineProvider(0, time.Now))
	if err != nil {
		t.Fatal(err)
	}
	store, err := integrations.NewStore(e.DB(), keyvault.NewMemory(), reg, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	// The real binding, plus a counting enqueue: a queued run with no job is
	// the failure QueueRun refuses to commit, and only a counter proves it.
	bound := compose.BindProviderDomain(store).WithSubmitEnqueue(
		func(context.Context, pgx.Tx, string, string) error {
			*c.enqueued++
			return nil
		})
	c.consumer = compose.NewPersonDataEnrich(e.Pool, bound, slog.New(slog.NewTextHandler(os.Stderr, nil)))
	return c
}

// humanCreated is what the relay delivers for a person a HUMAN typed — the
// individual create the automatic_individual_create toggle governs.
func humanCreated(personID ids.UUID) events.Envelope {
	return actorEnvelope(personID, "human", "human:"+ids.NewV7().String())
}

func actorEnvelope(personID ids.UUID, actorType, actorID string) events.Envelope {
	return events.Envelope{
		EventID:    ids.NewV7(),
		Type:       "person.created",
		OccurredAt: time.Now().UTC(),
		Actor:      events.Actor{Type: actorType, ID: actorID},
		Entity:     events.EntityRef{Type: "person", ID: personID},
		Trace:      events.Trace{CorrelationID: ids.NewV7()},
	}
}

func (c *providerConsumerEnv) runsForPerson(t *testing.T) int {
	t.Helper()
	var runs int
	if err := database.WithWorkspaceTx(c.env.Admin(), c.env.Pool, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(),
			`SELECT count(*) FROM provider_run WHERE person_id = $1`, c.personID).Scan(&runs)
	}); err != nil {
		t.Fatal(err)
	}
	return runs
}

// A human creating a contact, with the toggle on: the event buys the data.
func TestPersonCreatedQueuesAnEnrichmentRun(t *testing.T) {
	c := setupProviderConsumer(t, "automatic_on_create")

	if err := c.consumer.HandleEvent(context.Background(), humanCreated(c.personID)); err != nil {
		t.Fatal(err)
	}

	// Read directly, and BEFORE anything else queues: the consumer swallows
	// configuration refusals by design, so a probe that queued its own run
	// first would create the row this asserts and pass even if HandleEvent
	// did nothing at all.
	var state, skipReason string
	if err := database.WithWorkspaceTx(c.env.Admin(), c.env.Pool, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(),
			`SELECT state, coalesce(skip_reason, '') FROM provider_run WHERE person_id = $1`,
			c.personID).Scan(&state, &skipReason)
	}); err != nil {
		t.Fatalf("the consumer queued nothing for a newly created person: %v", err)
	}
	if skipReason != "" {
		t.Fatalf("the run was skipped (%s) rather than queued — the fixture trips a fence it was not meant to", skipReason)
	}
	if state != string(provider.RunQueued) {
		t.Errorf("the run is %s, want queued — the consumer must not call the provider on the event lane", state)
	}
	if *c.enqueued != 1 {
		t.Errorf("%d submit jobs were committed, want 1 — a queued run with no job sits in the live-run index forever, blocking every later attempt at that subject", *c.enqueued)
	}
}

// Auto-enrich switched off: the refusal is the configuration working, so the
// consumer reports success and buys nothing.
func TestPersonCreatedWithAutoEnrichOffBuysNothingAndDoesNotError(t *testing.T) {
	c := setupProviderConsumer(t, "on_demand")

	if err := c.consumer.HandleEvent(context.Background(), humanCreated(c.personID)); err != nil {
		t.Fatalf("a switched-off toggle surfaced as a failure, which would wedge the consumer group: %v", err)
	}
	if runs := c.runsForPerson(t); runs != 0 {
		t.Errorf("%d runs exist with auto-enrich off — the customer's credits were spent against their own setting", runs)
	}
}

// Buying provider data is human-only on the REST surface (x-agent-access,
// ADR-0055). An agent that can create a person must not reach through this
// event what the policy denies it at the door: the seat cap is what the
// agent's human could do through the same gate, and an event has no gate.
func TestAnAgentCreatedPersonBuysNothing(t *testing.T) {
	c := setupProviderConsumer(t, "automatic_on_create")

	if err := c.consumer.HandleEvent(context.Background(),
		actorEnvelope(c.personID, "agent", "agent:overnight")); err != nil {
		t.Fatal(err)
	}
	if runs := c.runsForPerson(t); runs != 0 {
		t.Errorf("an agent-created person triggered %d paid runs — an agent holding only createPerson just spent the customer's credits through a route the policy makes human-only", runs)
	}
}

// Capture creates a person per external counterparty, so a mailbox connect
// with a year of history would buy thousands of records. That is what
// automatic_import governs — off by default, with a preview and an estimate
// behind it — and it is NOT the individual-create toggle.
func TestAConnectorCreatedPersonIsGovernedByTheImportToggle(t *testing.T) {
	c := setupProviderConsumer(t, "automatic_on_create")

	if err := c.consumer.HandleEvent(context.Background(),
		actorEnvelope(c.personID, "connector", "connector:gmail")); err != nil {
		t.Fatal(err)
	}
	if runs := c.runsForPerson(t); runs != 0 {
		t.Errorf("a captured counterparty triggered %d paid runs under automatic_import=false — connecting a mailbox would buy data for every sender in its history", runs)
	}
}

// Only person.created buys. An edit must not re-purchase.
func TestOnlyPersonCreatedTriggersAPurchase(t *testing.T) {
	c := setupProviderConsumer(t, "automatic_on_create")
	env := humanCreated(c.personID)
	env.Type = "person.updated"

	if err := c.consumer.HandleEvent(context.Background(), env); err != nil {
		t.Fatal(err)
	}
	if runs := c.runsForPerson(t); runs != 0 {
		t.Errorf("an edit bought data (%d runs): every typo fixed on a contact would charge the customer again", runs)
	}
}
