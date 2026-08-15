// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package compose

import (
	"context"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/compose/integration"
	"github.com/gradionhq/margince/backend/internal/modules/activities"
	"github.com/gradionhq/margince/backend/internal/modules/capture"
	"github.com/gradionhq/margince/backend/internal/modules/comms"
	"github.com/gradionhq/margince/backend/pkg/extension"
)

// A core connector not already seeded is inserted, with transport='core'.
func TestReconcileChannelProvidersInsertsAnUnseenProvider(t *testing.T) {
	e := integration.Setup(t)
	ctx := context.Background()
	owner := integration.OwnerConn(t)
	// Restore the pre-registry default in BOTH packages' in-memory snapshots:
	// reconcileChannelProviders below sets them to this test's own composed
	// set, and a later test in this process must not see it.
	defer activities.SetChannelProviders([]string{capture.ProviderTelegram})
	defer comms.SetChannelProviders([]string{capture.ProviderTelegram})
	// Deleting the channel_provider row keeps the shared test database tidy for
	// the tests that follow. The activity_kind row is deliberately NOT cleaned
	// up: nothing should have written one, and the assertion below is what says
	// so — a defensive delete here would tidy away the evidence of a regression.
	t.Cleanup(func() {
		if _, err := owner.Exec(context.Background(), `DELETE FROM channel_provider WHERE provider = 'fake_core_channel'`); err != nil {
			t.Errorf("cleaning up channel_provider: %v", err)
		}
	})

	// telegram is already seeded by the core migration; assert the reconcile is
	// a no-op for it and inserts a genuinely new one — standing in for "core
	// ships a second channel connector" without adding one.
	if err := reconcileChannelProviders(ctx, e.Pool, []string{"telegram", "fake_core_channel"}); err != nil {
		t.Fatalf("reconcileChannelProviders: %v", err)
	}

	var transport string
	if err := owner.QueryRow(ctx,
		`SELECT transport FROM channel_provider WHERE provider = 'fake_core_channel'`).Scan(&transport); err != nil {
		t.Fatalf("querying the inserted row: %v", err)
	}
	if transport != "core" {
		t.Fatalf("transport = %q, want core", transport)
	}

	// The mirror, and the point of the whole reconcile change: registering a
	// transport must NOT mint an interaction kind. A provider says how a message
	// travelled; an activity kind says what sort of interaction it was. While
	// those two shared a vocabulary, boot had to insert an activity_kind row for
	// every provider just to satisfy a foreign key — and the moment the kind
	// vocabulary narrows to its semantic members, that insert would put back
	// exactly the rows the narrowing removed, on the next boot, for as long as
	// nobody noticed.
	var kindExists bool
	if err := owner.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM activity_kind WHERE kind = 'fake_core_channel')`).Scan(&kindExists); err != nil {
		t.Fatalf("querying activity_kind: %v", err)
	}
	if kindExists {
		t.Fatal("reconcileChannelProviders minted an activity_kind row for a provider — a transport is not an interaction kind, " +
			"and a boot that keeps inserting these will silently restore whatever the kind-narrowing migration removes")
	}

	// Idempotent: calling it again with the SAME set changes nothing and
	// errors on nothing — a role that constructs the registry twice (the
	// worker's one-shot backfill helper does) must not fail its second call.
	if err := reconcileChannelProviders(ctx, e.Pool, []string{"telegram", "fake_core_channel"}); err != nil {
		t.Fatalf("reconcileChannelProviders, second call: %v", err)
	}
}

// A provider name the registry's own grammar refuses fails the reconcile
// loudly, rather than being skipped into a half-composed installation. The
// grammar lives on the column itself — the channel_provider_provider_grammar
// constraint — so this is also the proof that boot keeps no second, disagreeing
// spelling of the rule.
func TestReconcileChannelProvidersRefusesAProviderNameTheGrammarRejects(t *testing.T) {
	e := integration.Setup(t)
	ctx := context.Background()
	defer activities.SetChannelProviders([]string{capture.ProviderTelegram})
	defer comms.SetChannelProviders([]string{capture.ProviderTelegram})

	// Hyphens and capitals are both outside ^[a-z][a-z0-9_]*$ — and this exact
	// spelling was a test fixture in this file before the grammar existed, which
	// is how a name no installation could ever register got used as the stand-in
	// for one that could.
	err := reconcileChannelProviders(ctx, e.Pool, []string{"fake-core-channel"})
	if err == nil {
		t.Fatal("reconcileChannelProviders accepted a provider name the channel_provider grammar refuses; " +
			"a name that cannot be stored must fail at boot, not become a provider the registry silently lacks")
	}

	var exists bool
	if qErr := integration.OwnerConn(t).QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM channel_provider WHERE provider = 'fake-core-channel')`).Scan(&exists); qErr != nil {
		t.Fatalf("querying channel_provider: %v", qErr)
	}
	if exists {
		t.Fatal("the refused provider was stored anyway")
	}
}

