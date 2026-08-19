// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package zalopersonal

// The chooser: what the member sees, what a save writes, and what it records.
//
// The two rules every operation in this unit is held to — no member argument, and
// nothing for an invocation with nobody behind it — are asserted over New().Tools
// in connection_test.go, so both operations here are covered by those the moment
// they are declared. What is left is this file's subject: that the screen can
// always show what was chosen, that the FIRST save is what arms capture, and that
// every verdict leaves a record naming the person it admitted.

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/pkg/extension"
)

// theRoster is the member's own contact list as Zalo answers it.
func theRoster() []zaloFriend {
	return []zaloFriend{
		{UserID: counterpartyZaloUID, DisplayName: "Nguyễn Văn Mẫu", Avatar: "https://z/a.png"},
		{UserID: "1900000000000000009", DisplayName: "Another Contact"},
		// An entry naming no account: it could match no message, so it must not sit
		// in a chooser as a person nobody can decide about.
		{DisplayName: "Nameless"},
	}
}

// chooserRuntime is one member's screen: their credential on deposit, and their
// stored verdicts scripted as the next multi-row read.
func chooserRuntime(t *testing.T, stored [][]any) (*fakeRuntime, *fakeInbox) {
	t.Helper()
	rt := newRuntime()
	depositSession(t, rt, callerUserID, memberIMEI)
	rt.tx.script(readVerdicts, stored...)
	return rt, &fakeInbox{uid: memberZaloUID, roster: theRoster()}
}

type chooserAnswer struct {
	Contacts []struct {
		ChannelUserID string `json:"channel_user_id"`
		DisplayName   string `json:"display_name"`
		AvatarURL     string `json:"avatar_url"`
		Mode          string `json:"mode"`
	} `json:"contacts"`
	RosterAvailable bool `json:"roster_available"`
}

func TestTheChooserJoinsTheRosterWithWhatTheMemberChose(t *testing.T) {
	t.Parallel()
	rt, opened := chooserRuntime(t, [][]any{
		allowRow(entryID, counterpartyZaloUID, verdictAllow, "Chosen"),
	})

	out, err := contactsVia(context.Background(), rt, callerUserID,
		newProvider(map[string]*fakeInbox{memberIMEI: opened}).open())
	if err != nil {
		t.Fatalf("reading the chooser: %v", err)
	}
	got := jsonOf[chooserAnswer](t, out)
	if !got.RosterAvailable {
		t.Fatal("the roster was read and still reported as unavailable")
	}
	if len(got.Contacts) != 2 {
		t.Fatalf("the chooser lists %d contacts; the roster entry naming no account must be dropped: %+v", len(got.Contacts), got.Contacts)
	}
	byID := map[string]string{}
	for _, contact := range got.Contacts {
		byID[contact.ChannelUserID] = contact.Mode
	}
	if byID[counterpartyZaloUID] != string(verdictAllow) {
		t.Fatalf("the chosen conversation reads as %q", byID[counterpartyZaloUID])
	}
	// UNDECIDED IS ITS OWN WORD, distinct from blocked, even though the filter
	// treats them the same: on a screen "you have not decided" and "you ruled this
	// out" are different sentences, and collapsing them would tell somebody they
	// declined something they never saw.
	if byID["1900000000000000009"] != string(verdictNone) {
		t.Fatalf("a contact nobody has decided about reads as %q", byID["1900000000000000009"])
	}
	// Ordered by the name a human reads, so the list does not reshuffle between
	// two reads of the same data.
	if got.Contacts[0].DisplayName != "Another Contact" {
		t.Fatalf("the chooser is not ordered by name: %+v", got.Contacts)
	}
}

