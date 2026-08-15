// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// What the subject-rights engines owe a message NOBODY HAS SENT YET.
//
// A scheduled send is the one shape of outbound mail that has no activity and
// no delivery row, so every projection built on `activity → comms_outbound`
// misses it by construction. That makes these three cases their own concern
// rather than more scheduling tests: each is about a route the sent-message
// path cannot take.
//
//   - Art. 17 erasure must empty AND stop a pending message. Anonymizing the
//     recipient while the mail still goes out would send erased data.
//   - Art. 15 export must carry it, because a message written to somebody and
//     not yet sent is data held about them.
//   - The blind-copy rule pulls both ways at once: a bcc'd subject sees their
//     own message and must not learn who else was blind-copied. Only a fixture
//     with several blind recipients can show both halves.

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/compose"
	"github.com/gradionhq/margince/backend/internal/compose/integration/apptest"
	"github.com/gradionhq/margince/backend/internal/modules/activities"
	"github.com/gradionhq/margince/backend/internal/modules/privacy"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

func TestErasingARecipientEmptiesAndStopsTheirScheduledMail(t *testing.T) {
	p := setupPreflight(t)
	p.connect(t, gmailReadonlyScope, gmailSendScope)

	// A message written the night before, addressed to the person who is about
	// to exercise Art. 17.
	id := p.scheduleFor(t, time.Now().Add(12*time.Hour))

	personID, err := ids.Parse(p.personID)
	if err != nil {
		t.Fatalf("person id %q: %v", p.personID, err)
	}
	if err := privacy.NewEraser(compose.InstallationDB(p.Pool)).ErasePerson(
		p.privacyAdmin(t), personID, "art-17"); err != nil {
		t.Fatalf("erasing the recipient: %v", err)
	}

	// The payload must no longer name them, and the message must no longer be
	// waiting to go out: a scheduled row survives with a live timer, so an
	// emptied-but-pending one would still fire the morning after the erasure
	// certified this person's data destroyed.
	status, _ := p.scheduledStatus(t, id)
	if status != activities.ScheduledStatusCancelled {
		t.Fatalf("a scheduled message to an erased person reads %q, want %q — it still has a timer",
			status, activities.ScheduledStatusCancelled)
	}
	var payload string
	if err := apptest.InWorkspace(p.AppEnv, t, p.Slug, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(),
			`SELECT payload::text FROM scheduled_send WHERE id = $1`, id).Scan(&payload)
	}); err != nil {
		t.Fatalf("reading the frozen payload: %v", err)
	}
	if strings.Contains(payload, "buyer@preflight.test") {
		t.Fatalf("the erased person's address survives in a scheduled message: %s", payload)
	}
	if strings.Contains(payload, "Written the night before.") {
		t.Fatalf("the body of a message to an erased person survives: %s", payload)
	}
}

// Art. 15 owes the subject the data held about them, and a message somebody
// wrote to them and has not sent is data held about them. It reaches the export
// by a route the sent-message projection cannot take: a scheduled send has no
// activity and no delivery row, so all three of that query's clauses miss it.
func TestASubjectAccessExportCarriesTheMailNobodyHasSentYet(t *testing.T) {
	p := setupPreflight(t)
	p.connect(t, gmailReadonlyScope, gmailSendScope)

	// Scheduled through the real endpoint, so the row under test is the one the
	// product writes — payload shape included, which is what the export reads.
	p.scheduleFor(t, time.Now().Add(6*time.Hour))

	personID, err := ids.Parse(p.personID)
	if err != nil {
		t.Fatalf("person id %q: %v", p.personID, err)
	}
	pkg, err := privacy.AssembleSAR(
		p.privacyAdmin(t), compose.InstallationDB(p.Pool), ids.From[ids.PersonKind](personID))
	if err != nil {
		t.Fatalf("AssembleSAR: %v", err)
	}

	if len(pkg.ScheduledMessages) != 1 {
		t.Fatalf("the export carried %d unsent messages, want the one waiting for this person: %#v",
			len(pkg.ScheduledMessages), pkg.ScheduledMessages)
	}
	row := pkg.ScheduledMessages[0]
	if subject, _ := row["subject"].(string); subject != "Monday morning" {
		t.Errorf("the unsent message came back with subject %q, want the one that was scheduled", subject)
	}
	if body, _ := row["body"].(string); !strings.Contains(body, "Written the night before") {
		t.Errorf("the export withheld the body of a message written to this person: %#v", row)
	}
	// The state is part of the answer: a subject told a message exists, but not
	// whether it is still going to arrive, has been told half a fact.
	if status, _ := row["status"].(string); status != activities.ScheduledStatusScheduled {
		t.Errorf("the export reported status %q, want %q", status, activities.ScheduledStatusScheduled)
	}
}