// A provider whose supplier is gone on a LATER boot is kept, never deleted —
// activity and person_channel_identity rows may still reference it.
func TestReconcileChannelProvidersNeverDeletesARetiredRow(t *testing.T) {
	e := integration.Setup(t)
	ctx := context.Background()
	// reconcileChannelProviders below sets BOTH packages' in-memory snapshots
	// to the empty set this test passes it; restore the pre-registry default
	// so a later test in this process does not see telegram deregistered.
	defer activities.SetChannelProviders([]string{capture.ProviderTelegram})
	defer comms.SetChannelProviders([]string{capture.ProviderTelegram})

	if err := reconcileChannelProviders(ctx, e.Pool, []string{}); err != nil {
		t.Fatalf("reconcileChannelProviders with an empty composed set: %v", err)
	}

	var count int
	if err := integration.OwnerConn(t).QueryRow(ctx,
		`SELECT count(*) FROM channel_provider WHERE provider = 'telegram'`).Scan(&count); err != nil {
		t.Fatalf("querying channel_provider: %v", err)
	}
	if count != 1 {
		t.Fatalf("telegram's row was deleted when it dropped out of the composed set (count=%d)", count)
	}
}

// A UNIT's declared channel becomes a registry row of its own — and until it
// does, no message can reference it: activity.channel_provider is a foreign key
// into this table, so a captured chat message would be refused by the database
// rather than landing under an unregistered name.
//
// What the row SAYS is the other half. transport='unit' is what the send path
// branches on to resolve a per-member credential instead of the workspace's bot,
// and credential_model='per_member' is what an operator reads to know that
// connecting is each member's own act rather than an admin's one-time binding.
func TestReconcileChannelProvidersRegistersAUnitsDeclaredTransport(t *testing.T) {
	e := integration.Setup(t)
	ctx := context.Background()
	owner := integration.OwnerConn(t)
	defer activities.SetChannelProviders([]string{capture.ProviderTelegram})
	defer comms.SetChannelProviders([]string{capture.ProviderTelegram})
	declaresTransport(t, "mine", extension.Channel{
		Provider: "mine_chat", Send: (&capturedSend{}).send, Live: answersLive(true, nil),
	})
	t.Cleanup(func() {
		if _, err := owner.Exec(context.Background(), `DELETE FROM channel_provider WHERE provider = 'mine_chat'`); err != nil {
			t.Errorf("cleaning up channel_provider: %v", err)
		}
	})

	if err := reconcileChannelProviders(ctx, e.Pool, []string{capture.ProviderTelegram}); err != nil {
		t.Fatalf("reconcileChannelProviders: %v", err)
	}

	var transport, credentialModel, label string
	var supplies bool
	if err := owner.QueryRow(ctx,
		`SELECT transport, credential_model, label, supplies_transport
		   FROM channel_provider WHERE provider = 'mine_chat'`).
		Scan(&transport, &credentialModel, &label, &supplies); err != nil {
		t.Fatalf("querying the unit's row: %v", err)
	}
	switch {
	case transport != "unit":
		t.Errorf("transport = %q, want unit — the send path branches on it to resolve a per-member credential", transport)
	case credentialModel != "per_member":
		t.Errorf("credential_model = %q, want per_member — a unit holds one sealed secret per member and no installation credential", credentialModel)
	case label != "Mine Chat":
		t.Errorf("label = %q, want a name derived from the id — this endpoint is readable by every seat, so nothing an operator typed belongs in it", label)
	case !supplies:
		t.Errorf("supplies_transport = false for a channel that declares a Send")
	}

	// And the in-memory half, which is what actually lets a reply leave: the send
	// path's own pre-flight reads this set, so a unit transport left out of it
	// would register, publish, capture — and park every reply a rep wrote under
	// "this installation cannot send on that", which is the one failure a rep
	// cannot tell from a broken provider.
	if !activities.CanSendOnProvider("mine_chat") {
		t.Error("the unit's transport is not in the sendable set; every reply on it would be refused before it was staged")
	}
	// Without narrowing the core's: a unit declaring a channel must not
	// deregister the workspace's own bot.
	if !activities.CanSendOnProvider(capture.ProviderTelegram) {
		t.Error("registering a unit transport dropped the core connector out of the sendable set")
	}
}

