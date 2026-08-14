// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package activities

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

type stubSignature struct {
	body    string
	err     error
	askedID ids.UUID
}

func (s *stubSignature) SignatureFor(_ context.Context, userID ids.UUID) (string, error) {
	s.askedID = userID
	return s.body, s.err
}

func humanCtx(userID ids.UUID) context.Context {
	return principal.WithActor(context.Background(), principal.Principal{
		Type: principal.PrincipalHuman, ID: "human:" + userID.String(), UserID: userID,
	})
}

// The sign-off goes under the message the rep wrote, separated by a blank line
// — which is what makes it read as theirs rather than as another paragraph.
func TestASignatureIsAppendedBeneathTheMessage(t *testing.T) {
	user := ids.NewV7()
	store := (&Store{}).WithSignature(&stubSignature{body: "Marek Janetzke\nGradion"})

	got, err := store.signedBody(humanCtx(user), "Shall we say Tuesday at 10?")
	if err != nil {
		t.Fatalf("signing the body failed: %v", err)
	}
	if got != "Shall we say Tuesday at 10?\n\nMarek Janetzke\nGradion" {
		t.Fatalf("unexpected signed body:\n%q", got)
	}
}

// The separator is a blank line, never the "-- " sig-dash. This product's own
// reply parser treats that dash as a signature boundary and cuts everything
// below it, so writing one would make our captured copy of the thread end at
// the signature we just added.
func TestTheSeparatorIsNotASigDash(t *testing.T) {
	store := (&Store{}).WithSignature(&stubSignature{body: "Marek"})

	got, err := store.signedBody(humanCtx(ids.NewV7()), "Body")
	if err != nil {
		t.Fatalf("signing the body failed: %v", err)
	}
	if strings.Contains(got, "\n-- \n") || strings.Contains(got, "\n--\n") {
		t.Fatalf("the signature was introduced by a sig-dash:\n%q", got)
	}
}

// Unsigned is the honest state for a member who never wrote one, and it is what
// every message did before this existed. It must not become a blank block.
func TestAnEmptySignatureLeavesTheBodyExactlyAsWritten(t *testing.T) {
	for name, sign := range map[string]string{
		"never written": "",
		"only spaces":   "   \n  ",
	} {
		t.Run(name, func(t *testing.T) {
			store := (&Store{}).WithSignature(&stubSignature{body: sign})
			got, err := store.signedBody(humanCtx(ids.NewV7()), "Body")
			if err != nil {
				t.Fatalf("signing the body failed: %v", err)
			}
			if got != "Body" {
				t.Fatalf("the body gained something: %q", got)
			}
		})
	}
}

// A role wired without the seam sends unsigned rather than refusing to send.
func TestNoSignatureReaderSendsUnsigned(t *testing.T) {
	got, err := (&Store{}).signedBody(humanCtx(ids.NewV7()), "Body")
	if err != nil {
		t.Fatalf("signing the body failed: %v", err)
	}
	if got != "Body" {
		t.Fatalf("the body changed with no reader wired: %q", got)
	}
}

// An agent acts under a human's authority but is not that human. A tool-written
// message arriving under somebody's personal sign-off claims a hand that never
// touched it, so the agent path asks for no signature at all.
func TestAnAgentSendSignsNothing(t *testing.T) {
	reader := &stubSignature{body: "Marek Janetzke"}
	store := (&Store{}).WithSignature(reader)
	ctx := principal.WithActor(context.Background(), principal.Principal{
		Type: principal.PrincipalAgent, ID: "agent:assistant", UserID: ids.NewV7(),
	})

	got, err := store.signedBody(ctx, "Body")
	if err != nil {
		t.Fatalf("signing the body failed: %v", err)
	}
	if got != "Body" {
		t.Fatalf("an agent send was signed: %q", got)
	}
	if reader.askedID != ids.Nil {
		t.Fatal("an agent send asked for a signature it may not use")
	}
}

// The signature is read for the AUTHENTICATED sender and nobody else, which is
// what keeps one member's sign-off off another member's mail.
func TestTheSignatureIsReadForTheAuthenticatedSender(t *testing.T) {
	user := ids.NewV7()
	reader := &stubSignature{body: "Marek"}
	store := (&Store{}).WithSignature(reader)

	if _, err := store.signedBody(humanCtx(user), "Body"); err != nil {
		t.Fatalf("signing the body failed: %v", err)
	}
	if reader.askedID != user {
		t.Fatalf("asked for %s, expected the sender %s", reader.askedID, user)
	}
}

// A read that fails is not silently swallowed: sending a message the sender
// believes is signed, unsigned, is a change to what they put their name to.
func TestAFailedSignatureReadRefusesTheSend(t *testing.T) {
	boom := errors.New("database is down")
	store := (&Store{}).WithSignature(&stubSignature{err: boom})

	if _, err := store.signedBody(humanCtx(ids.NewV7()), "Body"); !errors.Is(err, boom) {
		t.Fatalf("expected the read error to surface, got %v", err)
	}
}

