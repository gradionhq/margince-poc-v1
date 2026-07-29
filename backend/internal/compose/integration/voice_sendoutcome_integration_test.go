// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// The voice learning loop's send half over the WHOLE composition: a real HTTP
// send, through the server compose.New assembles, against a real database.
// The module suites prove what the recorder decides; only this proves that a
// send reaches it at all — the recorder refuses in silence, so a composition
// that never calls it is indistinguishable from one that does, in every test
// but this one.
//
// Two facts live here that nothing shorter can carry. The judgment is made on
// the body the HUMAN approved while the wire carries an unsubscribe footer the
// send appended — a case that needs the real footer and the real classifier in
// the same transaction. And a send whose signal an erasure already emptied
// still goes out, leaving that row exactly as erasure left it.

import (
	"context"
	"crypto/sha256"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// voiceSentBody is the text the model served; a send that carries it verbatim
// is the owner accepting the draft rather than rewriting it.
const voiceSentBody = "Thanks for the call — the pricing follows tomorrow."

// voiceSendEnv is one bootstrapped installation ready to send: the acting
// human's voice profile, a consented recipient under an unsubscribable
// purpose, and the anchor the send threads onto.
type voiceSendEnv struct {
	*env
	workspaceID ids.UUID
	ownerID     ids.UUID
	profileID   string
	activityID  string
}

func setupVoiceSend(t *testing.T) *voiceSendEnv {
	t.Helper()
	e := setup(t)
	e.bootstrapWorkspace(t)
	profile := createVoiceProfile(t, e)

	var workspaceID, ownerID ids.UUID
	if err := e.owner.QueryRow(context.Background(),
		`SELECT workspace_id, owner_id FROM voice_profile WHERE id = $1`, profile.ID).
		Scan(&workspaceID, &ownerID); err != nil {
		t.Fatalf("resolving the profile's workspace and owner: %v", err)
	}

	var person struct {
		ID string `json:"id"`
	}
	if status := e.call(t, "POST", "/v1/people", anyMap{
		"full_name": "Draft Reader",
		"emails":    []anyMap{{"email": "reader@buyer.test"}},
	}, nil, &person); status != http.StatusCreated {
		t.Fatalf("create person → %d", status)
	}
	// A purpose of the installation's own making, non-DOI: it carries an
	// unsubscribe surface (only `transactional` is locked), which is what puts
	// a footer on the wire, and it needs no opt-in round trip to grant.
	var purpose struct {
		ID string `json:"id"`
	}
	if status := e.call(t, "POST", "/v1/consent-purposes", anyMap{
		"key": "newsletter", "label": "Newsletter", "requires_double_opt_in": false,
	}, nil, &purpose); status != http.StatusCreated {
		t.Fatalf("create consent purpose → %d", status)
	}
	if status := e.call(t, "POST", "/v1/people/"+person.ID+"/consent", anyMap{
		"purpose_id": purpose.ID, "new_state": "granted", "lawful_basis": "consent",
	}, nil, nil); status != http.StatusOK {
		t.Fatalf("record consent → %d", status)
	}
	var activity struct {
		ID string `json:"id"`
	}
	if status := e.call(t, "POST", "/v1/activities", anyMap{
		"kind": "email", "subject": "Pricing question", "direction": "inbound",
		"links": []anyMap{{"entity_type": "person", "entity_id": person.ID}},
	}, nil, &activity); status != http.StatusCreated {
		t.Fatalf("log anchor activity → %d", status)
	}

	return &voiceSendEnv{
		env: e, workspaceID: workspaceID, ownerID: ownerID,
		profileID: profile.ID, activityID: activity.ID,
	}
}

// voiceDraftOptions varies the seeded signal for the case under test; the zero
// value is the ordinary one — a live drafted signal the sender owns.
type voiceDraftOptions struct {
	// erased mirrors what Art. 17 and the retention sweep leave behind: the
	// content NULLed in place, the row still drafted.
	erased bool
	// foreignOwner puts the signal on a second human's profile.
	foreignOwner bool
}

// openVoiceDraft opens the drafted signal a served draft leaves behind and
// returns its reference and row id. profile_version stays NULL: these cases
// are about the send, and a built version would only add an FK to satisfy.
func (e *voiceSendEnv) openVoiceDraft(t *testing.T, opts voiceDraftOptions) (ref string, signal ids.UUID) {
	t.Helper()
	ctx := context.Background()
	profileID, ownerID := any(e.profileID), e.ownerID
	if opts.foreignOwner {
		var colleague ids.UUID
		if err := e.owner.QueryRow(ctx, `
			INSERT INTO app_user (workspace_id, email, display_name)
			VALUES ($1, $2, 'Colleague') RETURNING id`,
			e.workspaceID, "colleague-"+ids.NewV7().String()+"@example.test").Scan(&colleague); err != nil {
			t.Fatalf("seeding the colleague: %v", err)
		}
		var foreign ids.UUID
		if err := e.owner.QueryRow(ctx, `
			INSERT INTO voice_profile (workspace_id, owner_id, scope, status, source, captured_by)
			VALUES ($1, $2, 'user', 'ready', 'ui', $3) RETURNING id`,
			e.workspaceID, colleague, "human:"+colleague.String()).Scan(&foreign); err != nil {
			t.Fatalf("seeding the colleague's voice profile: %v", err)
		}
		profileID, ownerID = foreign, colleague
	}

	ref = "vd-" + ids.NewV7().String()
	hash := sha256.Sum256([]byte(ref))
	generated, erasedAt := any(voiceSentBody), any(nil)
	if opts.erased {
		generated, erasedAt = nil, time.Now().UTC()
	}
	if err := e.owner.QueryRow(ctx, `
		INSERT INTO voice_learning_signal
		  (workspace_id, voice_profile_id, draft_ref_hash, outcome, generated_original,
		   content_erased_at, retention_until, source, captured_by)
		VALUES ($1, $2, $3, 'drafted', $4, $5, $6, 'draft', $7) RETURNING id`,
		e.workspaceID, profileID, hash[:], generated, erasedAt,
		time.Now().UTC().Add(180*24*time.Hour), "human:"+ownerID.String()).Scan(&signal); err != nil {
		t.Fatalf("seeding the drafted learning signal: %v", err)
	}
	return ref, signal
}

// send issues the authenticated send under the unsubscribable purpose and
// insists it was accepted: a refusal would prove nothing about the learning
// signal either way.
func (e *voiceSendEnv) send(t *testing.T, ref, body string) ids.UUID {
	t.Helper()
	var sent struct {
		ID string `json:"id"`
	}
	if status := e.call(t, "POST", "/v1/activities/"+e.activityID+"/send-email", anyMap{
		"to": []string{"reader@buyer.test"}, "subject": "Pricing",
		"body": body, "consent_purpose": "newsletter", "draft_ref": ref,
	}, nil, &sent); status != http.StatusAccepted {
		t.Fatalf("send-email → %d, want 202", status)
	}
	id, err := ids.Parse(sent.ID)
	if err != nil {
		t.Fatalf("the accepted send returned no activity id: %v", err)
	}
	return id
}

// transmittedBody is what the delivery machinery will actually put on the
// wire — the body AFTER the send derived its unsubscribe footer.
func (e *voiceSendEnv) transmittedBody(t *testing.T, activityID ids.UUID) string {
	t.Helper()
	var body string
	if err := e.inWorkspace(t, e.slug, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(),
			`SELECT body FROM comms_outbound WHERE activity_id = $1`, activityID).Scan(&body)
	}); err != nil {
		t.Fatalf("reading the staged delivery: %v", err)
	}
	return body
}