// A capture-only unit registers its provider and is NOT sendable. The two are
// separate columns because the difference is real and a rep sees it: the
// timeline can name what carried a message on a transport nothing can answer on.
func TestReconcileChannelProvidersRegistersACaptureOnlyUnitTransportAsUnsendable(t *testing.T) {
	e := integration.Setup(t)
	ctx := context.Background()
	owner := integration.OwnerConn(t)
	defer activities.SetChannelProviders([]string{capture.ProviderTelegram})
	defer comms.SetChannelProviders([]string{capture.ProviderTelegram})
	declaresTransport(t, "mine", extension.Channel{Provider: "mine_chat"})
	t.Cleanup(func() {
		if _, err := owner.Exec(context.Background(), `DELETE FROM channel_provider WHERE provider = 'mine_chat'`); err != nil {
			t.Errorf("cleaning up channel_provider: %v", err)
		}
	})

	if err := reconcileChannelProviders(ctx, e.Pool, []string{capture.ProviderTelegram}); err != nil {
		t.Fatalf("reconcileChannelProviders: %v", err)
	}

	var supplies bool
	if err := owner.QueryRow(ctx,
		`SELECT supplies_transport FROM channel_provider WHERE provider = 'mine_chat'`).Scan(&supplies); err != nil {
		t.Fatalf("querying the unit's row: %v", err)
	}
	if supplies {
		t.Error("a channel declaring no Send was registered as supplying transport")
	}
	if activities.CanSendOnProvider("mine_chat") {
		t.Error("a capture-only transport is in the sendable set; a reply would be staged against a unit that cannot transmit")
	}
}

// A unit SHADOWING a core connector fails the boot, and this is the sharpest
// failure the whole surface has: every Telegram reply a rep wrote would leave on
// the unit's per-member credential instead of the workspace's bot — the same
// message, sent by a different person, with nothing on the screen different.
//
// It is refused HERE rather than in the extension preflight because this is the
// first point at which both sets exist: the core's transports are decided when
// the capture registry is built, which can happen after extension registration,
// so the preflight would answer from an empty set and pass the collision it
// exists to catch.
func TestReconcileChannelProvidersRefusesAUnitThatShadowsACoreConnector(t *testing.T) {
	e := integration.Setup(t)
	ctx := context.Background()
	defer activities.SetChannelProviders([]string{capture.ProviderTelegram})
	defer comms.SetChannelProviders([]string{capture.ProviderTelegram})
	declaresTransport(t, "impostor", extension.Channel{
		Provider: capture.ProviderTelegram, Send: (&capturedSend{}).send, Live: answersLive(true, nil),
	})

	err := reconcileChannelProviders(ctx, e.Pool, []string{capture.ProviderTelegram})

	if err == nil {
		t.Fatal("a unit declaring the core's own transport was accepted; every reply on it would leave under the unit's credential")
	}
	if !strings.Contains(err.Error(), capture.ProviderTelegram) {
		t.Errorf("the refusal %q does not name the transport in dispute, which is the one thing an operator must rename", err)
	}
	// And it refuses BEFORE writing anything: a boot that dies with the row
	// already re-pointed would leave the collision installed.
	var transport string
	if qErr := integration.OwnerConn(t).QueryRow(ctx,
		`SELECT transport FROM channel_provider WHERE provider = $1`, capture.ProviderTelegram).Scan(&transport); qErr != nil {
		t.Fatalf("querying channel_provider: %v", qErr)
	}
	if transport != "core" {
		t.Errorf("telegram's transport is now %q; the refused reconcile rewrote the row it was refusing", transport)
	}
}