// The degradation that matters most: a member must always be able to see and
// change what they already chose, and a chooser that goes blank when Zalo is
// unreachable takes away the one control this unit promises them — at exactly the
// moment they may be reaching for it.
func TestTheChooserStillAnswersWhatWasChosenWhenZaloCannotBeReached(t *testing.T) {
	t.Parallel()
	for name, provider := range map[string]*fakeProvider{
		"a session Zalo no longer accepts": newProvider(nil),
		"a roster call that was refused": newProvider(map[string]*fakeInbox{
			memberIMEI: {uid: memberZaloUID, rosterErr: errors.New("refused")},
		}),
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			rt, _ := chooserRuntime(t, [][]any{
				allowRow(entryID, counterpartyZaloUID, verdictAllow, "Chosen Earlier"),
				allowRow(secondEntryID, "1900000000000000009", verdictBlock, "Ruled Out"),
			})

			out, err := contactsVia(context.Background(), rt, callerUserID, provider.open())
			if err != nil {
				t.Fatalf("%s failed the screen: %v", name, err)
			}
			got := jsonOf[chooserAnswer](t, out)
			if got.RosterAvailable {
				t.Fatalf("%s was reported as a readable roster", name)
			}
			if len(got.Contacts) != 2 {
				t.Fatalf("%s lost the member's own choices: %+v", name, got.Contacts)
			}
			// The stored name, so the list still reads as PEOPLE rather than as
			// account ids.
			if got.Contacts[0].DisplayName != "Chosen Earlier" || got.Contacts[0].Mode != string(verdictAllow) {
				t.Fatalf("%s rendered a saved choice as %+v", name, got.Contacts[0])
			}
		})
	}
}

// A conversation being captured must not disappear from the screen that controls
// it because the provider stopped listing that person as a contact.
func TestTheChooserKeepsAChosenConversationTheRosterNoLongerLists(t *testing.T) {
	t.Parallel()
	rt, opened := chooserRuntime(t, [][]any{
		allowRow(entryID, "1900000000000000077", verdictAllow, "Former Contact"),
	})

	out, err := contactsVia(context.Background(), rt, callerUserID,
		newProvider(map[string]*fakeInbox{memberIMEI: opened}).open())
	if err != nil {
		t.Fatalf("reading the chooser: %v", err)
	}
	got := jsonOf[chooserAnswer](t, out)
	for _, contact := range got.Contacts {
		if contact.ChannelUserID == "1900000000000000077" && contact.Mode == string(verdictAllow) {
			return
		}
	}
	t.Fatalf("a conversation still being captured is not on the screen that controls it: %+v", got.Contacts)
}

// savingRuntime scripts one member's save: the connection as it is read, the
// connection as the save leaves it, and one new verdict.
//
// EVERY save issues the connection write, whether or not capture was already armed,
// because it also clears the polling backoff — so there is always a second row to
// script.
func savingRuntime(t *testing.T, conn, saved []any) *fakeRuntime {
	t.Helper()
	rt := newRuntime()
	rows := [][]any{conn, saved, allowRow(entryID, counterpartyZaloUID, verdictAllow, "Chosen")}
	rt.tx.singleRows = rows
	// The read of the existing verdict finds none: this is a first choice about this
	// counterparty.
	rt.tx.noRows = map[int]bool{len(rows): true}
	return rt
}

// oneVerdict is the document a screen submits for a single pick under only_chosen.
// EVERY save carries the mode: it is the answer, and the entries only refine it.
const oneVerdict = `{"capture_mode":"only_chosen","entries":[{"channel_user_id":"` +
	counterpartyZaloUID + `","mode":"allow","display_name":"Chosen"}]}`

// oneExclusion is the mirror document under everyone_except: the same person, on the
// leave-out list instead of the pick list.
const oneExclusion = `{"capture_mode":"everyone_except","entries":[{"channel_user_id":"` +
	counterpartyZaloUID + `","mode":"block","display_name":"Left out"}]}`

func TestTheFirstSaveIsWhatArmsCapture(t *testing.T) {
	t.Parallel()
	rt := savingRuntime(t,
		connectionRow(statusConnected, memberZaloUID, false),
		connectionRow(statusConnected, memberZaloUID, true))

	out, err := saveAllowlist(context.Background(), rt, json.RawMessage(oneVerdict))
	if err != nil {
		t.Fatalf("saving a first list: %v", err)
	}
	got := jsonOf[struct {
		Saved        int  `json:"saved"`
		CaptureArmed bool `json:"capture_armed"`
	}](t, out)
	if got.Saved != 1 || !got.CaptureArmed {
		t.Fatalf("the first save answered %+v", got)
	}
	sql, args := rt.tx.statementMentioning(t, "capture_enabled = true")
	if !strings.Contains(sql, "version = version + 1") {
		t.Fatalf("arming capture is not version-guarded:\n%s", sql)
	}
	if args[0] != callerUserID {
		t.Fatalf("capture was armed for %v rather than for the caller", args[0])
	}
	// The verdict itself carries the caller's own id, stamped from the invocation.
	_, entryArgs := rt.tx.statementMentioning(t, "ON CONFLICT (workspace_id, user_id, channel_user_id)")
	if entryArgs[0] != callerUserID || entryArgs[1] != counterpartyZaloUID || entryArgs[2] != string(verdictAllow) {
		t.Fatalf("the verdict was written as %v", entryArgs)
	}
	verbs := publishedVerbs(rt)
	if !verbs[eventCaptureArmed] || !verbs[eventVerdictSet] {
		t.Fatalf("the save announced %v; arming capture and the verdict are both facts somebody may ask about", verbs)
	}
}

