// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package identity

// The extension RBAC object has to travel the WHOLE way: registered at boot →
// admitted by Parse in a stored role document → merged into the effective
// permissions → serialized into /me under the key the client looks it up by.
//
// The test lives here rather than in compose because the last two steps are
// identity's internals: policy.Merge and meResponse are both unexported, and a
// test that stopped short of the wire would prove the object was granted while
// leaving the failure this exists to prevent — a screen that typechecks, gates
// on an object the client never learns the user holds, renders nothing, and looks
// like a frontend bug — entirely possible. Registration is exercised through the
// exported seam the composition root calls (RegisterRbacObjects), so nothing
// here reaches around the boot path.

import (
	"encoding/json"
	"maps"
	"testing"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/modules/identity/internal/policy"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// registerForTest registers objects and unregisters them afterwards, so one
// test's vocabulary is never another's.
func registerForTest(t *testing.T, objects ...RbacObject) {
	t.Helper()
	if err := RegisterRbacObjects(objects...); err != nil {
		t.Fatalf("registering %v: %v", objects, err)
	}
	t.Cleanup(ResetRbacObjectsForTest)
}

// meObjects renders /me for a principal holding permissions and returns the
// authorization.objects map exactly as a client receives it.
func meObjects(t *testing.T, perms principal.Permissions) map[string]map[string]bool {
	t.Helper()
	raw, err := json.Marshal(NewHandlers(&Service{}).meResponse(Identity{
		Email:       "rep@example.com",
		SeatType:    "full",
		Permissions: perms,
	}, crmcontracts.Native))
	if err != nil {
		t.Fatalf("marshalling /me: %v", err)
	}
	var decoded struct {
		Authorization struct {
			Objects map[string]map[string]bool `json:"objects"`
		} `json:"authorization"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("decoding /me: %v", err)
	}
	return decoded.Authorization.Objects
}

func TestExtensionRbacObjectReachesTheMeSnapshot(t *testing.T) {
	const object = "ext_crm_demo_widget"
	registerForTest(t, object)

	// Step 1 — a stored role document may now GRANT it. Without registration
	// this is where the whole thing stops: Parse DROPS an object outside the
	// vocabulary, so the grant never reaches the merge and no snapshot carries
	// it. (It drops rather than refuses because Parse reads stored data that
	// outlives the code defining the vocabulary — see policy.Parse and the
	// UAT's F4.)
	doc, err := policy.Parse([]byte(`{"objects":{"` + object + `":{"create":false,"read":true,"update":true,"delete":false}},"row_scope":"team"}`))
	if err != nil {
		t.Fatalf("a role document granting a registered extension object must parse: %v", err)
	}

	// Step 2 — the merge carries it into the effective permissions.
	perms := policy.Merge(map[string]policy.Document{"rep": doc})
	if got := perms.Objects[object]; !got.Read || !got.Update || got.Create || got.Delete {
		t.Fatalf("merged grant on %s = %+v, want read+update only", object, got)
	}

	// Step 3 — and /me publishes it under the contract's own key spelling. This
	// is the step a struct comparison cannot see: the client looks the grant up
	// by lowercase name, so a snapshot that carried the object with capitalized
	// keys would read as a correctly withheld permission.
	objects := meObjects(t, perms)
	got, ok := objects[object]
	if !ok {
		t.Fatalf("/me carries no %s key, so the client can never learn the user holds it — "+
			"the unit's screen would render nothing and look like a frontend bug. Got %v", object, objects)
	}
	want := map[string]bool{"create": false, "read": true, "update": true, "delete": false}
	if !maps.Equal(got, want) {
		t.Errorf("/me authorization.objects[%s] = %v, want %v", object, got, want)
	}
}

// TestAnUnregisteredExtensionObjectReachesNothing is the other direction, and it
// is what makes the test above about REGISTRATION rather than about Merge being
// permissive. Without it, a Parse that carried anything through would satisfy
// the pair.
//
// The assertion is that the grant REACHES NOTHING, not that the document is
// refused. Those were the same thing until the UAT's F4: refusing meant the
// login failed, so removing a unit locked out every user whose role still
// named its object. Dropping keeps the vocabulary closed where it matters —
// nothing is granted — without making stored data unreadable.
func TestAnUnregisteredExtensionObjectReachesNothing(t *testing.T) {
	t.Cleanup(ResetRbacObjectsForTest)
	const object = "ext_crm_demo_widget"
	// Nothing registered.
	doc, err := policy.Parse([]byte(`{"objects":{"` + object + `":{"read":true}},"row_scope":"team"}`))
	if err != nil {
		t.Fatalf("a document granting an unregistered object must still parse: %v", err)
	}
	if perms := policy.Merge(map[string]policy.Document{"rep": doc}); perms.Objects[object].Read {
		t.Fatal("an unregistered object reached the effective permissions — the vocabulary is not closed")
	}
	if _, ok := meObjects(t, policy.Merge(map[string]policy.Document{"rep": doc}))[object]; ok {
		t.Fatal("/me carries an object nothing registered, so a screen would gate on a grant no server enforces")
	}
	if RBACObjectGrantable(object) {
		t.Fatal("an unregistered object reports as grantable, so an authority requirement naming it would look satisfiable")
	}
}

// TestARegisteredObjectIsInTheSnapshotEvenUngranted: the seeded core role
// documents list every core object, so /me has always been the complete
// vocabulary with the holder's grants filled in — a client can tell "you hold
// nothing on this" from "no such object". An extension object arrives after
// those documents were seeded, so without the seed in Merge it would be ABSENT
// for every principal not explicitly granted it, and the unit's screen could
// not tell the two apart.
func TestARegisteredObjectIsInTheSnapshotEvenUngranted(t *testing.T) {
	const object = "ext_crm_demo_widget"
	registerForTest(t, object)

	// A role document that grants a core object and says nothing about the
	// extension one.
	doc, err := policy.Parse([]byte(`{"objects":{"deal":{"read":true}},"row_scope":"own"}`))
	if err != nil {
		t.Fatal(err)
	}
	objects := meObjects(t, policy.Merge(map[string]policy.Document{"rep": doc}))
	got, ok := objects[object]
	if !ok {
		t.Fatalf("/me omits the registered object entirely, so the client cannot distinguish "+
			"'no grant' from 'no such object'. Got %v", objects)
	}
	if maps.Equal(got, map[string]bool{"create": true, "read": true, "update": true, "delete": true}) {
		t.Fatal("the seeded entry granted everything — the seed must be the ZERO grant")
	}
	for action, allowed := range got {
		if allowed {
			t.Errorf("/me reports %s on an ungranted object as allowed", action)
		}
	}
}

// TestTheExtensionVocabularyIsNotAWayIntoTheCoreOne: the registration path
// widens what a role document may grant, which is exactly the kind of seam a
// unit could otherwise use to grant itself authority over core records.
func TestTheExtensionVocabularyIsNotAWayIntoTheCoreOne(t *testing.T) {
	t.Cleanup(ResetRbacObjectsForTest)
	for _, name := range []RbacObject{
		"deal",                // a core object
		"widget",              // unnamespaced
		"Ext_Crm_Demo_Widget", // wrong case
		"ext_widget",          // no unit segment
		"ext_crm-demo_widget", // a hyphen, which no SQL identifier holds unquoted
		"",
	} {
		if err := RegisterRbacObjects(name); err == nil {
			t.Errorf("RegisterRbacObjects(%q) = nil, want a refusal", string(name))
		}
	}
	// And registering one twice is an error rather than a no-op: two claims on
	// one object name is a wiring defect where each side thinks it owns the
	// grants.
	if err := RegisterRbacObjects("ext_crm_demo_widget"); err != nil {
		t.Fatal(err)
	}
	if err := RegisterRbacObjects("ext_crm_demo_widget"); err == nil {
		t.Fatal("re-registering an object succeeded; want the duplicate refusal")
	}
}