// A unit may not seize a transport the REGISTRY reserves for the core, even
// when this binary composed no connector for it — and `whatsapp` is exactly
// that shape: registered by migration so a hand-logged WhatsApp message can say
// what carried it, with no Go connector behind it.
//
// It is the case a composed-set check misses, and it shipped that way once.
// `capture.Registry.ChannelProviders` returns only connectors implementing the
// message seam, so a unit declaring `whatsapp` passed the collision check, the
// upsert re-pointed the core row at the unit, and every previously-unrepliable
// WhatsApp conversation in the installation became one the unit transmits — on
// its own credential, with nothing on any screen different.
func TestReconcileChannelProvidersRefusesAUnitThatSeizesARegisteredCoreTransport(t *testing.T) {
	e := integration.Setup(t)
	ctx := context.Background()
	defer activities.SetChannelProviders([]string{capture.ProviderTelegram})
	defer comms.SetChannelProviders([]string{capture.ProviderTelegram})
	// whatsapp is deliberately NOT in the composed set below, which is the
	// truth about this binary: nothing composes a WhatsApp connector.
	declaresTransport(t, "impostor", extension.Channel{
		Provider: "whatsapp", Send: (&capturedSend{}).send, Live: answersLive(true, nil),
	})

	err := reconcileChannelProviders(ctx, e.Pool, []string{capture.ProviderTelegram})

	if err == nil {
		t.Fatal("a unit seized a registered core transport it composed no connector for; every hand-logged conversation on it became repliable by the unit")
	}
	var transport string
	var supplies bool
	if qErr := integration.OwnerConn(t).QueryRow(ctx,
		`SELECT transport, supplies_transport FROM channel_provider WHERE provider = 'whatsapp'`).
		Scan(&transport, &supplies); qErr != nil {
		t.Fatalf("querying channel_provider: %v", qErr)
	}
	if transport != "core" {
		t.Errorf("whatsapp's transport is now %q; the refused reconcile rewrote the row it was refusing", transport)
	}
	if supplies {
		t.Error("whatsapp is now marked as supplying transport; this installation composes no connector for it")
	}
	if activities.CanSendOnProvider("whatsapp") {
		t.Error("whatsapp entered the sendable set, so every hand-logged WhatsApp conversation would now accept a reply")
	}
}

// The seam SHIPS, not just the function: ReconcileChannelProviders — the boot
// step a process role calls — registers a composed unit's transport and makes
// it sendable, on a role that builds NO capture registry. That is the write
// twin of TestTheDirectoryLoadsFromTheRegistryAndNotFromTheCaptureBoot, and it
// is the shape the whole step exists for: building the capture registry is
// gated on a configured keyvault, and a vault-less role must still register
// what it composed.
//
// It asks about a UNIT transport rather than telegram on purpose. Telegram's
// row is seeded by the core migration and testdb does not reset
// channel_provider, and both in-memory sets carry telegram as their
// compile-time default — so every telegram assertion here is already true
// before the act, and would pass against a boot step that did nothing at all.
// A unit's row can only have been written by this call.
func TestTheBootStepRegistersAComposedUnitTransportWithNoCaptureRegistry(t *testing.T) {
	e := integration.Setup(t)
	ctx := context.Background()
	owner := integration.OwnerConn(t)
	defer activities.SetChannelProviders([]string{capture.ProviderTelegram})
	defer comms.SetChannelProviders([]string{capture.ProviderTelegram})
	declaresTransport(t, "boot", extension.Channel{
		Provider: "boot_chat", Send: (&capturedSend{}).send, Live: answersLive(true, nil),
	})
	t.Cleanup(func() {
		if _, err := owner.Exec(context.Background(),
			`DELETE FROM channel_provider WHERE provider = 'boot_chat'`); err != nil {
			t.Errorf("cleaning up channel_provider: %v", err)
		}
	})
	emptyTheSendableSets(t)

	// Through RecordComposition, which is what a process role actually calls —
	// reaching past it to the reconcile alone would leave the step every role
	// wires untested, and the ordering it owns unproven.
	if err := RecordComposition(ctx, e.Pool, discardLog(), ComposedExtensions()); err != nil {
		t.Fatalf("the boot step on a role that composes no capture registry: %v", err)
	}
	// The inventory half landed too: both facts follow from the same composed
	// set, and a step that recorded one of them is the half-wired boot this
	// function exists to make unreachable.
	if n := observedExtensionCount(t, owner); n == 0 {
		t.Error("the boot step registered the transports but recorded no extension inventory")
	}

	var transport string
	if err := owner.QueryRow(ctx,
		`SELECT transport FROM channel_provider WHERE provider = 'boot_chat'`).Scan(&transport); err != nil {
		t.Fatalf("reading back the unit transport the boot step should have registered: %v", err)
	}
	if transport != "unit" {
		t.Errorf("boot_chat registered as transport %q, want unit", transport)
	}
	// Both in-memory halves, refilled from empty: the send pre-flight reads
	// them, so a transport missing here is one every reply is refused on.
	if !activities.CanSendOnProvider("boot_chat") {
		t.Error("the unit's transport is not in activities' sendable set")
	}
	if _, capability := comms.SendScopeFor("boot_chat"); capability == comms.CannotSend {
		t.Error("the unit's transport is not in comms' sendable set")
	}
	if !activities.CanSendOnProvider(capture.ProviderTelegram) {
		t.Error("the core connector did not come back into the sendable set")
	}
}

