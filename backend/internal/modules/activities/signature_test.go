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
