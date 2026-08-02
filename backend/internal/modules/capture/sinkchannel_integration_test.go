// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package capture_test

// The channel record's route through the Sink (telegram-oa design §6.4): it
// reaches the channel ensure seam, and it leaves behind none of the artifacts
// the mail tier ladder writes about a sender it judged.
//
// That pairing is the whole assertion. Before the channel path existed a
// domainless record simply derived nothing, so "no ledger row, no breadcrumb"
// alone would have passed on a record nothing happened to; and if the mail
// ladder were ever widened to swallow channel records, its address-keyed
// deferral would fail inside the gate savepoint and leave exactly such an
// artifact behind. The fourth gate — the impersonation quarantine — lives in
// people and is proved there (TestQuarantineSuspectNeedsADomainToJudge).

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/gradionhq/margince/backend/internal/modules/capture"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/connector"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
)

// recordingChannelEnsurer stands in for compose's people adapter — the true
// module boundary. It records what the Sink asked for, which is the only way to
// prove the channel seam was reached at all.
type recordingChannelEnsurer struct {
	mu       sync.Mutex
	requests []capture.EnsureChannelRequest
}

func (r *recordingChannelEnsurer) EnsureChannelCounterparty(_ context.Context, req capture.EnsureChannelRequest) (capture.EnsureOutcome, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.requests = append(r.requests, req)
	return capture.EnsureOutcome{PersonCreated: true}, nil
}

func (r *recordingChannelEnsurer) seen() []capture.EnsureChannelRequest {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]capture.EnsureChannelRequest(nil), r.requests...)
}

// refusingMailEnsurer is wired so the mail seam is present and fails loudly if
// a channel record is ever handed to it: the two contracts are not
// interchangeable, and a record arriving on the wrong one would silently create
// a person with no identity binding.
type refusingMailEnsurer struct{ t *testing.T }

func (m refusingMailEnsurer) EnsureCounterparty(_ context.Context, in capture.EnsureRequest) (capture.EnsureOutcome, error) {
	m.t.Errorf("the mail resolver was asked to ensure %+v; a channel record has no address for it to resolve", in)
	return capture.EnsureOutcome{}, nil
}