// Reconcile runs over an infra transaction, never a workspace-bound one: the
// boot step runs BEFORE the installation is bootstrapped, when no organization
// exists. A workspace-bound transaction cannot resolve which workspace to bind,
// which halts every fresh install rather than some corner of one.
func TestTheBootStepReconcilesBeforeTheInstallationIsBootstrapped(t *testing.T) {
	e := integration.Setup(t)
	ctx := context.Background()
	owner := integration.OwnerConn(t)
	defer activities.SetChannelProviders([]string{capture.ProviderTelegram})
	defer comms.SetChannelProviders([]string{capture.ProviderTelegram})
	declaresTransport(t, "prebootstrap", extension.Channel{
		Provider: "prebootstrap_chat", Send: (&capturedSend{}).send, Live: answersLive(true, nil),
	})
	t.Cleanup(func() {
		if _, err := owner.Exec(context.Background(),
			`DELETE FROM channel_provider WHERE provider = 'prebootstrap_chat'`); err != nil {
			t.Errorf("cleaning up channel_provider: %v", err)
		}
	})
	emptyTheSendableSets(t)

	// Archived, not deleted: identity.Service.InstallationWorkspace counts
	// un-archived workspaces, and integration.Setup's own fixture rows
	// (app_user, team) FK-reference this workspace with ON DELETE RESTRICT, so
	// a DELETE here would fail on the wrong constraint before ever reaching
	// the one this test is about.
	if _, err := owner.Exec(ctx, `UPDATE workspace SET archived_at = now()`); err != nil {
		t.Fatalf("archiving every workspace to simulate a pre-bootstrap install: %v", err)
	}

	if err := ReconcileChannelProviders(ctx, e.Pool); err != nil {
		t.Fatalf("the boot step with no organization bootstrapped yet: %v", err)
	}
	if !activities.CanSendOnProvider("prebootstrap_chat") {
		t.Error("the boot step did not reconcile with no organization bootstrapped yet")
	}
}

// A boot step that cannot register the vocabulary aborts the boot, and says
// which of its two halves failed. The refusal itself is proved by the reconcile
// suite above; what matters here is that RecordComposition surfaces it rather
// than swallowing it, and that an operator reading one line knows whether the
// inventory or the transports were what went wrong.
func TestRecordingTheCompositionFailsTheBootWhenAUnitShadowsACoreTransport(t *testing.T) {
	e := integration.Setup(t)
	ctx := context.Background()
	defer activities.SetChannelProviders([]string{capture.ProviderTelegram})
	defer comms.SetChannelProviders([]string{capture.ProviderTelegram})
	declaresTransport(t, "impostor", extension.Channel{
		Provider: capture.ProviderTelegram, Send: (&capturedSend{}).send, Live: answersLive(true, nil),
	})

	err := RecordComposition(ctx, e.Pool, discardLog(), ComposedExtensions())

	if err == nil {
		t.Fatal("a boot whose unit declares the core's own transport was allowed to continue; " +
			"every reply on that transport would leave under the unit's credential")
	}
	if !strings.Contains(err.Error(), "channel vocabulary") {
		t.Errorf("the boot failure %q does not say which step failed, so an operator cannot tell "+
			"a transport collision from an inventory problem", err)
	}
	if !strings.Contains(err.Error(), capture.ProviderTelegram) {
		t.Errorf("the boot failure %q does not name the transport in dispute, which is the one thing "+
			"an operator must rename", err)
	}
}

// observedExtensionCount answers how many extension-composition observations
// the installation holds — the inventory half of RecordComposition, read from
// the trail it actually writes.
func observedExtensionCount(t *testing.T, owner *pgx.Conn) int {
	t.Helper()
	var n int
	if err := owner.QueryRow(context.Background(),
		`SELECT count(*) FROM system_log WHERE action = $1`, extensionCompositionObserved).Scan(&n); err != nil {
		t.Fatalf("counting the recorded extension observations: %v", err)
	}
	return n
}

// emptyTheSendableSets clears what the assertions are about to look at, and
// fails if the clearing did not take. Both packages carry telegram as a
// compile-time default and testdb does not reset channel_provider, so a test
// that does not start from empty cannot tell a boot step that ran from one that
// did nothing.
func emptyTheSendableSets(t *testing.T) {
	t.Helper()
	activities.SetChannelProviders(nil)
	comms.SetChannelProviders(nil)
	if activities.CanSendOnProvider(capture.ProviderTelegram) {
		t.Fatal("the sendable set survived being emptied — this run could not tell a reconcile from a no-op")
	}
}
