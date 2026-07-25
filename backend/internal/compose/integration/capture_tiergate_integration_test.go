// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// The tiered creation gate (ADR-0072 §1) — which senders become records. Split
// from the auto-create suite because the tiers are their own subject: one file
// proves what each tier decides in isolation, the other what a capture writes.

import "testing"

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
	})
	t.Run("transactional infrastructure keeps the activity, derives no counterparty", func(t *testing.T) {
		// A DocuSign envelope (exact infra eSLD, no corroboration needed) and a
		// conference blast on a prefix subdomain WITH a List-Unsubscribe header
		// (corroborated) both suppress person+org while the timeline row stands
		// (ADR-0072/A118, CAP-PARAM-6).
		sync(t,
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
	t.Run("a conference blast WITHOUT corroboration is a normal counterparty", func(t *testing.T) {
		// The same prefix subdomain, but no List-Unsubscribe and a human
		// localpart: not suppressed — a real company can live at event.*.
		sync(t, email("ada@event.realco.example", "Ada Real", captureOwner, "rc1@event.realco.example", ""))
		if n := countRows(t, e, `
			SELECT count(*) FROM person p JOIN person_email pe ON pe.person_id = p.id
			WHERE pe.email = 'ada@event.realco.example'`); n != 1 {
			t.Fatal("an uncorroborated prefix sender must create a normal person")
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