// voiceSignal is the stored judgment as a later corpus decision reads it.
type voiceSignal struct {
	outcome           string
	similarity        *float64
	generatedOriginal *string
	contentErasedAt   *time.Time
	version           int64
}

func (e *voiceSendEnv) readSignal(t *testing.T, signal ids.UUID) voiceSignal {
	t.Helper()
	var row voiceSignal
	if err := e.owner.QueryRow(context.Background(), `
		SELECT outcome, similarity::double precision, generated_original, content_erased_at, version
		FROM voice_learning_signal WHERE id = $1`, signal).Scan(
		&row.outcome, &row.similarity, &row.generatedOriginal, &row.contentErasedAt, &row.version); err != nil {
		t.Fatalf("reading the learning signal: %v", err)
	}
	return row
}

// The end-to-end proof the wiring exists at all, on the case that would
// otherwise misread: a marketing send appends an unsubscribe footer the sender
// never wrote, so a judgment made on the transmitted body would score every
// marketing draft as edited. The footer assertion is what makes the accepted
// outcome mean something — without it the case would pass on a send that
// derived no footer to be confused by.
func TestAComposedSendRecordsTheVoiceOutcomeOnTheApprovedBody(t *testing.T) {
	e := setupVoiceSend(t)
	ref, signal := e.openVoiceDraft(t, voiceDraftOptions{})

	sent := e.send(t, ref, voiceSentBody)

	transmitted := e.transmittedBody(t, sent)
	if !strings.Contains(transmitted, "/v1/public/preferences/") {
		t.Fatalf("the transmitted body carries no unsubscribe footer, so this case proves nothing:\n%s", transmitted)
	}
	if transmitted == voiceSentBody {
		t.Fatal("the transmitted body is the approved body verbatim — the two must differ for this case to be about the right one")
	}
	row := e.readSignal(t, signal)
	if row.outcome != "accepted" || row.version != 2 {
		t.Fatalf("signal = %+v, want an accepted outcome at version 2 — a composition that never reaches the recorder leaves it drafted at version 1", row)
	}
	if row.similarity == nil || *row.similarity != 1 {
		t.Fatalf("similarity = %v, want 1: the owner sent the machine's words, and the footer is not their edit", row.similarity)
	}
}