func publishedVerbs(rt *fakeRuntime) map[string]bool {
	verbs := map[string]bool{}
	for _, event := range rt.tx.published {
		verbs[event.Verb] = true
	}
	return verbs
}

// Editing a list is not arming capture a second time. Recording it as one would say
// capture was switched on every time somebody changed their mind.
func TestSavingAgainDoesNotReArmCaptureOrSayItDid(t *testing.T) {
	t.Parallel()
	rt := savingRuntime(t,
		withIdleStreak(connectionRow(statusConnected, memberZaloUID, true), 4),
		connectionRow(statusConnected, memberZaloUID, true))

	out, err := saveAllowlist(context.Background(), rt, json.RawMessage(oneVerdict))
	if err != nil {
		t.Fatalf("editing a list: %v", err)
	}
	if jsonOf[struct {
		CaptureArmed bool `json:"capture_armed"`
	}](t, out).CaptureArmed {
		t.Fatal("an edit reported itself as the save that armed capture")
	}
	// The LEDGER is what must not claim capture was switched on; the statement still
	// runs, because every save also brings the member forward.
	if publishedVerbs(rt)[eventCaptureArmed] {
		t.Fatal("the bus was told capture was armed by a save that changed nothing about it")
	}
	if len(rt.tx.audited) != 1 {
		t.Fatalf("an edit wrote %d ledger row(s); only the verdict changed: %+v", len(rt.tx.audited), rt.tx.audited)
	}
}

// THE UX TRAP. A member who has been quiet for a week is inside a capped polling
// backoff, and the moment they arm a conversation they must not wait it out before
// anything appears — which is what they would read as the feature not working.
//
// It holds for an EDIT of an already-armed list too, which is the case where the
// wait is longest: a week of blocks, then one allow.
func TestSavingTheListAlwaysBringsTheMemberForward(t *testing.T) {
	t.Parallel()
	for name, conn := range map[string][]any{
		"a first save":                  withIdleStreak(connectionRow(statusConnected, memberZaloUID, false), 0),
		"an edit after a quiet week":    withIdleStreak(connectionRow(statusConnected, memberZaloUID, true), 12),
		"a session waiting on a rescan": withIdleStreak(connectionRow(statusNeedsReconnect, memberZaloUID, true), 3),
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			rt := savingRuntime(t, conn, connectionRow(statusConnected, memberZaloUID, true))

			if _, err := saveAllowlist(context.Background(), rt, json.RawMessage(oneVerdict)); err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			sql, _ := rt.tx.statementMentioning(t, "capture_enabled = true")
			if !strings.Contains(sql, duePromptly) {
				t.Fatalf("%s left the member inside their backoff:\n%s", name, sql)
			}
		})
	}
}

// ONE LEDGER ROW PER VERDICT, with the before-image of whatever the database
// actually held: a summary saying "17 saved" cannot answer the question somebody
// will ask, which is whether this installation was ever permitted to read a named
// person's conversation.
func TestEveryVerdictIsRecordedByItselfWithWhatItReplaced(t *testing.T) {
	t.Parallel()
	rt := newRuntime()
	rt.tx.singleRows = [][]any{
		connectionRow(statusConnected, memberZaloUID, true), // read
		connectionRow(statusConnected, memberZaloUID, true), // brought forward
		// allow -> block, which is a NARROWING and therefore leaves no floor: this
		// test is about how one verdict is recorded, and the lifting of an exclusion
		// is its own act with its own row (floor_test.go).
		allowRow(entryID, counterpartyZaloUID, verdictAllow, "Chosen"), // the existing verdict
		allowRow(entryID, counterpartyZaloUID, verdictBlock, "Chosen"), // what the upsert returned
	}

	if _, err := saveAllowlist(context.Background(), rt, json.RawMessage(oneExclusion)); err != nil {
		t.Fatalf("changing a verdict: %v", err)
	}
	if len(rt.tx.audited) != 1 {
		t.Fatalf("changing one verdict wrote %d ledger row(s): %+v", len(rt.tx.audited), rt.tx.audited)
	}
	change := rt.tx.audited[0]
	if change.Entity != allowlistEntity || change.ID != entryID {
		t.Fatalf("the verdict was recorded against %q/%q", change.Entity, change.ID)
	}
	if change.Action != extension.AuditUpdate {
		t.Fatalf("replacing a verdict was recorded as %q; a create would read as a first-ever permission", change.Action)
	}
	if !strings.Contains(string(change.Before), string(verdictAllow)) ||
		!strings.Contains(string(change.After), string(verdictBlock)) {
		t.Fatalf("the ledger does not say what changed: %s -> %s", change.Before, change.After)
	}
}

