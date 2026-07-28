// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// The tiered creation gate (ADR-0072 §1) — which senders become records. Split
// from the auto-create suite because the tiers are their own subject: one file
// proves what each tier decides in isolation, the other what a capture writes.

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/database"
)

// The suppressing tiers, each on its own: a free-mail domain is a person and
// never a company, mail infrastructure is neither while its activity stands,
// and a lookalike sender no rule corroborates is an ordinary counterparty.
func TestCaptureTierGateSuppressesWhatIsNotACounterparty(t *testing.T) {
	env := newCaptureEnv(t)
	e, sync := env.e, env.sync
	t.Run("free-mail creates the person, never a company", func(t *testing.T) {
		sync(t, email("bob@gmail.com", "Bob Person", captureOwner, "b1@gmail.com", ""))
		if n := countRows(t, e, `
			SELECT count(*) FROM person p JOIN person_email pe ON pe.person_id = p.id
			WHERE pe.email = 'bob@gmail.com'`); n != 1 {
			t.Fatalf("%d persons for bob, want 1", n)
		}
		if n := countRows(t, e, `SELECT count(*) FROM organization WHERE display_name = 'gmail.com'`); n != 0 {
			t.Fatal("gmail.com must never become an organization")
		}
		// And free-mail is decided HERE, not deferred. Nothing but tier order
		// enforces that: a deferred free-mail sender would be judged later from
		// a ledger row that carries only the domain, and a `real` verdict would
		// mint the "Gmail" organization the tier just refused.
		if n := countRows(t, e, `
			SELECT count(*) FROM capture_pending_counterparty WHERE email = 'bob@gmail.com'`); n != 0 {
			t.Fatalf("%d ledger rows for a free-mail sender, want 0 — deferring one lets a later verdict mint a company from gmail.com", n)
		}
	})
	t.Run("transactional infrastructure keeps the activity, derives no counterparty", func(t *testing.T) {
		// A DocuSign envelope (exact infra eSLD, no corroboration needed) and a
		// conference blast on a prefix subdomain WITH a List-Unsubscribe header
		// (corroborated) both suppress person+org while the timeline row stands
		// (ADR-0072/A118, CAP-PARAM-6).
		sync(
			t,
			email("dse@eu.docusign.net", "DocuSign EU", captureOwner, "ds1@docusign.net", ""),
			emailWithListUnsub("hello@event.gitex.com", "GITEX", captureOwner, "gx1@event.gitex.com"),
		)
		if n := countRows(t, e, `SELECT count(*) FROM activity WHERE source_id IN ('ds1@docusign.net', 'gx1@event.gitex.com')`); n != 2 {
			t.Fatalf("%d transactional activities captured, want 2 — the timeline row must stand", n)
		}
		if n := countRows(t, e, `
			SELECT count(*) FROM person p JOIN person_email pe ON pe.person_id = p.id
			WHERE pe.email IN ('dse@eu.docusign.net', 'hello@event.gitex.com')`); n != 0 {
			t.Fatal("transactional infrastructure must derive no person")
		}
		if n := countRows(t, e, `SELECT count(*) FROM organization WHERE display_name IN ('Docusign', 'Gitex')`); n != 0 {
			t.Fatal("transactional infrastructure must derive no organization")
		}
		if n := countRows(t, e, `
			SELECT count(*) FROM system_log
			WHERE action = 'capture_transactional_suppressed' AND detail->>'source_id' IN ('ds1@docusign.net', 'gx1@event.gitex.com')`); n != 2 {
			t.Fatalf("%d transactional-suppression breadcrumbs, want 2", n)
		}
	})
	t.Run("a conference blast WITHOUT corroboration is deferred, not suppressed", func(t *testing.T) {
		// The same prefix subdomain, but no List-Unsubscribe and a human
		// localpart: T2 does not fire, because a real company can live at
		// event.*. What used to happen next was a record on sight; the sender
		// is now the ambiguous class and waits for a verdict instead.
		sync(t, email("ada@event.realco.example", "Ada Real", captureOwner, "rc1@event.realco.example", ""))

		if n := countRows(t, e, `
			SELECT count(*) FROM system_log
			WHERE action = 'capture_transactional_suppressed' AND detail->>'source_id' = 'rc1@event.realco.example'`); n != 0 {
			t.Fatal("an uncorroborated prefix sender was suppressed — a real company can live at event.*")
		}
		if n := countRows(t, e, `
			SELECT count(*) FROM capture_pending_counterparty
			WHERE email = 'ada@event.realco.example' AND status = 'pending'`); n != 1 {
			t.Fatalf("%d pending ledger rows, want 1 — an unknown sender defers rather than creating", n)
		}
		if n := countRows(t, e, `
			SELECT count(*) FROM person p JOIN person_email pe ON pe.person_id = p.id
			WHERE pe.email = 'ada@event.realco.example'`); n != 0 {
			t.Fatal("a first-time sender minted a person — ADR-0063's create-on-sight is what this amends")
		}
		if n := countRows(t, e, `SELECT count(*) FROM activity WHERE source_id = 'rc1@event.realco.example'`); n != 1 {
			t.Fatal("the activity must stand — deferring the record never drops the message")
		}
	})
}