// The markup alternative carries the SAME sign-off as the plain one. Two parts
// of a message that disagreed would be two messages, and which one a recipient
// reads is their client's decision rather than ours.
func TestTheMarkupAlternativeCarriesTheSameSignOff(t *testing.T) {
	store := (&Store{}).WithSignature(&stubSignature{body: "Marek Janetzke\nGradion"})

	got, err := store.signedHTML(humanCtx(ids.NewV7()), "<p>Shall we say Tuesday?</p>", sendDeliverability{})
	if err != nil {
		t.Fatalf("signing the markup failed: %v", err)
	}
	if !strings.Contains(got, "Marek Janetzke<br>Gradion") {
		t.Fatalf("the markup lost the sign-off or its line break: %q", got)
	}
}

// The signature is stored as plain text and reaches a markup document, so it is
// escaped. A member whose sign-off contains "Weiß & Konrad <Recht>" must not
// have it become a broken tag, and one who typed a script tag must not have it
// run in the recipient's client.
func TestASignatureCannotInjectMarkup(t *testing.T) {
	store := (&Store{}).WithSignature(&stubSignature{
		body: `Weiß & Konrad <Recht><script>alert(1)</script>`,
	})

	got, err := store.signedHTML(humanCtx(ids.NewV7()), "<p>Body</p>", sendDeliverability{})
	if err != nil {
		t.Fatalf("signing the markup failed: %v", err)
	}
	if strings.Contains(got, "<script>") {
		t.Fatalf("a signature injected live markup: %q", got)
	}
	if !strings.Contains(got, "Wei&#223; &amp; Konrad") && !strings.Contains(got, "Weiß &amp; Konrad") {
		t.Fatalf("the signature was not escaped as text: %q", got)
	}
}

// A message with no markup stays single-part. Manufacturing an HTML alternative
// would make every plain send multipart for no reader's benefit.
func TestNoMarkupBodyProducesNoMarkupAlternative(t *testing.T) {
	store := (&Store{}).WithSignature(&stubSignature{body: "Marek"})

	got, err := store.signedHTML(humanCtx(ids.NewV7()), "", sendDeliverability{})
	if err != nil {
		t.Fatalf("signing the markup failed: %v", err)
	}
	if got != "" {
		t.Fatalf("a plain-text send gained a markup part: %q", got)
	}
}

// Both parts must offer the unsubscribe link, from the SAME token: whether a
// recipient can unsubscribe must not depend on which alternative their client
// chose to render.
func TestBothPartsCarryTheUnsubscribeSurface(t *testing.T) {
	derived := sendDeliverability{
		unsubURL:  "https://app.test/v1/public/unsubscribe/tok",
		manageURL: "https://app.test/v1/public/preferences/tok",
	}
	store := (&Store{}).WithSignature(&stubSignature{})

	got, err := store.signedHTML(humanCtx(ids.NewV7()), "<p>Body</p>", derived)
	if err != nil {
		t.Fatalf("signing the markup failed: %v", err)
	}
	if !strings.Contains(got, derived.unsubURL) || !strings.Contains(got, derived.manageURL) {
		t.Fatalf("the markup part carries no unsubscribe surface: %q", got)
	}
}

type stubSenderName struct {
	name string
	err  error
}

func (s *stubSenderName) ActorIdentity(context.Context) (string, string, error) {
	return s.name, "", s.err
}

// The name reaches the send when identity knows it.
func TestTheSenderNameIsResolvedForTheSend(t *testing.T) {
	store := (&Store{}).WithSenderName(&stubSenderName{name: "Lars Jankowfsky"})

	got, err := store.senderDisplayName(humanCtx(ids.NewV7()))
	if err != nil {
		t.Fatalf("resolving the sender name failed: %v", err)
	}
	if got != "Lars Jankowfsky" {
		t.Fatalf("expected the sender's name, got %q", got)
	}
}

// A role wired without the seam sends a bare address rather than refusing.
func TestNoSenderNameReaderSendsUnnamed(t *testing.T) {
	got, err := (&Store{}).senderDisplayName(humanCtx(ids.NewV7()))
	if err != nil {
		t.Fatalf("resolving the sender name failed: %v", err)
	}
	if got != "" {
		t.Fatalf("a store with no reader produced a name: %q", got)
	}
}

// A name that cannot be read is not a reason to refuse a send: the message is
// correct without it, and trading a cosmetic gap for a delivery failure is the
// worse answer. The error still surfaces rather than being swallowed.
func TestAFailedSenderNameReadSurfaces(t *testing.T) {
	boom := errors.New("identity is unreachable")
	store := (&Store{}).WithSenderName(&stubSenderName{err: boom})

	if _, err := store.senderDisplayName(humanCtx(ids.NewV7())); !errors.Is(err, boom) {
		t.Fatalf("expected the read error to surface, got %v", err)
	}
}
