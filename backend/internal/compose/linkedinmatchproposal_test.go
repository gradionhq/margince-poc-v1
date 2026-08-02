// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// What a staged LinkedIn match is allowed to say.
//
// The payload is read by whoever can decide the proposal, so what it carries is
// a disclosure decision rather than a mapping. These need no database: they are
// claims about the shape, and a test that had to seed a workspace to check a
// field list would be testing the seeding.

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/modules/people"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

func TestTheProposalCarriesTheExportsSpellingAndNotTheFoldedForms(t *testing.T) {
	// A human judges "is this the same person" on what LinkedIn actually said.
	// The folded strings the matcher compared on — lowercased, unaccented,
	// legal-suffix stripped — cannot be judged by anybody and must not travel:
	// nobody can decide "andreas muller · simio".
	m := people.PendingLinkedInMatch{
		ConnectionID: ids.NewV7(), PersonID: ids.NewV7(),
		ConnectionName: "André Schultewolter", ConnectionCompany: "SIMIO GmbH & Co. KG",
		PersonName: "Andre Schultewolter",
	}
	payload, err := json.Marshal(linkedInMatchProposal{
		ConnectionID: m.ConnectionID, PersonID: m.PersonID,
		ConnectionName: m.ConnectionName, ConnectionCompany: m.ConnectionCompany,
		PersonName: m.PersonName,
	})
	if err != nil {
		t.Fatalf("marshalling the proposal: %v", err)
	}
	var fields map[string]any
	if err := json.Unmarshal(payload, &fields); err != nil {
		t.Fatalf("unreadable proposal: %v", err)
	}
	want := []string{"connection_id", "person_id", "connection_name", "connection_company", "person_name"}
	for _, key := range want {
		if _, ok := fields[key]; !ok {
			t.Errorf("the proposal omits %s — the inbox cannot render the question without it", key)
		}
	}
	if len(fields) != len(want) {
		t.Errorf("the proposal carries %d fields (%v), want exactly %v — a ghost's other "+
			"details are nobody's business who is only deciding an identity", len(fields), fields, want)
	}
	if strings.Contains(string(payload), "andré schultewolter") ||
		strings.Contains(string(payload), "simio gmbh") {
		t.Errorf("the proposal carries a folded form: %s", payload)
	}
}

func TestAConnectionWithNoEmployerStillReadsAsASentence(t *testing.T) {
	// LinkedIn's company field is member-edited and often empty. A summary
	// reading "Andreas Müller at  looks like Andreas Müller" is the kind of
	// thing that makes a queue feel broken.
	if got := employerOrPlaceholder(""); got == "" || strings.TrimSpace(got) == "" {
		t.Errorf("an empty employer renders as %q, leaving a hole in the summary", got)
	}
	if got := employerOrPlaceholder("Acme GmbH"); got != "Acme GmbH" {
		t.Errorf("a real employer was rewritten to %q", got)
	}
}

func TestStagingRefusesAContextWithNoHumanBehindIt(t *testing.T) {
	// The proposal records WHOSE export raised the question, and that record is
	// what the audit trail answers "who asked" with. A context carrying no
	// human cannot stage: writing the proposal anyway would put a question in
	// somebody's inbox that no trail attributes to anyone.
	if _, err := withGhostOwnerAsSubject(context.Background()); !errors.Is(err, apperrors.ErrPermissionDenied) {
		t.Errorf("staging without an actor answered %v, want a permission refusal", err)
	}

	// With a member bound, the subject IS that member.
	me := ids.NewV7()
	ctx, err := withGhostOwnerAsSubject(principal.WithActor(context.Background(),
		principal.Principal{Type: principal.PrincipalHuman, UserID: me}))
	if err != nil {
		t.Fatalf("staging as a member: %v", err)
	}
	actor, ok := principal.Actor(ctx)
	if !ok || actor.OnBehalfOf != me {
		t.Errorf("the staging subject is %v, want the acting member %s", actor.OnBehalfOf, me)
	}
}