// T1 outranks T2, and only on evidence the provider vouched for. Writing to
// someone spares their later bulk mail; a forged From:owner buys nothing; and
// outranking T2 never promotes a free-mail domain into a company.
func TestCaptureTierGateLetsCorrespondencePrecedeSuppression(t *testing.T) {
	env := newCaptureEnv(t)
	e, sync, syncSent := env.e, env.sync, env.syncSent
	t.Run("writing to an address spares its later bulk mail from suppression", func(t *testing.T) {
		// T1 runs BEFORE T2 and the order is load-bearing (ADR-0072 §1): once
		// the workspace has written to someone, their newsletter footer must not
		// turn them into infrastructure. The same address and the same
		// List-Unsubscribe corroboration that suppressed above now survive,
		// because the mailbox owner wrote to them first.
		syncSent(t, map[string]bool{"ev1@myco.example": true},
			email(captureOwner, "", "team@event.expo.example", "ev1@myco.example", ""))
		if n := countRows(t, e, `
			SELECT count(*) FROM activity
			WHERE counterparty_email = 'team@event.expo.example' AND counterparty_outbound_attested`); n != 1 {
			t.Fatalf("%d attested outbound activities, want 1 — the T1 evidence must be stamped", n)
		}

		sync(t, emailWithListUnsub("team@event.expo.example", "Expo", captureOwner, "ev2@event.expo.example"))
		if n := countRows(t, e, `
			SELECT count(*) FROM person p JOIN person_email pe ON pe.person_id = p.id
			WHERE pe.email = 'team@event.expo.example'`); n != 1 {
			t.Fatalf("%d persons for a corresponded-with sender, want 1 — T1 must spare it from T2", n)
		}
		if n := countRows(t, e, `
			SELECT count(*) FROM system_log
			WHERE action = 'capture_correspondence_spared' AND detail->>'source_id' = 'ev2@event.expo.example'`); n != 1 {
			t.Fatalf("%d spare breadcrumbs, want 1 — an overridden suppression must be as visible as a suppression", n)
		}
	})
	t.Run("a forged From:owner does not whitelist the address it names", func(t *testing.T) {
		// The attack the T1 evidence exists to refuse: inbound mail whose From
		// header claims the mailbox owner. It parses as outbound — that is all
		// direction can mean — but the provider filed it in the inbox and
		// attested nothing, so the address stays suppressed infrastructure.
		sync(t, email(captureOwner, "", "blast@sendgrid.net", "forge1@evil.example", ""))
		if n := countRows(t, e, `
			SELECT count(*) FROM activity
			WHERE counterparty_email = 'blast@sendgrid.net' AND counterparty_outbound_attested`); n != 0 {
			t.Fatal("a forged From:owner message attested correspondence — the gate reads the header, not the provider")
		}
		sync(t, emailWithListUnsub("blast@sendgrid.net", "Blast", captureOwner, "forge2@sendgrid.net"))
		if n := countRows(t, e, `
			SELECT count(*) FROM person p JOIN person_email pe ON pe.person_id = p.id
			WHERE pe.email = 'blast@sendgrid.net'`); n != 0 {
			t.Fatal("a forged From:owner whitelisted an ESP address past T2 suppression")
		}
	})
	t.Run("a corresponded-with free-mail address is still never a company", func(t *testing.T) {
		// T1 overrides T2 suppression ONLY. Free-mail's org rule is about what a
		// domain can honestly name, not about whether its sender is trusted, so
		// writing to a gmail.com address buys its owner a person and never an
		// organization called "Gmail" — the junk this ADR exists to prevent.
		syncSent(t, map[string]bool{"fm1@myco.example": true},
			email(captureOwner, "", "carol@gmail.com", "fm1@myco.example", ""))
		if n := countRows(t, e, `
			SELECT count(*) FROM person p JOIN person_email pe ON pe.person_id = p.id
			WHERE pe.email = 'carol@gmail.com'`); n != 1 {
			t.Fatalf("%d persons for carol, want 1", n)
		}
		if n := countRows(t, e, `SELECT count(*) FROM organization WHERE display_name IN ('Gmail', 'gmail.com')`); n != 0 {
			t.Fatal("a corresponded-with free-mail address minted an organization")
		}
	})
}

