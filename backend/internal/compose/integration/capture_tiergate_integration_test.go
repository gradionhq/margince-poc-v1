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

	"github.com/gradionhq/margince/backend/internal/modules/activities"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/ports/connector"
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

// grantedSendGate lets the send through the consent seam: this suite is about
// the correspondence evidence a send records, not about suppression, which the
// consent suites prove on the same store method.
type grantedSendGate struct{}

func (grantedSendGate) RequireGrantedForEmails(context.Context, []string, string) error { return nil }

// The channel spelling of the same verdict: one gate answers for both
// transports, so a stub that granted one and not the other would let a suite
// pass a send the real gate refuses.
func (grantedSendGate) RequireGrantedForRecipients(context.Context, []connector.Recipient, string) error {
	return nil
}

// discardSendStager accepts the delivery so the send commits. Transmission is
// not what this suite proves; what the send WROTE is.
type discardSendStager struct{}

func (discardSendStager) StageTx(context.Context, pgx.Tx, activities.DeliveryRequest) error {
	return nil
}

// A send composed in the CRM is correspondence, and after ADR-0072's natural
// key collapse it is the ONLY record of it: the activity this send writes
// carries the key the provider's echo of the same message carries, so that
// echo's ON CONFLICT DO NOTHING upsert finds the row and writes nothing. The
// evidence the echo used to bring — who the message was with, and that the
// workspace sent it — therefore has to be written by the send itself, or a
// prospect the CRM emailed first is a stranger when they reply.
func TestCaptureTierGateHonorsCorrespondenceFromACRMOriginatedSend(t *testing.T) {
	env := newCaptureEnv(t)
	e, sync := env.e, env.sync
	ctx := e.asFullUser()

	anchorID := ids.NewV7()
	if err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(context.Background(), `
			INSERT INTO activity (id, workspace_id, kind, subject, occurred_at, source, captured_by)
			VALUES ($1, NULLIF(current_setting('app.workspace_id', true), '')::uuid,
			        'email', 'Intro', now(), 'manual', 'human:x')`, anchorID)
		return err
	}); err != nil {
		t.Fatalf("seed anchor: %v", err)
	}

	if _, err := activities.NewStore(e.Pool).SendEmail(ctx, ids.From[ids.ActivityKind](anchorID),
		activities.SendEmailInput{
			Recipients:     []string{"Team@News.Prospect.Example"},
			Subject:        "Following up",
			Body:           "Good to meet you.",
			ConsentPurpose: "transactional",
		}, grantedSendGate{}, discardSendStager{}); err != nil {
		t.Fatalf("CRM-originated send: %v", err)
	}

	// The evidence both readers of this column consult: the T1
	// correspondence-positive gate, and the noise sweep's escape hatch that
	// stops a wrongly-suppressed sender's reply from being hidden. It is
	// stored normalized, so the recipient's header casing cannot hide it.
	if n := countRows(t, e, `
		SELECT count(*) FROM activity
		WHERE counterparty_email = 'team@news.prospect.example'
		  AND direction = 'outbound' AND counterparty_outbound_attested`); n != 1 {
		t.Fatalf("%d attested outbound activities after a CRM send, want 1 — the echo will not record it later", n)
	}

	// Their reply arrives on a prefix subdomain WITH a List-Unsubscribe header
	// — the exact shape T2 suppresses for an unknown sender. Because the
	// workspace wrote to them first, T1 spares it.
	sync(t, emailWithListUnsub("team@news.prospect.example", "Prospect", captureOwner, "pr1@news.prospect.example"))
	if n := countRows(t, e, `
		SELECT count(*) FROM person p JOIN person_email pe ON pe.person_id = p.id
		WHERE pe.email = 'team@news.prospect.example'`); n != 1 {
		t.Fatalf("%d persons for a sender the CRM had written to, want 1 — a CRM send must count as correspondence", n)
	}
	if n := countRows(t, e, `
		SELECT count(*) FROM capture_pending_counterparty WHERE email = 'team@news.prospect.example'`); n != 0 {
		t.Fatalf("%d ledger rows for a corresponded-with sender, want 0 — T1 decides, nothing defers", n)
	}
}

// The enrich-on-capture condition (ADR-0072/A118 §9): mail from a company the
// workspace ALREADY has must not re-trigger enrichment. That is the half worth
// holding, because a domain the workspace has already decided about must not
// re-ask on every message — that would spend the day's reads re-answering a
// settled question.
//
// The other half — that a NEW domain DOES queue a read — is not observable here
// and this test does not claim it. Starting a read needs an ambient River
// client; no test process binds one, so the attempt fails and its budget slot is
// refunded, leaving the same counter a domain that never triggered would. Every
// production capture runs inside a River job, so the condition is exercised
// there and nowhere a test can watch it.
func TestCaptureDoesNotReEnrichACompanyItAlreadyHas(t *testing.T) {
	env := newCaptureEnv(t)
	e, sync, syncSent := env.e, env.sync, env.syncSent

	// T1 correspondence-positive is what lets a first inbound message create
	// records at all: the owner wrote to them first, attested by the provider.
	syncSent(t, map[string]bool{"out1@myco.example": true},
		email(captureOwner, "", "cto@newco.example", "out1@myco.example", ""))
	sync(t, email("cto@newco.example", "CTO", captureOwner, "in1@newco.example", ""))

	// The corresponded-with sender becomes a PERSON, and their domain becomes
	// one open company question — not a company invented from the domain label.
	// NOT is_anchor: the installation's own company is created by cold start,
	// not derived from a captured domain.
	if n := countRows(t, e, `SELECT count(*) FROM organization WHERE NOT is_anchor`); n != 0 {
		t.Fatalf("%d organizations from an unjudged domain, want 0", n)
	}
	if n := countRows(t, e, `
		SELECT count(*) FROM organization_domain_disposition
		WHERE domain = 'newco.example' AND status = 'pending'`); n != 1 {
		t.Fatalf("%d open company questions for newco.example, want exactly 1", n)
	}

	// A second message from the same company lands on the question that is
	// already open — one row, one crawl, however many colleagues write in.
	sync(t, email("sales@newco.example", "Sales", captureOwner, "in2@newco.example", ""))
	if n := countRows(t, e, `
		SELECT count(*) FROM organization_domain_disposition WHERE domain = 'newco.example'`); n != 1 {
		t.Fatalf("%d questions after a second message, want the one that was already open", n)
	}
	if n := countRows(t, e, `
		SELECT coalesce(sum(enqueued), 0) FROM capture_auto_enrich_budget`); n != 0 {
		t.Fatalf("budget spent = %d, want 0 — nothing here started a read, so nothing may stay reserved", n)
	}
}