func TestChannelRecordSkipsEveryMailDomainGate(t *testing.T) {
	owner, pool := setupCaptureDB(t)
	ctx := context.Background()
	ws := ids.NewV7()
	if _, err := owner.Exec(ctx,
		`INSERT INTO workspace (id, name, slug, base_currency) VALUES ($1, 'Channel Sink', $2, 'EUR')`,
		ws, "channel-sink-"+ws.String()); err != nil {
		t.Fatalf("seeding workspace: %v", err)
	}
	// The workspace's own mail domain, so the T0 colleagues gate has something
	// to find if anything ever asks it about a record with no domain.
	if _, err := owner.Exec(ctx,
		`INSERT INTO workspace_email_domain (workspace_id, domain) VALUES ($1, 'own-house.test')`,
		ws); err != nil {
		t.Fatalf("seeding the internal mail domain: %v", err)
	}

	ensurer := &recordingChannelEnsurer{}
	sink := capture.NewSink(pool).
		WithEnsurer(refusingMailEnsurer{t: t},
			capture.NewTransactionalList([]string{"sendgrid.net"}, nil)).
		WithChannelEnsurer(ensurer)

	identity := connector.ChannelIdentity{Provider: "telegram", ChannelUserID: "990101", Username: "chatty"}
	rec := connector.NormalizedRecord{
		EntityType: datasource.EntityActivity,
		NaturalKey: connector.NaturalKey{SourceSystem: "telegram", SourceID: "8100:4242:7"},
		Fields: capture.ActivityFields{
			Kind: "telegram", Body: "hello there", Direction: connector.DirectionInbound,
			OccurredAt: time.Date(2026, 7, 30, 9, 0, 0, 0, time.UTC),
		},
		Source:     "telegram:8100:4242:7",
		CapturedBy: "connector:telegram",
		Counterparty: connector.Counterparty{
			Direction: connector.DirectionInbound,
			// A name that would quarantine a mail counterparty, and no address
			// at all — the shape a channel record actually arrives in.
			DisplayName:     "ceo@real-corp.example",
			ChannelIdentity: identity,
		},
		ThreadKey: "telegram:8100:4242",
	}

	ref, err := sink.Upsert(channelSinkContext(ctx, ws), rec)
	if err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	requests := ensurer.seen()
	if len(requests) != 1 {
		t.Fatalf("the channel seam saw %d requests, want exactly 1 — an inbound channel message must derive its human", len(requests))
	}
	got := requests[0]
	if got.Identity != identity {
		t.Fatalf("request identity = %+v, want %+v", got.Identity, identity)
	}
	if got.DisplayName != rec.Counterparty.DisplayName {
		t.Fatalf("request display name = %q, want %q", got.DisplayName, rec.Counterparty.DisplayName)
	}
	if got.ActivityID != ref.ID {
		t.Fatalf("request activity = %s, want the captured activity %s", got.ActivityID, ref.ID)
	}
	if got.CapturedBy != rec.CapturedBy {
		t.Fatalf("request captured_by = %q, want the acting connector %q", got.CapturedBy, rec.CapturedBy)
	}
	if got.Source != rec.Source {
		t.Fatalf("request source = %q, want %q", got.Source, rec.Source)
	}

	// No disposition ledger row: the ledger is address-keyed, so a verdict about
	// a channel sender could only ever be a verdict about nobody.
	var dispositions int
	if err := owner.QueryRow(ctx,
		`SELECT count(*) FROM capture_pending_counterparty WHERE workspace_id = $1`, ws).Scan(&dispositions); err != nil {
		t.Fatalf("counting dispositions: %v", err)
	}
	if dispositions != 0 {
		t.Fatalf("%d disposition rows for a channel record, want 0", dispositions)
	}

	// And no gate breadcrumb of any kind — a suppression, a deferral cap, or a
	// gate fault would each be a mail gate having judged this message.
	var breadcrumbs []string
	rows, err := owner.Query(ctx,
		`SELECT action FROM system_log WHERE workspace_id = $1 ORDER BY action`, ws)
	if err != nil {
		t.Fatalf("reading the operational ledger: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var action string
		if err := rows.Scan(&action); err != nil {
			t.Fatalf("scanning a ledger row: %v", err)
		}
		breadcrumbs = append(breadcrumbs, action)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("draining the operational ledger: %v", err)
	}
	if len(breadcrumbs) != 0 {
		t.Fatalf("the capture gates left %v behind for a channel record, want nothing", breadcrumbs)
	}

	// The transactional gate, asked what the mail path would ask it: it holds a
	// populated list, and a channel record's absent domain matches nothing
	// rather than everything. The consumer-mail gate is not asked at all here —
	// it reads the workspace list on the capture transaction, and a channel
	// record never reaches that ladder.
	if suppress, reason := capture.NewTransactionalList([]string{"sendgrid.net"}, nil).
		Suppress(capture.TransactionalInput{Domain: rec.Counterparty.Domain}); suppress {
		t.Fatalf("the transactional gate suppressed an absent domain (%s)", reason)
	}
}

// channelSinkContext binds the workspace-channel connector principal the ingest
// worker mints: a connector acting for no human, permitted to create the
// activity it captures, workspace-wide.
func channelSinkContext(ctx context.Context, ws ids.UUID) context.Context {
	ctx = principal.WithWorkspaceID(ctx, ws)
	ctx = principal.WithActor(ctx, principal.Principal{
		Type: principal.PrincipalConnector,
		ID:   "connector:telegram",
		Permissions: principal.Permissions{
			RoleKeys: []string{"channel"},
			Objects: map[string]principal.ObjectGrant{
				"activity": {Create: true},
				"person":   {Create: true},
			},
			RowScope: principal.RowScopeAll,
		},
	})
	return principal.WithCorrelationID(ctx, ids.NewV7())
}