// The corroboration rule (CAP-PARAM-6) suppresses a prefix-subdomain sender
// only when something confirms it is bulk infrastructure — a List-Unsubscribe
// header, or a machine localpart. Both halves of ADR-0072 §1's promise hold at
// once: the derivation is suppressed AND the message still reaches the
// timeline, which is the whole reason a DocuSign envelope is worth capturing.
func TestCaptureTierGateSuppressesAMachineLocalpartWithoutLosingTheMessage(t *testing.T) {
	env := newCaptureEnv(t)
	e, sync := env.e, env.sync

	sync(t, email("no-reply@em.vendor.example", "Vendor", captureOwner, "v1@em.vendor.example", ""))

	if n := countRows(t, e, `SELECT count(*) FROM activity WHERE source_id = 'v1@em.vendor.example'`); n != 1 {
		t.Fatalf("%d activities for the vendor envelope, want 1 — the timeline row must stand", n)
	}
	if n := countRows(t, e, `
		SELECT count(*) FROM person p JOIN person_email pe ON pe.person_id = p.id
		WHERE pe.email = 'no-reply@em.vendor.example'`); n != 0 {
		t.Fatal("a machine localpart on a prefix subdomain must derive no person")
	}
	if n := countRows(t, e, `
		SELECT count(*) FROM system_log
		WHERE action = 'capture_transactional_suppressed' AND detail->>'source_id' = 'v1@em.vendor.example'`); n != 1 {
		t.Fatalf("%d suppression breadcrumbs, want 1 — the corroboration rule must be the one that fired", n)
	}
}

// A noise verdict settles only the mail it can still reach (ADR-0072 §4). Past
// that window the sender's next message is new evidence and raises its own
// question — without which one forged message would bar an address from ever
// becoming a record, while its later mail went neither judged nor hidden.
func TestCaptureTierGateReopensASenderWhoseNoiseVerdictHasAged(t *testing.T) {
	env := newCaptureEnv(t)
	e, sync := env.e, env.sync

	sync(t, email("stale@judged.example", "Judged", captureOwner, "aged1@judged.example", ""))
	resolveDispositionAged(t, e, "stale@judged.example", "noise", 30*24*time.Hour)

	sync(t, email("stale@judged.example", "Judged", captureOwner, "aged2@judged.example", ""))

	if n := countRows(t, e, `
		SELECT count(*) FROM capture_pending_counterparty
		WHERE email = 'stale@judged.example' AND status = 'pending'`); n != 1 {
		t.Fatalf("%d fresh questions for a sender whose verdict aged out, want 1", n)
	}
	// A RECENT verdict still settles the matter — the rule is about age, not
	// about ignoring the ledger.
	if n := countRows(t, e, `
		SELECT count(*) FROM system_log WHERE action = 'capture_noise_sender'`); n != 0 {
		t.Fatal("an aged-out verdict was still treated as settling the sender")
	}
}

// resolveDispositionAged puts an address's disposition into a terminal state
// resolved the given duration ago.
func resolveDispositionAged(t *testing.T, e *searchEnv, email, status string, ago time.Duration) {
	t.Helper()
	err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(context.Background(), `
			UPDATE capture_pending_counterparty
			   SET status = $2, resolved_at = now() - $3::interval,
			       next_attempt_at = NULL, claimed_until = NULL, claimed_by = NULL
			 WHERE email = $1`, email, status, ago.String())
		return err
	})
	if err != nil {
		t.Fatalf("aging the disposition: %v", err)
	}
}

// The enrich-on-capture condition itself (ADR-0072/A118 §9): a NEW company
// queues a dossier, and every later message from that company queues nothing.
//
// The second half is the one worth holding. Mail from a company the workspace
// already knows is most mail, and re-asking on each message would spend the
// day's ten reads on companies nobody learned anything new about — so the
// trigger keys on the ensure having CREATED the organization, not on a message
// having arrived from one.
func TestCaptureQueuesEnrichmentOnlyForACompanyItJustCreated(t *testing.T) {
	env := newCaptureEnv(t)
	e, sync, syncSent := env.e, env.sync, env.syncSent

	// T1 correspondence-positive is what lets a first inbound message create
	// records at all: the owner wrote to them first, attested by the provider.
	syncSent(t, map[string]bool{"out1@myco.example": true},
		email(captureOwner, "", "cto@newco.example", "out1@myco.example", ""))
	sync(t, email("cto@newco.example", "CTO", captureOwner, "in1@newco.example", ""))

	if n := countRows(t, e, `
		SELECT count(*) FROM organization o
		JOIN organization_domain d ON d.organization_id = o.id
		WHERE d.domain = 'newco.example'`); n != 1 {
		t.Fatalf("%d organizations for the corresponded-with company, want 1", n)
	}
	spentAfterCreate := countRows(t, e, `
		SELECT coalesce(sum(enqueued), 0) FROM capture_auto_enrich_budget`)
	if spentAfterCreate != 1 {
		t.Fatalf("budget spent = %d after a company was created, want 1 — the capture must queue its dossier", spentAfterCreate)
	}

	// A second message from the same company. It resolves onto the existing
	// organization, so nothing new was learned and nothing may be spent.
	sync(t, email("sales@newco.example", "Sales", captureOwner, "in2@newco.example", ""))
	if n := countRows(t, e, `
		SELECT coalesce(sum(enqueued), 0) FROM capture_auto_enrich_budget`); n != spentAfterCreate {
		t.Fatalf("budget spent = %d after mail from a company we already had, want it unchanged at %d",
			n, spentAfterCreate)
	}
}