func TestAFirstVerdictIsRecordedAsACreateWithNoBeforeImage(t *testing.T) {
	t.Parallel()
	rt := savingRuntime(t,
		connectionRow(statusConnected, memberZaloUID, true),
		connectionRow(statusConnected, memberZaloUID, true))

	if _, err := saveAllowlist(context.Background(), rt, json.RawMessage(oneVerdict)); err != nil {
		t.Fatalf("saving a first verdict: %v", err)
	}
	change := rt.tx.audited[len(rt.tx.audited)-1]
	if change.Action != extension.AuditCreate || len(change.Before) != 0 {
		t.Fatalf("a first verdict was recorded as %q with before-image %s", change.Action, change.Before)
	}
}

// Choosing conversations for an account this installation holds no credential for
// would record a consent nothing can act on and no screen can explain.
func TestSavingIsRefusedWhereThereIsNoAccountToChooseFor(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		conn     []any
		noRow    bool
		sentinel error
	}{
		"nobody who ever connected":    {noRow: true, sentinel: extension.ErrNotFound},
		"an account already withdrawn": {conn: connectionRow(statusDisconnected, memberZaloUID, false), sentinel: extension.ErrInvalid},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			rt := newRuntime()
			if tc.noRow {
				rt.tx.noRows = map[int]bool{1: true}
			} else {
				rt.tx.singleRows = [][]any{tc.conn}
			}

			_, err := saveAllowlist(context.Background(), rt, json.RawMessage(oneVerdict))
			if !errors.Is(err, tc.sentinel) {
				t.Fatalf("%s was answered %v", name, err)
			}
			for _, sql := range rt.tx.statements {
				if strings.Contains(sql, "capture_enabled = true") {
					t.Fatalf("%s still armed capture:\n%s", name, sql)
				}
			}
		})
	}
}

// A connection that moved between the read and the write: the member disconnected,
// or connected a different account, while the list was being saved. Arming capture
// on the row as it is NOW would undo what they just did.
func TestAConnectionThatMovedUnderTheSaveIsAConflict(t *testing.T) {
	t.Parallel()
	rt := newRuntime()
	rt.tx.singleRows = [][]any{connectionRow(statusConnected, memberZaloUID, false)}
	// The version-guarded write matches nothing: the row moved on.
	rt.tx.noRows = map[int]bool{2: true}

	_, err := saveAllowlist(context.Background(), rt, json.RawMessage(oneVerdict))
	if !errors.Is(err, extension.ErrConflict) {
		t.Fatalf("a connection that moved under the save was answered %v", err)
	}
}