// The blind-copy rule has two halves that pull opposite ways, and only a
// message with SEVERAL blind recipients can show both: a bcc'd subject must
// find their own message in their export, and must not learn who else was
// blind-copied on it. A projection that satisfied one half would look correct
// against a single-recipient fixture.
func TestABlindCopiedSubjectSeesTheirOwnMailAndNobodyElsesAddress(t *testing.T) {
	p := setupPreflight(t)
	p.connect(t, gmailReadonlyScope, gmailSendScope)

	// The subject is BLIND-copied; somebody else is the visible addressee, and
	// a third party shares the blind list with them.
	//
	// Both extra addressees need a person on file and a granted purpose,
	// because consent is owed to EVERY addressee however they were addressed —
	// the same rule that makes a blind copy a consent question at all. Without
	// them the send is refused 409 before it can be scheduled, which would say
	// nothing about the export.
	//
	// The other blind address is typed in MIXED CASE on purpose. The send path
	// removes blind copies from the To line case-insensitively; an export that
	// compared raw would leave this one sitting in the visible list while the
	// message itself correctly hid it, and the two derivations of "who is on
	// the To line" would disagree about the same message.
	const otherBlind = "Third.Party@Preflight.test"
	p.seedConsentedRecipient(t, "Visible Addressee", "visible@preflight.test")
	p.seedConsentedRecipient(t, "Other Blind", otherBlind)
	var scheduled struct {
		ID string `json:"id"`
	}
	status := p.Call(t, "POST", "/v1/emails", apptest.AnyMap{
		"subject": "Quiet copy", "body": "You were blind-copied on this.",
		"to":              []string{"visible@preflight.test"},
		"bcc":             []string{"buyer@preflight.test", otherBlind},
		"consent_purpose": "transactional",
		"links": []apptest.AnyMap{
			{"entity_type": "person", "entity_id": p.personID},
		},
		"scheduled_at": time.Now().Add(6 * time.Hour).UTC().Format(time.RFC3339),
		"scheduled_tz": "Europe/Berlin",
	}, nil, &scheduled)
	if status != http.StatusCreated {
		t.Fatalf("scheduling a blind-copied message → %d, want 201", status)
	}

	personID, err := ids.Parse(p.personID)
	if err != nil {
		t.Fatalf("person id %q: %v", p.personID, err)
	}
	pkg, err := privacy.AssembleSAR(
		p.privacyAdmin(t), compose.InstallationDB(p.Pool), ids.From[ids.PersonKind](personID))
	if err != nil {
		t.Fatalf("AssembleSAR: %v", err)
	}

	// Found on the blind list: without this the subject is absent from their own
	// export of a message they were going to receive.
	if len(pkg.ScheduledMessages) != 1 {
		t.Fatalf("a blind-copied subject got %d unsent messages, want 1: %#v",
			len(pkg.ScheduledMessages), pkg.ScheduledMessages)
	}
	// …and narrowed to themselves: exporting the whole blind list would hand
	// this subject a stranger's address, which is what a blind copy exists to
	// prevent.
	rendered := fmt.Sprintf("%#v", pkg.ScheduledMessages[0])
	if !strings.Contains(rendered, "buyer@preflight.test") {
		t.Errorf("the export withheld the subject's own blind address: %s", rendered)
	}
	// Case-insensitive, because the leak this guards against does not care how
	// the address was typed.
	if strings.Contains(strings.ToLower(rendered), strings.ToLower(otherBlind)) {
		t.Errorf("the export disclosed another blind recipient's address to this subject: %s", rendered)
	}
}
