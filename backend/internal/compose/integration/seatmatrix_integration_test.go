// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// AAD-AC-4 as a matrix rather than a sample: seat_type × role × action.
//
// The claim under test is an ORDERING one — the seat ceiling sits BELOW
// role permissions, so a read seat is refused whatever its role grants,
// and a full seat then falls through to RBAC. Two verbs on one role
// cannot state that; only every seeded role against every action class
// can, and only if the expectation for the full seat is read out of the
// same grant document the server enforces rather than written down here
// a second time.
//
// So the rows come from the DATABASE (`select key, permissions from
// role`, the seeded system roles), and the full-seat verdict is derived
// from that JSON. Re-granting a role, or seeding another, moves this
// test with it; a hand-listed table would have gone on passing.

import (
	"bytes"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/gradionhq/margince/backend/internal/compose/integration/apptest"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// seatAction is one of AAD-AC-4's action classes, bound to the endpoint
// that performs it and to the grant that decides it for a full seat.
type seatAction struct {
	class string
	// object/verb name the RBAC grant the server checks, so the expected
	// full-seat outcome can be read out of the role's own permissions.
	object string
	verb   string
	// call issues the request and answers the status and the problem code.
	call func(t *testing.T, e *apptest.AppEnv, f seatFixtures) (int, string)
}

// seatFixtures are the records the matrix acts on, all created once by
// the bootstrap admin on a full seat before any role is swapped in.
type seatFixtures struct {
	personID   string
	dealID     string
	pipelineID string
	birthStage string
	nextStage  string
	colleague  string
}

func seatActions() []seatAction {
	return []seatAction{
		{
			class: "mutate (create)", object: "person", verb: "create",
			call: func(t *testing.T, e *apptest.AppEnv, _ seatFixtures) (int, string) {
				return callForCode(t, e, "POST", "/v1/people", apptest.AnyMap{
					"full_name": "Matrix Probe", "source": "manual",
				})
			},
		},
		{
			class: "mutate (update)", object: "person", verb: "update",
			call: func(t *testing.T, e *apptest.AppEnv, f seatFixtures) (int, string) {
				return callForCode(t, e, "PATCH", "/v1/people/"+f.personID, apptest.AnyMap{"title": "Probed"})
			},
		},
		{
			class: "advance", object: "deal", verb: "update",
			call: func(t *testing.T, e *apptest.AppEnv, f seatFixtures) (int, string) {
				return callForCode(t, e, "POST", "/v1/deals/"+f.dealID+"/advance",
					apptest.AnyMap{"to_stage_id": f.nextStage})
			},
		},
		{
			class: "export", object: "person", verb: "read",
			call: func(t *testing.T, e *apptest.AppEnv, _ seatFixtures) (int, string) {
				// owner_id is the person vocabulary's one filterable leaf,
				// and a filterless export is refused for its own reasons
				// before it ever reaches the gate this cell is about.
				return exportForCode(t, e, apptest.AnyMap{
					"object": "person", "format": "csv",
					"filter": apptest.AnyMap{"field": "owner_id", "op": "eq", "value": ids.NewV7().String()},
				})
			},
		},
		{
			class: "share (write record_grant)", object: "person", verb: "update",
			call: func(t *testing.T, e *apptest.AppEnv, f seatFixtures) (int, string) {
				return callForCode(t, e, "POST", "/v1/record-grants", apptest.AnyMap{
					"record_type": "person", "record_id": f.personID,
					"subject_type": "user", "subject_id": f.colleague, "access": "write",
				})
			},
		},
	}
}

func TestTheSeatCeilingHoldsForEverySeededRoleAndAction(t *testing.T) {
	e := apptest.SetupApp(t)
	e.Slug = "seat-matrix"
	apptest.BootstrapWorkspaceSession(t, e, "Seat Matrix", "seat@fable.test", "Admin")
	fixtures := seedSeatFixtures(t, e)
	roles := seededRoles(t, e)
	if len(roles) < 2 {
		t.Fatalf("%d system roles seeded; the matrix needs the seeded set to say anything", len(roles))
	}

	// A matrix whose every cell is granted proves only the read-seat half:
	// the full-seat leg would then never once exercise "and RBAC decides".
	// Counted and asserted, so a policy change that made every cell fall on
	// one side would fail here rather than quietly narrow what this proves.
	var allowed, denied int
	for _, role := range roles {
		assignSoleRole(t, e, role.key)
		for _, action := range seatActions() {
			// Every cell acts on the same starting state. Without this the
			// fixtures carry one role's successful write into the next
			// role's cell, and the second attempt is refused for a reason
			// that has nothing to do with the seat or the role.
			resetSeatFixtures(t, e, fixtures)
			// A read seat is refused whatever the role grants — the
			// ceiling is licensing, and it is checked first.
			e.SetWorkspaceSeat(t, e.Slug, "read")
			fullSeat(t, e, fixtures.colleague)
			if status, code := action.call(t, e, fixtures); status != http.StatusForbidden ||
				code != "seat_tier_insufficient" {
				t.Errorf("read seat / role %q / %s → %d %q, want 403 seat_tier_insufficient",
					role.key, action.class, status, code)
			}
			// A full seat falls through to RBAC, and what RBAC then says
			// is read out of this role's own seeded grant document.
			e.SetWorkspaceSeat(t, e.Slug, "full")
			fullSeat(t, e, fixtures.colleague)
			status, code := action.call(t, e, fixtures)
			if code == "seat_tier_insufficient" {
				t.Errorf("full seat / role %q / %s → %d %q — the ceiling refused a licensed seat",
					role.key, action.class, status, code)
			}
			granted := role.grants(action.object, action.verb)
			if granted {
				allowed++
				// The granted leg has to SUCCEED, not merely avoid the
				// seat code: an action refused for an unrelated reason —
				// a malformed body, a tuple already granted by the
				// previous role — would otherwise read as "the ceiling
				// let it through" while proving nothing about the ceiling.
				if status >= http.StatusBadRequest {
					t.Errorf("full seat / role %q / %s → %d %q, but the seeded document grants %s.%s",
						role.key, action.class, status, code, action.object, action.verb)
				}
			} else {
				denied++
				if code != "permission_denied" {
					t.Errorf("full seat / role %q / %s → %d %q, but the seeded document grants no %s.%s",
						role.key, action.class, status, code, action.object, action.verb)
				}
			}
		}
	}
	if allowed == 0 || denied == 0 {
		t.Fatalf("the matrix ran %d granted and %d ungranted cells; both are needed for it to state that the ceiling sits BELOW RBAC",
			allowed, denied)
	}
	t.Logf("seat matrix: %d roles × %d actions — %d granted, %d refused by RBAC on a full seat",
		len(roles), len(seatActions()), allowed, denied)
}

// The receiving half of the same ceiling (AAD-AC-4): a read seat may be
// handed a record to read, never one to write.
func TestAWriteGrantIsRefusedToAReadSeat(t *testing.T) {
	e := apptest.SetupApp(t)
	e.Slug = "seat-grant"
	apptest.BootstrapWorkspaceSession(t, e, "Seat Grant", "seat-grant@fable.test", "Admin")
	fixtures := seedSeatFixtures(t, e)
	readSeatColleague(t, e, fixtures.colleague)

	share := func(access string) (int, string) {
		return callForCode(t, e, "POST", "/v1/record-grants", apptest.AnyMap{
			"record_type": "person", "record_id": fixtures.personID,
			"subject_type": "user", "subject_id": fixtures.colleague, "access": access,
		})
	}
	if status, code := share("write"); status != http.StatusForbidden || code != "seat_tier_insufficient" {
		t.Fatalf("write grant to a read seat → %d %q, want 403 seat_tier_insufficient", status, code)
	}
	// A read grant hands over exactly the authority the licence carries.
	if status, _ := share("read"); status != http.StatusCreated {
		t.Fatalf("read grant to a read seat → %d, want 201", status)
	}
	// A TEAM is not a seat: the grant stands, and the read seats inside it
	// are still refused every write at their own admission. Inserted
	// directly because team CRUD is deferred in the contract — the row is
	// a fixture here, not the writer under test.
	team := teamFixture(t, e)
	if status, code := callForCode(t, e, "POST", "/v1/record-grants", apptest.AnyMap{
		"record_type": "person", "record_id": fixtures.personID,
		"subject_type": "team", "subject_id": team, "access": "write",
	}); status != http.StatusCreated {
		t.Fatalf("write grant to a team → %d %q, want 201 — a team carries no seat to refuse", status, code)
	}

	// And nothing was written by the refused attempt.
	var writes int
	if err := e.Owner.QueryRow(t.Context(),
		`SELECT count(*) FROM record_grant WHERE subject_id = $1::uuid AND access = 'write'`,
		fixtures.colleague).Scan(&writes); err != nil {
		t.Fatal(err)
	}
	if writes != 0 {
		t.Fatalf("%d write grants reached a read seat, want 0", writes)
	}
}

// resetSeatFixtures returns the records the matrix acts on to the state
// seedSeatFixtures left them in — the deal on its birth stage, and no grant
// on the tuple the share action asserts. Written with the owner connection
// because this is fixture bookkeeping between cells, not an operation the
// matrix is measuring.
func resetSeatFixtures(t *testing.T, e *apptest.AppEnv, f seatFixtures) {
	t.Helper()
	if _, err := e.Owner.Exec(t.Context(),
		`DELETE FROM record_grant WHERE record_id = $1::uuid AND subject_id = $2::uuid`,
		f.personID, f.colleague); err != nil {
		t.Fatalf("reset grants: %v", err)
	}
	if _, err := e.Owner.Exec(t.Context(),
		`UPDATE deal SET stage_id = $2::uuid WHERE id = $1::uuid`, f.dealID, f.birthStage); err != nil {
		t.Fatalf("reset deal stage: %v", err)
	}
}

// teamFixture mints one team to receive a share. The contract defers team
// CRUD, so there is no endpoint to create one through.
func teamFixture(t *testing.T, e *apptest.AppEnv) string {
	t.Helper()
	var id string
	if err := e.Owner.QueryRow(t.Context(), `
		INSERT INTO team (workspace_id, name)
		VALUES ((SELECT id FROM workspace WHERE slug = $1), 'Matrix Team') RETURNING id::text`,
		e.Slug).Scan(&id); err != nil {
		t.Fatalf("seed team: %v", err)
	}
	return id
}

// seededRole is one system role as the database holds it: the key the
// matrix reports, and the grant document the expectation is read from.
type seededRole struct {
	key     string
	objects map[string]map[string]bool
}

func (r seededRole) grants(object, verb string) bool { return r.objects[object][verb] }

// seededRoles reads the system roles the bootstrap laid down. This is the
// derivation: the matrix's rows and its full-seat expectations both come
// from here, so the test cannot disagree with the policy it is checking.
func seededRoles(t *testing.T, e *apptest.AppEnv) []seededRole {
	t.Helper()
	rows, err := e.Owner.Query(t.Context(),
		`SELECT key, permissions FROM role WHERE is_system ORDER BY key`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []seededRole
	for rows.Next() {
		var key string
		var raw []byte
		if err := rows.Scan(&key, &raw); err != nil {
			t.Fatal(err)
		}
		var doc struct {
			Objects map[string]map[string]bool `json:"objects"`
		}
		if err := json.Unmarshal(raw, &doc); err != nil {
			t.Fatalf("role %q carries an unreadable permission document: %v", key, err)
		}
		out = append(out, seededRole{key: key, objects: doc.Objects})
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// assignSoleRole makes the signed-in admin hold exactly the named role, so
// the request that follows is answered by that role's grants alone.
func assignSoleRole(t *testing.T, e *apptest.AppEnv, key string) {
	t.Helper()
	if _, err := e.Owner.Exec(t.Context(), `
		UPDATE role_assignment SET role_id = (SELECT id FROM role WHERE key = $1 AND is_system)
		WHERE user_id = (SELECT id FROM app_user WHERE email = $2)`,
		key, "seat@fable.test"); err != nil {
		t.Fatalf("assigning role %q: %v", key, err)
	}
}

// fullSeat holds the share recipient on a full seat whatever the acting
// human's is. SetWorkspaceSeat flips EVERY non-agent user, so without this
// the share cell's read-seat leg would be ambiguous: the refusal could be
// the actor's ceiling (what the matrix measures) or the recipient's (the
// receiving-half check, which raises the same sentinel and has its own
// test). Pinning the recipient makes each cell a statement about the actor.
func fullSeat(t *testing.T, e *apptest.AppEnv, userID string) {
	t.Helper()
	if _, err := e.Owner.Exec(t.Context(),
		`UPDATE app_user SET seat_type = 'full' WHERE id = $1::uuid`, userID); err != nil {
		t.Fatalf("full-seating the share recipient: %v", err)
	}
}

// readSeatColleague puts one named user on a read seat, leaving the acting
// admin on a full one — the asymmetry the receiving-half refusal needs.
func readSeatColleague(t *testing.T, e *apptest.AppEnv, userID string) {
	t.Helper()
	if _, err := e.Owner.Exec(t.Context(),
		`UPDATE app_user SET seat_type = 'read' WHERE id = $1::uuid`, userID); err != nil {
		t.Fatalf("read-seating the colleague: %v", err)
	}
}

// exportForCode is callForCode for the one action whose SUCCESS is not JSON:
// a granted export answers text/csv, which the shared decode would choke on.
// Status always, code only from a problem body.
func exportForCode(t *testing.T, e *apptest.AppEnv, body apptest.AnyMap) (int, string) {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequestWithContext(t.Context(), "POST", e.TS.URL+"/v1/exports", bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Workspace-Slug", e.Slug)
	resp, err := e.Client.Do(req)
	if err != nil {
		t.Fatalf("export request: %v", err)
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			t.Errorf("closing the export body: %v", err)
		}
	}()
	if resp.StatusCode < http.StatusBadRequest {
		return resp.StatusCode, ""
	}
	var problem struct {
		Code string `json:"code"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&problem); err != nil {
		t.Fatalf("decoding the export refusal: %v", err)
	}
	return resp.StatusCode, problem.Code
}

// callForCode issues one request and answers its status and problem code —
// the two things every cell of the matrix is about.
func callForCode(t *testing.T, e *apptest.AppEnv, method, path string, body apptest.AnyMap) (int, string) {
	t.Helper()
	var problem struct {
		Code string `json:"code"`
	}
	return e.Call(t, method, path, body, nil, &problem), problem.Code
}

// seedSeatFixtures creates everything the matrix acts on, as the
// bootstrap admin on a full seat, before any role or seat is changed.
func seedSeatFixtures(t *testing.T, e *apptest.AppEnv) seatFixtures {
	t.Helper()
	seeded := apptest.DiscoverSeededPipeline(t, e)
	var person struct {
		ID string `json:"id"`
	}
	if status := e.Call(t, "POST", "/v1/people", apptest.AnyMap{
		"full_name": "Matrix Subject", "source": "manual",
	}, nil, &person); status != http.StatusCreated {
		t.Fatalf("seed person → %d", status)
	}
	var stages struct {
		Data []struct {
			ID       string `json:"id"`
			Semantic string `json:"semantic"`
			Position int    `json:"position"`
		} `json:"data"`
	}
	if status := e.Call(t, "GET", "/v1/stages?pipeline_id="+seeded.PipelineID, nil, nil, &stages); status != http.StatusOK {
		t.Fatalf("read stages → %d", status)
	}
	next := ""
	for _, s := range stages.Data {
		if s.Semantic == "open" && s.ID != seeded.Open {
			next = s.ID
			break
		}
	}
	if next == "" {
		t.Fatal("the seeded pipeline has no second open stage to advance to")
	}
	return seatFixtures{
		personID:   person.ID,
		dealID:     apptest.CreateOpenDeal(t, e, seeded),
		pipelineID: seeded.PipelineID,
		birthStage: seeded.Open,
		nextStage:  next,
		colleague:  inviteColleague(t, e),
	}
}

// inviteColleague adds a second user to receive a share, through the real
// invite endpoint rather than a hand-inserted row.
func inviteColleague(t *testing.T, e *apptest.AppEnv) string {
	t.Helper()
	var user struct {
		ID string `json:"id"`
	}
	status := e.Call(t, "POST", "/v1/users", apptest.AnyMap{
		"email": "colleague@fable.test", "display_name": "Colleague", "role": "rep",
	}, nil, &user)
	if status != http.StatusCreated || user.ID == "" {
		t.Fatalf("invite colleague → %d %q", status, user.ID)
	}
	return user.ID
}
