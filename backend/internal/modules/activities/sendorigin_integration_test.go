// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package activities

// An account-started send over a real migrated Postgres (ADR-0087 §1): it
// reaches the SAME governed path a reply reaches, roots its own thread, and
// files itself on the records the caller named — each of which is row-scope
// probed at insert, so a caller cannot link a message onto a record they
// may not see.

import (
	"context"
	"errors"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// seedOrganization writes a company as the table owner — the record a rep
// opens when they press "Write email", which the send path did not create.
func (e *sendEnv) seedOrganization(t *testing.T) ids.UUID {
	t.Helper()
	id := ids.NewV7()
	if _, err := e.owner.Exec(context.Background(),
		`INSERT INTO organization (id, workspace_id, display_name, owner_id, source, captured_by)
		 VALUES ($1, $2, 'Buyer GmbH', $3, 'manual', 'human:x')`,
		id, e.ws, e.rep); err != nil {
		t.Fatalf("seeding the organization: %v", err)
	}
	return id
}

func accountOrigin(org ids.UUID) SendOrigin {
	return FromAccount([]ActivityLinkInput{{EntityType: "organization", EntityID: org}})
}

// The whole point of the origin: a message with no anchor still sends, and
// the timeline row it leaves behind is filed under the company it was
// started from rather than under nothing.
func TestAnAccountStartedSendFilesItselfOnTheRecordItWasStartedFrom(t *testing.T) {
	e := setupSend(t)
	org := e.seedOrganization(t)
	stager := &recordingStager{}

	sent, err := e.store(stubUnsubscribeLinker{}).SendEmail(
		e.as(principal.RowScopeAll), accountOrigin(org), sendInput("transactional"), stubConsentGate{}, stager)
	if err != nil {
		t.Fatalf("account-started SendEmail: %v", err)
	}

	if sent.Links == nil || len(*sent.Links) != 1 {
		t.Fatalf("account-started send wrote links %v, want exactly the organization it was started from", sent.Links)
	}
	link := (*sent.Links)[0]
	if string(link.EntityType) != "organization" || ids.UUID(link.EntityId) != org {
		t.Fatalf("link = %s/%s, want organization/%s", link.EntityType, link.EntityId, org)
	}
	if sent.SourceId == nil {
		t.Fatal("account-started send carries no source_id; the provider's echo would create a second timeline row")
	}
}

// A new conversation has no ancestry to carry, and its root is its own
// identity — the key capture derives when it reads that root back out of
// the mailbox, which is what lets a reply to this message join this thread.
func TestAnAccountStartedSendRootsAFreshThreadAtItsOwnIdentity(t *testing.T) {
	e := setupSend(t)
	org := e.seedOrganization(t)
	stager := &recordingStager{}

	sent, err := e.store(stubUnsubscribeLinker{}).SendEmail(
		e.as(principal.RowScopeAll), accountOrigin(org), sendInput("transactional"), stubConsentGate{}, stager)
	if err != nil {
		t.Fatalf("account-started SendEmail: %v", err)
	}

	staged := stager.only(t)
	if staged.InReplyTo != "" || len(staged.References) != 0 {
		t.Fatalf("a message that answers nothing staged In-Reply-To %q / References %v, want both empty",
			staged.InReplyTo, staged.References)
	}
	if staged.ThreadKey != staged.MessageID {
		t.Fatalf("thread key = %q, want this message's own identity %q (a root is its own key)",
			staged.ThreadKey, staged.MessageID)
	}
	if got := e.storedThreadKey(t, ids.UUID(sent.Id)); got != staged.MessageID {
		t.Fatalf("stored thread_key = %q, want %q — a reply-detection join reads the stored key", got, staged.MessageID)
	}
}

// The links arrive from the caller rather than from a record the send path
// already read, so the row-scope probe at insert is the only thing standing
// between a guessed UUID and a message filed on someone else's company.
func TestAnAccountStartedSendRefusesALinkTheCallerCannotSee(t *testing.T) {
	e := setupSend(t)
	stager := &recordingStager{}
	// A well-formed id for a company that exists in no workspace this caller
	// can reach — the shape a guessed or cross-tenant identifier takes.
	unseen := ids.NewV7()

	_, err := e.store(stubUnsubscribeLinker{}).SendEmail(
		e.as(principal.RowScopeAll), accountOrigin(unseen), sendInput("transactional"), stubConsentGate{}, stager)

	if !errors.Is(err, apperrors.ErrNotFound) {
		t.Fatalf("send linked to an invisible record = %v, want ErrNotFound (existence-hiding)", err)
	}
	if len(stager.staged) != 0 {
		t.Fatalf("a refused send staged %d deliveries, want none", len(stager.staged))
	}
}

// An origin nobody named is a wiring defect, and it must refuse rather than
// resolve to "no anchor" — which is exactly the silent new conversation
// ADR-0087 rejects making the anchor merely optional to avoid.
func TestASendWithNoOriginRefusesRatherThanStartingAConversation(t *testing.T) {
	e := setupSend(t)
	stager := &recordingStager{}

	_, err := e.store(stubUnsubscribeLinker{}).SendEmail(
		e.as(principal.RowScopeAll), SendOrigin{}, sendInput("transactional"), stubConsentGate{}, stager)

	var noOrigin *NoSendOriginError
	if !errors.As(err, &noOrigin) {
		t.Fatalf("send with no origin = %v, want NoSendOriginError", err)
	}
	if len(stager.staged) != 0 {
		t.Fatalf("a send with no origin staged %d deliveries, want none", len(stager.staged))
	}
}

// The reply path's guard order is the invariant the refactor most easily
// breaks: authorization must refuse before the consent gate answers, and
// that has to hold on the new origin too, or an account-started send
// becomes a way to learn a stranger's consent state.
func TestAnAccountStartedSendRefusesOnAuthorizationBeforeConsentAnswers(t *testing.T) {
	e := setupSend(t)
	org := e.seedOrganization(t)
	gate := &countingConsentGate{}

	_, err := e.store(stubUnsubscribeLinker{}).SendEmail(
		e.readOnly(), accountOrigin(org), sendInput("transactional"), gate, &recordingStager{})

	if !errors.Is(err, apperrors.ErrPermissionDenied) {
		t.Fatalf("account-started send without create = %v, want ErrPermissionDenied", err)
	}
	if gate.calls != 0 {
		t.Fatalf("the consent gate answered %d times for a caller who may not send; authorization must refuse first", gate.calls)
	}
}

// countingConsentGate grants every request and counts being asked, so a
// test can assert the gate was never REACHED — a gate that refuses would
// prove nothing about ordering, since the send fails either way.
type countingConsentGate struct {
	stubConsentGate
	calls int
}

func (g *countingConsentGate) RequireGrantedForEmails(context.Context, []string, string) error {
	g.calls++
	return nil
}
