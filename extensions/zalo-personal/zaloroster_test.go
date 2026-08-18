// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package zalopersonal

// The roster read, and the one property it exists to hold: what this unit maps
// is three fields, and everything else the wire row carries is dropped while
// decoding rather than after.

import (
	"reflect"
	"strings"
	"testing"
)

// hasFieldNamedLike reports whether a struct carries a field that would collect
// the named wire key — by tag, or by encoding/json's case-insensitive match on
// the field name, which is how a field with no tag still picks a value up.
func hasFieldNamedLike(fields reflect.Type, wireKey string) bool {
	for i := range fields.NumField() {
		field := fields.Field(i)
		if tag, _, _ := strings.Cut(field.Tag.Get("json"), ","); tag != "" {
			if strings.EqualFold(tag, wireKey) {
				return true
			}
			continue
		}
		if strings.EqualFold(field.Name, wireKey) {
			return true
		}
	}
	return false
}

const rosterPath = "/api/social/friend/getfriends"

// capturedRosterRow is one row of getfriends as the live service answered it
// (2026-08-17), with the identifiers replaced because this repository is public.
// EVERY key is verbatim, which is the point: the fields this suite asserts are
// dropped are the real ones, not ones a test invented.
const capturedRosterRow = `{
  "userId": "1900000000000000001", "userKey": "1900000000000000001",
  "globalId": "ETDJHKMFJIAFBJD3IB7K4RR6AC618IO0",
  "username": "t_m7dcadmim7", "displayName": "Nguyễn Văn Mẫu", "zaloName": "vanmau",
  "avatar": "https://s120-ava-talk.zadn.vn/1/f/7/b/1/120/placeholder.jpg",
  "bgavatar": "", "cover": "https://cover.example/c.jpg",
  "gender": 1, "dob": -631177200, "sdob": "01/01/1950", "status": "",
  "phoneNumber": "", "isFr": 1, "isBlocked": 0,
  "lastActionTime": 1786863018394, "lastUpdateTime": 1723263667,
  "isActive": 1, "isActivePC": 0, "isActiveWeb": 0, "isValid": 1,
  "accountStatus": 0, "user_mode": 0, "type": 0, "key": 0, "createdTs": 0,
  "oaInfo": null, "oa_status": null, "isEnterpriseAccount": 0,
  "bizPkg": { "label": null, "pkgId": 0 }
}`

func TestTheRosterMapsTheThreeFieldsAContactChoiceNeeds(t *testing.T) {
	fake := newChatServer()
	fake.calls[rosterPath] = func(*testing.T, map[string]any) string {
		return `{"error_code":0,"data":[` + capturedRosterRow + `]}`
	}

	roster, err := resumeAgainst(t, fake).friends(t.Context())
	if err != nil {
		t.Fatalf("read the roster: %v", err)
	}
	if len(roster) != 1 {
		t.Fatalf("the roster read back %d contact(s), want the one the server sent", len(roster))
	}

	want := zaloFriend{
		UserID:      "1900000000000000001",
		DisplayName: "Nguyễn Văn Mẫu",
		Avatar:      "https://s120-ava-talk.zadn.vn/1/f/7/b/1/120/placeholder.jpg",
	}
	if roster[0] != want {
		t.Errorf("the contact read back as %+v, want %+v", roster[0], want)
	}
}

// THE PRIVACY BOUNDARY IS THE STRUCT. The wire row reports a date of birth
// (dob/sdob) and presence telemetry (lastActionTime, isActivePC, isActiveWeb) —
// when a person was last active and on which device. Ingesting a rep's personal
// contacts' presence history into a company CRM is exactly what the allowlist
// exists to prevent, and an allowlist cannot protect a field that was mapped
// before it ran. A field the mapped type does not have is a field no later
// change can accidentally store.
func TestNoFieldBeyondTheThreeSurvivesTheRosterMapping(t *testing.T) {
	dropped := []string{
		"dob", "sdob", "gender", "lastActionTime", "isActivePC", "isActiveWeb",
		"phoneNumber", "username", "globalId", "userKey", "cover", "isBlocked",
	}
	for _, field := range dropped {
		if !strings.Contains(capturedRosterRow, `"`+field+`"`) {
			t.Fatalf("the captured row no longer carries %q, so this test proves nothing about dropping it", field)
		}
	}

	// Reflected off the type rather than asserted against a written list: a
	// field added to zaloFriend later has to be a deliberate decision, and this
	// fails the moment one of the dropped names appears on it.
	for _, field := range dropped {
		if hasFieldNamedLike(reflect.TypeFor[zaloFriend](), field) {
			t.Errorf("zaloFriend carries %q, which the wire row must not be able to bring in", field)
		}
	}
	if hasFieldNamedLike(reflect.TypeFor[wireFriend](), "phoneNumber") {
		t.Error("the wire row's phoneNumber is unmarshalled, and a field with no consumer is a field that leaks")
	}
}

func TestARosterThatIsNotAListIsReportedRatherThanReadAsEmpty(t *testing.T) {
	fake := newChatServer()
	fake.calls[rosterPath] = func(*testing.T, map[string]any) string {
		return `{"error_code":0,"data":{"contacts":[]}}`
	}

	_, err := resumeAgainst(t, fake).friends(t.Context())
	if err == nil {
		t.Fatal("a roster that is not a list was read as an empty roster, which tells the member they know nobody")
	}
	if !strings.Contains(err.Error(), "roster") {
		t.Errorf("the failure reads %q, and it has to say what could not be parsed", err)
	}
}

func TestTheRosterAsksForOnePageOfTheSizeThisUnitPins(t *testing.T) {
	fake := newChatServer()
	var asked map[string]any
	fake.calls[rosterPath] = func(_ *testing.T, params map[string]any) string {
		asked = params
		return `{"error_code":0,"data":[]}`
	}

	if _, err := resumeAgainst(t, fake).friends(t.Context()); err != nil {
		t.Fatalf("read the roster: %v", err)
	}
	if got, want := asked["count"], float64(friendsPageSize); got != want {
		t.Errorf("the roster asked for count %v, want %v", got, want)
	}
	if got := asked["page"]; got != float64(1) {
		t.Errorf("the roster asked for page %v, want the first one", got)
	}
	if got := asked["imei"]; got != vectorIMEI {
		t.Errorf("the roster call carried imei %v, want the session's own device id", got)
	}
}

func TestARosterCallWithNoProfileHostSaysSoRatherThanCallingNothing(t *testing.T) {
	session := resumeAgainst(t, newChatServer())
	session.service = map[string][]string{}

	if _, err := session.friends(t.Context()); err == nil {
		t.Fatal("a roster read with no profile host was attempted anyway")
	}
}