// The GDPR gate the whole design rests on. Erasure NULLs the served text in
// place and leaves the row drafted; a send that judged it would compare
// against nothing, call that an edit, and write a fresh similarity over text
// an erasure already removed. The mail still goes out — a learning concern
// never costs a message.
func TestAComposedSendNeverRematerialisesAnErasedSignal(t *testing.T) {
	e := setupVoiceSend(t)
	ref, signal := e.openVoiceDraft(t, voiceDraftOptions{erased: true})

	e.send(t, ref, voiceSentBody)

	row := e.readSignal(t, signal)
	if row.outcome != "drafted" || row.version != 1 {
		t.Fatalf("signal = %+v, want the erased row untouched at version 1", row)
	}
	if row.generatedOriginal != nil {
		t.Fatalf("generated_original = %q — the send re-materialised text an erasure removed", *row.generatedOriginal)
	}
	if row.similarity != nil || row.contentErasedAt == nil {
		t.Fatalf("signal = %+v, want the erasure's own marks left exactly as it made them", row)
	}
}

// A reference on someone else's profile answers exactly like an unknown one:
// nothing recorded, and the send unaffected. Failing here would be an oracle
// over another human's drafts — "absent" and "someone else's" must be the same
// answer to the sender.
func TestAComposedSendRecordsNothingForAForeignOwnersSignal(t *testing.T) {
	e := setupVoiceSend(t)
	ref, signal := e.openVoiceDraft(t, voiceDraftOptions{foreignOwner: true})

	sent := e.send(t, ref, voiceSentBody)

	if body := e.transmittedBody(t, sent); body == "" {
		t.Fatal("the send staged no delivery — a foreign learning signal must not cost the message")
	}
	row := e.readSignal(t, signal)
	if row.outcome != "drafted" || row.version != 1 || row.similarity != nil {
		t.Fatalf("signal = %+v, want another human's row untouched at version 1", row)
	}
}
