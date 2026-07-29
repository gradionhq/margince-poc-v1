// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package activities

// What the one send path REFUSES, and what it must attach for a mailbox
// provider to accept the message. The harness and the stubs both files ride
// live in email_integration_test.go, which covers the other half: what a send
// writes to the timeline.

import (
	"errors"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// RFC 8058 deliverability is derived on the send path itself, not in one
// transport: the MCP send_email tool reaches this store method directly, and
// marketing mail without a List-Unsubscribe header is what gets a domain
// filtered.
func TestSendEmailDerivesUnsubscribeHeadersForAMarketingPurpose(t *testing.T) {
	e := setupSend(t)
	anchor := e.seedAnchor(t, "", "")
	stager := &recordingStager{}
	linker := stubUnsubscribeLinker{token: testUnsubscribeTok, ok: true}

	sent, err := e.store(linker).SendEmail(
		e.as(principal.RowScopeAll), anchor, soloSendInput("marketing_email"), stubConsentGate{}, stager)
	if err != nil {
		t.Fatalf("SendEmail: %v", err)
	}

	wantURL := testBaseURL + "/v1/public/preferences/" + testUnsubscribeTok + "/unsubscribe?purpose=marketing_email"
	staged := stager.only(t)
	if staged.ListUnsubscribe != "<"+wantURL+">" {
		t.Fatalf("staged List-Unsubscribe = %q, want the bracketed one-click URL <%s>", staged.ListUnsubscribe, wantURL)
	}
	// Header and footer derive from the SAME token and URL, so a recipient's
	// visible link can never point somewhere the machine header does not.
	if !strings.Contains(staged.Body, wantURL) {
		t.Fatalf("staged body carries no visible unsubscribe link:\n%s", staged.Body)
	}
	if !strings.Contains(staged.Body, testBaseURL+"/v1/public/preferences/"+testUnsubscribeTok+"\n") &&
		!strings.HasSuffix(staged.Body, testBaseURL+"/v1/public/preferences/"+testUnsubscribeTok) {
		t.Fatalf("staged body carries no manage-preferences link:\n%s", staged.Body)
	}
	// The timeline records what actually went out, footer included.
	if sent.Body == nil || !strings.Contains(*sent.Body, wantURL) {
		t.Fatalf("logged activity body does not match the transmitted body: %v", sent.Body)
	}
}

// A transactional message has nothing to unsubscribe from — the linker
// declines to mint a token for a locked purpose — so it carries no header and
// its body is left exactly as the sender wrote it.
func TestSendEmailDerivesNoUnsubscribeHeadersForATransactionalPurpose(t *testing.T) {
	e := setupSend(t)
	anchor := e.seedAnchor(t, "", "")
	stager := &recordingStager{}
	linker := stubUnsubscribeLinker{token: testUnsubscribeTok, ok: false}

	if _, err := e.store(linker).SendEmail(
		e.as(principal.RowScopeAll), anchor, sendInput("transactional"), stubConsentGate{}, stager); err != nil {
		t.Fatalf("SendEmail: %v", err)
	}

	staged := stager.only(t)
	if staged.ListUnsubscribe != "" {
		t.Fatalf("transactional send carries List-Unsubscribe %q, want none", staged.ListUnsubscribe)
	}
	if staged.Body != "As discussed." {
		t.Fatalf("transactional body = %q, want the sender's text untouched", staged.Body)
	}
}

// A preference token is a bearer credential over ONE person's consent record —
// it reads their state, withdraws, and grants — and a single rendered message
// carries a single token to every addressee. Sending that message to a second
// person hands them the first recipient's credential, so the send is refused
// before anything is staged.
func TestSendEmailRefusesAMultiAddresseeSendThatCarriesAnUnsubscribeToken(t *testing.T) {
	e := setupSend(t)
	anchor := e.seedAnchor(t, "", "")
	stager := &recordingStager{}
	linker := stubUnsubscribeLinker{token: testUnsubscribeTok, ok: true}

	// sendInput addresses buyer@ and cc's boss@ — two people, one token.
	_, err := e.store(linker).SendEmail(
		e.as(principal.RowScopeAll), anchor, sendInput("marketing_email"), stubConsentGate{}, stager)
	var refusal *SharedUnsubscribeTokenError
	if !errors.As(err, &refusal) {
		t.Fatalf("multi-addressee marketing send → %v, want a SharedUnsubscribeTokenError", err)
	}
	if !strings.Contains(refusal.Error(), "once per recipient") {
		t.Fatalf("refusal %q does not tell the user what to do about it", refusal.Error())
	}
	if len(stager.staged) != 0 || e.outboundCount(t) != 0 {
		t.Fatal("a refused send still staged a delivery or logged an activity")
	}
}

// …and the refusal is about the TOKEN, not about the recipient count: a
// transactional send mints none, so it reaches as many addressees as the caller
// listed. Refusing those too would break every ordinary reply-all.
func TestSendEmailAcceptsAMultiAddresseeSendThatCarriesNoUnsubscribeToken(t *testing.T) {
	e := setupSend(t)
	anchor := e.seedAnchor(t, "", "")
	stager := &recordingStager{}
	linker := stubUnsubscribeLinker{token: testUnsubscribeTok, ok: false}

	if _, err := e.store(linker).SendEmail(
		e.as(principal.RowScopeAll), anchor, sendInput("transactional"), stubConsentGate{}, stager); err != nil {
		t.Fatalf("multi-addressee transactional send → %v, want acceptance", err)
	}
	staged := stager.only(t)
	if staged.ListUnsubscribe != "" {
		t.Fatalf("a transactional send carries List-Unsubscribe %q — then it should have been refused", staged.ListUnsubscribe)
	}
	if len(staged.Cc) != 1 {
		t.Fatalf("staged cc = %v, want the addressee the caller listed", staged.Cc)
	}
}

// The activity and its delivery are one fact. A staging failure that still
// left the activity behind would promise the user a send that was never
// queued, on a timeline they have no way to correct.
func TestSendEmailCommitsNoActivityWhenStagingFails(t *testing.T) {
	e := setupSend(t)
	anchor := e.seedAnchor(t, "", "")
	stager := &recordingStager{err: errors.New("delivery table unavailable")}

	_, err := e.store(stubUnsubscribeLinker{}).SendEmail(
		e.as(principal.RowScopeAll), anchor, sendInput("transactional"), stubConsentGate{}, stager)
	if err == nil {
		t.Fatal("SendEmail reported success though staging refused")
	}
	if n := e.outboundCount(t); n != 0 {
		t.Fatalf("%d outbound activities survived a failed staging, want 0 (one transaction, one fact)", n)
	}
}

// Accepting mail we already know cannot leave hands the user a 202 and a
// silently parked delivery they cannot see. Every mailbox connected before
// the send grant existed holds read-only access, so the check must ask about
// the GRANT — "is something connected?" would pass all of them.
func TestSendEmailRefusesWhenTheMailboxHoldsNoSendGrant(t *testing.T) {
	e := setupSend(t)
	anchor := e.seedAnchor(t, "", "")
	stager := &recordingStager{}
	store := e.store(stubUnsubscribeLinker{}).WithMailbox(stubMailbox{capable: false})

	_, err := store.SendEmail(e.as(principal.RowScopeAll), anchor, sendInput("transactional"), stubConsentGate{}, stager)
	var refusal *MailboxNotSendCapableError
	if !errors.As(err, &refusal) {
		t.Fatalf("send with no send-capable mailbox → %v, want a MailboxNotSendCapableError", err)
	}
	if !strings.Contains(refusal.Error(), "reconnect") {
		t.Fatalf("refusal %q does not tell the user what to do about it", refusal.Error())
	}
	if len(stager.staged) != 0 || e.outboundCount(t) != 0 {
		t.Fatal("a refused send still staged a delivery or logged an activity")
	}
}

// The staged delivery names the anchor, and naming a record is a read: an
// anchor outside the caller's row scope refuses with the same answer a
// missing one gives, before anything is staged.
func TestSendEmailRefusesAnAnchorOutsideTheCallersRowScope(t *testing.T) {
	e := setupSend(t)
	anchor := e.seedAnchor(t, "", "")
	e.linkToPersonOwnedBy(t, anchor, e.other)
	stager := &recordingStager{}

	_, err := e.store(stubUnsubscribeLinker{}).SendEmail(
		e.as(principal.RowScopeOwn), anchor, sendInput("transactional"), stubConsentGate{}, stager)
	if !errors.Is(err, apperrors.ErrNotFound) {
		t.Fatalf("send anchored to another rep's activity → %v, want ErrNotFound (existence-hiding)", err)
	}
	if len(stager.staged) != 0 || e.outboundCount(t) != 0 {
		t.Fatal("a send refused at the row-scope gate still staged a delivery or logged an activity")
	}
}

// The whole send path rests on one ordering: AUTHORIZATION REFUSES BEFORE
// ANYTHING ELSE ANSWERS. A caller with no rights over the anchor is owed the
// row-scope answer and nothing more — a 500 naming this installation's delivery
// wiring tells them the send path exists and how it is composed, which is a
// fact about the deployment they reached by pointing at a record they may not
// read.
func TestSendEmailAnswersAnUnauthorizedCallerBeforeTheWiringGuards(t *testing.T) {
	e := setupSend(t)
	anchor := e.seedAnchor(t, "", "")
	e.linkToPersonOwnedBy(t, anchor, e.other)

	// Composed with NO delivery machinery: the wiring guard would fire on this
	// call if it ran first.
	_, err := e.store(stubUnsubscribeLinker{}).SendEmail(
		e.as(principal.RowScopeOwn), anchor, sendInput("transactional"), stubConsentGate{}, nil)
	if errors.Is(err, errNoDeliveryStager) {
		t.Fatal("an unauthorized caller learned the send path has no delivery machinery wired")
	}
	if !errors.Is(err, apperrors.ErrNotFound) {
		t.Fatalf("send anchored outside the caller's row scope → %v, want ErrNotFound (existence-hiding)", err)
	}
}

// The same ordering for the OBJECT grant: a caller who may read the anchor but
// not create an activity gets the denial, not the composition state.
func TestSendEmailAnswersAMissingCreateGrantBeforeTheWiringGuards(t *testing.T) {
	e := setupSend(t)
	anchor := e.seedAnchor(t, "", "")

	_, err := e.store(stubUnsubscribeLinker{}).SendEmail(
		e.readOnly(), anchor, sendInput("transactional"), stubConsentGate{}, nil)
	if errors.Is(err, errNoDeliveryStager) {
		t.Fatal("a caller with no create grant learned the send path has no delivery machinery wired")
	}
	if !errors.Is(err, apperrors.ErrPermissionDenied) {
		t.Fatalf("send without a create grant → %v, want ErrPermissionDenied", err)
	}
}

// …and once the caller IS authorized, the wiring guard is what refuses: a send
// nothing will ever transmit must not leave a timeline entry claiming a message
// went out.
func TestSendEmailRefusesAnAuthorizedSendWithNoDeliveryMachinery(t *testing.T) {
	e := setupSend(t)
	anchor := e.seedAnchor(t, "", "")

	_, err := e.store(stubUnsubscribeLinker{}).SendEmail(
		e.as(principal.RowScopeAll), anchor, sendInput("transactional"), stubConsentGate{}, nil)
	if !errors.Is(err, errNoDeliveryStager) {
		t.Fatalf("send with no delivery stager → %v, want errNoDeliveryStager", err)
	}
	if n := e.outboundCount(t); n != 0 {
		t.Fatalf("%d outbound activities survived a refused send, want 0", n)
	}
}

// The sender's OWN authority answers before the recipients' consent does. A
// user who holds no send grant gets the refusal they can act on — "reconnect
// your mailbox" — rather than a verdict about whether the people they addressed
// consented, which is a fact about those people they did not earn the right to
// observe by attempting a send they cannot make.
func TestSendEmailAnswersTheMailboxRefusalBeforeTheConsentGate(t *testing.T) {
	e := setupSend(t)
	anchor := e.seedAnchor(t, "", "")
	stager := &recordingStager{}
	gate := &recordingConsentGate{err: apperrors.ErrConsentNotGranted}
	store := e.store(stubUnsubscribeLinker{}).WithMailbox(stubMailbox{capable: false})

	_, err := store.SendEmail(e.as(principal.RowScopeAll), anchor, sendInput("transactional"), gate, stager)
	var refusal *MailboxNotSendCapableError
	if !errors.As(err, &refusal) {
		t.Fatalf("send with no send grant → %v, want a MailboxNotSendCapableError", err)
	}
	if gate.consulted {
		t.Error("the consent gate answered for a caller who may not send at all")
	}
}

// An anchor that is not mail carries no RFC822 identity, so the send starts a
// conversation instead of threading onto an identifier no mail client can
// resolve. Emitting a calendar system's opaque id as In-Reply-To breaks the
// header for every recipient.
func TestSendEmailThreadsOntoNothingWhenTheAnchorIsNotMail(t *testing.T) {
	e := setupSend(t)
	anchor := e.seedNonMailAnchor(t, "evt-88231@google.com")
	stager := &recordingStager{}

	sent, err := e.store(stubUnsubscribeLinker{}).SendEmail(
		e.as(principal.RowScopeAll), anchor, sendInput("transactional"), stubConsentGate{}, stager)
	if err != nil {
		t.Fatalf("SendEmail: %v", err)
	}
	staged := stager.only(t)
	if staged.InReplyTo != "" || len(staged.References) != 0 {
		t.Fatalf("staged In-Reply-To=%q References=%v from a calendar anchor; those headers resolve to no message",
			staged.InReplyTo, staged.References)
	}
	// A message that answers nothing is its own thread root, keyed on the
	// identity it was minted with — the key capture derives when it reads the
	// sent copy back out of the mailbox.
	if sent.SourceId == nil || staged.ThreadKey != *sent.SourceId {
		t.Fatalf("staged thread key = %q, want this message's own identity %v", staged.ThreadKey, sent.SourceId)
	}
}

// Send and capture key the same column. The send writes thread_key at write
// time; capture's echo of the same natural key is an ON CONFLICT DO NOTHING
// upsert, which the log path answers by returning the existing row untouched
// — so neither leg can overwrite the other's value.
func TestReplayingASourceKeyLeavesTheStoredThreadKeyUntouched(t *testing.T) {
	e := setupSend(t)
	ctx := e.as(principal.RowScopeAll)
	store := NewStore(e.pool)
	system, sourceID := "gmail", "replayed@buyer.test"

	first, created, err := store.LogActivity(ctx, LogActivityInput{
		Kind: "email", Source: "manual", SourceSystem: &system, SourceID: &sourceID,
		ThreadKey: "root@buyer.test",
	})
	if err != nil || !created {
		t.Fatalf("first log: %v (created=%v)", err, created)
	}
	second, created, err := store.LogActivity(ctx, LogActivityInput{
		Kind: "email", Source: "gmail", SourceSystem: &system, SourceID: &sourceID,
		ThreadKey: "someone-elses-root@buyer.test",
	})
	if err != nil {
		t.Fatalf("replayed log: %v", err)
	}
	if created {
		t.Fatal("replaying a source key created a second activity")
	}
	if second.Id != first.Id {
		t.Fatalf("replay returned activity %s, want the existing %s", second.Id, first.Id)
	}
	if got := e.storedThreadKey(t, ids.UUID(first.Id)); got != "root@buyer.test" {
		t.Fatalf("stored thread_key = %q after a replay, want the value the first write set", got)
	}
}
