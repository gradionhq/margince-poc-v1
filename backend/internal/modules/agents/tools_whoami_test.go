// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package agents

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// The identity every write already carried and nothing published.
//
// on_behalf_of rides every write and the audit log records it, but no tool
// answered it — so an assistant could not set owner_id on a record it was
// creating, could not filter "my deals" without asking, and had no language to
// write stored prose in.
func TestWhoamiNamesTheHumanThePassportActsFor(t *testing.T) {
	user := ids.NewV7()
	out, err := whoami{read: func(context.Context) (ActingIdentity, error) {
		return ActingIdentity{
			UserID: user, DisplayName: "Demo Admin", Email: "admin@demo.test",
			Locale: "de", Timezone: "Europe/Berlin",
		}, nil
	}}.Handle(context.Background(), nil)
	if err != nil {
		t.Fatalf("whoami answered %v, want the acting user", err)
	}
	var got WhoamiResult
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("decoding: %v", err)
	}
	if got.ActingUserID != user {
		t.Errorf("acting_user_id = %s, want %s — this is what owner_id and assignee_id take",
			got.ActingUserID, user)
	}
	if got.Locale != "de" {
		t.Errorf("locale = %q, want de — stored prose follows the reader's language", got.Locale)
	}
}

// Nobody behind the call is an answer, not an error: a system principal acts
// for no one, and a person who never chose a language has no locale. An empty
// locale must not be reported as "en", which cannot be told from a choice.
func TestWhoamiAnswersEmptyForAPrincipalWithNoHuman(t *testing.T) {
	out, err := whoami{read: func(context.Context) (ActingIdentity, error) {
		return ActingIdentity{}, nil
	}}.Handle(context.Background(), nil)
	if err != nil {
		t.Fatalf("whoami answered %v, want an empty identity", err)
	}
	var got WhoamiResult
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("decoding: %v", err)
	}
	if got.Locale != "" || got.DisplayName != "" {
		t.Errorf("answered %+v, want every field empty", got)
	}
}