// Documents the contract's schema describes and a verdict table cannot hold. The
// repeated counterparty is the one worth naming: two decisions about one person is
// a mistake, and resolving it silently would be this CRM choosing which of
// somebody's two answers it acted on.
func TestADocumentTheVerdictTableCannotHoldIsRefused(t *testing.T) {
	t.Parallel()
	long := strings.Repeat("x", extension.MaxChannelUserIDLength+1)
	for name, args := range map[string]string{
		"no entries at all":                `{"entries":[]}`,
		"a person named twice":             `{"capture_mode":"only_chosen","entries":[{"channel_user_id":"a","mode":"allow"},{"channel_user_id":"a","mode":"block"}]}`,
		"an entry verdict nobody declared": `{"capture_mode":"only_chosen","entries":[{"channel_user_id":"a","mode":"maybe"}]}`,
		"an entry naming nobody":           `{"capture_mode":"only_chosen","entries":[{"channel_user_id":"  ","mode":"allow"}]}`,
		"an account id past the cap":       `{"capture_mode":"only_chosen","entries":[{"channel_user_id":"` + long + `","mode":"allow"}]}`,
		"a display name past the cap": `{"capture_mode":"only_chosen","entries":[{"channel_user_id":"a","mode":"allow","display_name":"` +
			strings.Repeat("n", extension.MaxDisplayNameRunes+1) + `"}]}`,
		"a field the contract does not declare": `{"capture_mode":"only_chosen","entries":[{"channel_user_id":"a","mode":"allow","capture":true}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			rt := newRuntime()
			if _, err := saveAllowlist(context.Background(), rt, json.RawMessage(args)); err == nil {
				t.Fatalf("%s was accepted", name)
			}
			if len(rt.tx.statements) != 0 {
				t.Fatalf("%s reached the database: %v", name, rt.tx.statements)
			}
		})
	}
}

func TestASaveOverTheOneRequestCapIsRefused(t *testing.T) {
	t.Parallel()
	entries := make([]string, 0, maxSavedEntries+1)
	for i := 0; i <= maxSavedEntries; i++ {
		entries = append(entries, `{"channel_user_id":"u`+itoa(i)+`","mode":"allow"}`)
	}
	rt := newRuntime()
	args := json.RawMessage(`{"capture_mode":"only_chosen","entries":[` + strings.Join(entries, ",") + `]}`)

	if _, err := saveAllowlist(context.Background(), rt, args); !errors.Is(err, extension.ErrInvalid) {
		t.Fatalf("a save past the cap was answered %v", err)
	}
}

// The number the connected screen has to be able to make true.
func TestStatusSaysHowManyConversationsAreArmed(t *testing.T) {
	t.Parallel()
	rt := newRuntime()
	rt.tx.singleRows = [][]any{connectionRow(statusConnected, memberZaloUID, true), {3}}

	out, err := status(context.Background(), rt, json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("reading the status: %v", err)
	}
	if got := jsonOf[struct {
		AllowedCount int `json:"allowed_count"`
	}](t, out).AllowedCount; got != 3 {
		t.Fatalf("the screen is told %d conversations are armed", got)
	}
	sql, args := rt.tx.statementMentioning(t, "count(*)")
	if args[1] != string(verdictAllow) {
		t.Fatalf("the count is not restricted to allowed conversations:\n%s", sql)
	}
}

// A member who has connected nothing is not asked how many conversations they
// armed: the question has no subject, and spending a statement on it would report a
// count about an account that does not exist.
func TestAMemberWithNoConnectionIsNotCountedAgainst(t *testing.T) {
	t.Parallel()
	rt := newRuntime()
	rt.tx.noRows = map[int]bool{1: true}

	if _, err := status(context.Background(), rt, json.RawMessage(`{}`)); err != nil {
		t.Fatalf("not having connected is the ordinary state, and it answered %v", err)
	}
	for _, sql := range rt.tx.statements {
		if strings.Contains(sql, "count(*)") {
			t.Fatalf("a count was taken for a member with no connection:\n%s", sql)
		}
	}
}

// Removing somebody from the picker sends `none`, which takes them off the list
// entirely — and MUST NOT forget how far their conversation was already captured.
//
// That property is the one the separate cursor table buys, and it is why this test
// asserts on an absence: a member who removes a person and puts them back should
// resume, not re-receive their whole conversation. When the bookmark lived on the
// verdict row, deleting the verdict silently reset it.
func TestTakingSomebodyOffTheListForgetsTheVerdictAndNotTheReadingPosition(t *testing.T) {
	t.Parallel()
	rt := newRuntime()
	rt.tx.singleRows = [][]any{
		connectionRow(statusConnected, memberZaloUID, true),            // read
		connectionRow(statusConnected, memberZaloUID, true),            // brought forward
		allowRow(entryID, counterpartyZaloUID, verdictAllow, "Chosen"), // the row being removed
	}
	args := json.RawMessage(`{"capture_mode":"only_chosen","entries":[{"channel_user_id":"` +
		counterpartyZaloUID + `","mode":"none"}]}`)

	if _, err := saveAllowlist(context.Background(), rt, args); err != nil {
		t.Fatalf("taking somebody off the list: %v", err)
	}
	sql, delArgs := rt.tx.statementMentioning(t, "allowlist\n\t\t  WHERE user_id")
	if delArgs[0] != callerUserID || delArgs[1] != counterpartyZaloUID {
		t.Fatalf("the wrong row was removed: %v\n%s", delArgs, sql)
	}
	for _, statement := range rt.tx.statements {
		if strings.Contains(statement, "conversation_cursor") {
			t.Fatalf("removing a verdict touched the reading position:\n%s", statement)
		}
	}
	change := rt.tx.audited[len(rt.tx.audited)-1]
	if change.Action != extension.AuditErase || len(change.After) != 0 {
		t.Fatalf("a removal was recorded as %q with an after-image %s", change.Action, change.After)
	}
	if !strings.Contains(string(change.Before), counterpartyZaloUID) {
		t.Fatalf("the ledger does not say who was removed: %s", change.Before)
	}
}

// Removing somebody who was never on the list is the outcome asked for, not an error:
// a picker that removes the same person twice has done no harm.
func TestTakingSomebodyOffAListTheyWereNeverOnIsNotAnError(t *testing.T) {
	t.Parallel()
	rt := newRuntime()
	rt.tx.singleRows = [][]any{
		connectionRow(statusConnected, memberZaloUID, true),
		connectionRow(statusConnected, memberZaloUID, true),
	}
	rt.tx.noRows = map[int]bool{3: true}
	args := json.RawMessage(`{"capture_mode":"only_chosen","entries":[{"channel_user_id":"` +
		counterpartyZaloUID + `","mode":"none"}]}`)

	if _, err := saveAllowlist(context.Background(), rt, args); err != nil {
		t.Fatalf("removing somebody who was not on the list: %v", err)
	}
	for _, statement := range rt.tx.statements {
		if strings.Contains(statement, "DELETE") {
			t.Fatalf("a row that did not exist was deleted anyway:\n%s", statement)
		}
	}
}

// A save with a mode and NO entries is legal, and under everyone_except it is the
// commonest first save there is: everything, with nobody left out yet.
func TestAModeWithNoListIsAValidSave(t *testing.T) {
	t.Parallel()
	rt := newRuntime()
	rt.tx.singleRows = [][]any{
		connectionRow(statusConnected, memberZaloUID, false),
		withMode(connectionRow(statusConnected, memberZaloUID, true), captureEveryoneExcept),
	}

	out, err := saveAllowlist(context.Background(), rt,
		json.RawMessage(`{"capture_mode":"everyone_except"}`))
	if err != nil {
		t.Fatalf("choosing a mode with no list: %v", err)
	}
	got := jsonOf[struct {
		Saved        int  `json:"saved"`
		CaptureArmed bool `json:"capture_armed"`
	}](t, out)
	if got.Saved != 0 || !got.CaptureArmed {
		t.Fatalf("choosing everyone_except with no exclusions answered %+v", got)
	}
	sql, args := rt.tx.statementMentioning(t, "capture_enabled = true")
	if args[2] != captureEveryoneExcept {
		t.Fatalf("the mode was written as %v:\n%s", args[2], sql)
	}
}

// THE FLOOR MOVES ONLY WHEN THE MODE CHANGES. Stamping it on every save would push it
// forward each time somebody edited a list — and under everyone_except that silently
// loses the messages between the two saves for every conversation with no bookmark.
func TestTheModeFloorMovesOnAChangeOfModeAndNotOnAnEdit(t *testing.T) {
	t.Parallel()
	rt := newRuntime()
	rt.tx.singleRows = [][]any{
		withMode(connectionRow(statusConnected, memberZaloUID, true), captureEveryoneExcept),
		withMode(connectionRow(statusConnected, memberZaloUID, true), captureEveryoneExcept),
		allowRow(entryID, counterpartyZaloUID, verdictBlock, "Left out"),
	}
	rt.tx.noRows = map[int]bool{3: true}

	if _, err := saveAllowlist(context.Background(), rt, json.RawMessage(oneExclusion)); err != nil {
		t.Fatalf("editing a leave-out list: %v", err)
	}
	sql, _ := rt.tx.statementMentioning(t, "capture_mode_since = CASE WHEN")
	if !strings.Contains(sql, "capture_mode, '') = $3") {
		t.Fatalf("the floor is not conditional on the mode actually changing:\n%s", sql)
	}
}
