// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package identity

import (
	"regexp"
	"strconv"
	"testing"
	"time"

	openapi_types "github.com/oapi-codegen/runtime/types"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// wireUser/wireTeam are pure mappings (no DB), so they carry their own
// unit coverage; the row-scoped read behaviour is proven in the
// real-Postgres integration lane.

func TestWireUser(t *testing.T) {
	id := ids.NewV7()
	ws := ids.NewV7()
	created := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	got := wireUser(userRow{
		ID:          id,
		WorkspaceID: ws,
		Email:       "ada@example.com",
		DisplayName: "Ada Admin",
		Status:      "active",
		IsAgent:     false,
		CreatedAt:   created,
	})

	if got.Id != openapi_types.UUID(id) {
		t.Errorf("Id = %v, want %v", got.Id, id)
	}
	if got.WorkspaceId != openapi_types.UUID(ws) {
		t.Errorf("WorkspaceId = %v, want %v — it is required on User", got.WorkspaceId, ws)
	}
	if string(got.Email) != "ada@example.com" {
		t.Errorf("Email = %q, want ada@example.com", got.Email)
	}
	if got.DisplayName != "Ada Admin" {
		t.Errorf("DisplayName = %q, want Ada Admin", got.DisplayName)
	}
	if string(got.Status) != "active" {
		t.Errorf("Status = %q, want active", got.Status)
	}
	if got.IsAgent {
		t.Error("IsAgent = true, want false")
	}
	if got.CreatedAt == nil || !got.CreatedAt.Equal(created) {
		t.Errorf("CreatedAt = %v, want %v", got.CreatedAt, created)
	}
	// The wire User type carries no credential field — a compile-time
	// guarantee (there is nowhere to put a password), so the roster read
	// cannot leak one. Timezone is not populated by the roster read.
	if got.Timezone != nil {
		t.Errorf("Timezone = %v, want nil (roster read does not select it)", *got.Timezone)
	}
}

// The roster answers every authenticated member, so the shared mapping must
// withhold the role keys even when the row it is given carries them — that
// omission is the whole disclosure gate, and a row read for an admin is the
// same row read for a rep.
func TestWireUserWithholdsRoleKeys(t *testing.T) {
	got := wireUser(userRow{
		ID:          ids.NewV7(),
		WorkspaceID: ids.NewV7(),
		Email:       "ada@example.com",
		DisplayName: "Ada Admin",
		Status:      "active",
		Roles:       []string{"admin"},
		CreatedAt:   time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC),
	})

	if got.Roles != nil {
		t.Errorf("Roles = %v, want nil — a non-admin roster must not disclose who holds a role", *got.Roles)
	}
}

func TestWireUserWithRolesCarriesTheRoleKeys(t *testing.T) {
	row := userRow{
		ID:          ids.NewV7(),
		WorkspaceID: ids.NewV7(),
		Email:       "ada@example.com",
		DisplayName: "Ada Admin",
		Status:      "active",
		Roles:       []string{"admin"},
		CreatedAt:   time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC),
	}
	got := wireUserWithRoles(row)

	if got.Roles == nil {
		t.Fatal("Roles = nil, want the member's role keys — the admin card renders the current role from them")
	}
	if len(*got.Roles) != 1 || (*got.Roles)[0] != "admin" {
		t.Errorf("Roles = %v, want [admin]", *got.Roles)
	}
	// Everything else is the shared mapping; only the role keys are added.
	if got.DisplayName != row.DisplayName || string(got.Status) != row.Status {
		t.Errorf("got %q/%q, want %q/%q", got.DisplayName, got.Status, row.DisplayName, row.Status)
	}
}

// An unassigned seat has no role, and the admin card distinguishes that from
// "not disclosed" — so it must arrive as an empty list, never as an absent one.
func TestWireUserWithRolesKeepsAnUnassignedSeatDistinctFromAWithheldOne(t *testing.T) {
	got := wireUserWithRoles(userRow{
		ID:          ids.NewV7(),
		WorkspaceID: ids.NewV7(),
		Email:       "nora@example.com",
		DisplayName: "Nora None",
		Status:      "active",
		Roles:       []string{},
		CreatedAt:   time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC),
	})

	if got.Roles == nil {
		t.Fatal("Roles = nil, want an empty list — absent means withheld, not unassigned")
	}
	if len(*got.Roles) != 0 {
		t.Errorf("Roles = %v, want empty", *got.Roles)
	}
}

// The roster serves both callers off the same rows, so which mapping the
// caller gets IS the disclosure gate.
func TestRosterUserMappingDisclosesRoleKeysOnlyToAnAdmin(t *testing.T) {
	row := userRow{
		ID:          ids.NewV7(),
		WorkspaceID: ids.NewV7(),
		Email:       "ada@example.com",
		DisplayName: "Ada Admin",
		Status:      "active",
		Roles:       []string{"admin"},
		CreatedAt:   time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC),
	}

	if got := rosterUserMapping(false)(row); got.Roles != nil {
		t.Errorf("non-admin roster Roles = %v, want nil", *got.Roles)
	}
	if got := rosterUserMapping(true)(row); got.Roles == nil {
		t.Error("admin roster Roles = nil, want the member's role keys")
	}
}

// A row the read never asked for role keys on arrives nil, and the wire answers
// WITHOUT the field. Emitting "[]" instead would tell the client this member
// holds no role — a statement about someone's privileges that nothing checked.
func TestWireUserWithRolesOmitsTheFieldWhenTheReadDidNotAskForIt(t *testing.T) {
	got := wireUserWithRoles(userRow{
		ID:          ids.NewV7(),
		WorkspaceID: ids.NewV7(),
		Email:       "ada@example.com",
		DisplayName: "Ada Admin",
		Status:      "active",
		Roles:       nil,
		CreatedAt:   time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC),
	})

	if got.Roles != nil {
		t.Errorf("Roles = %v, want absent — nil means the read never fetched them", *got.Roles)
	}
}

// The bind numbering is a contract between each query string and the args its
// spec sends: leadArgs bind first, then the pager's own. Deriving the obligation
// from the strings beats remembering it — a fifth query with the wrong offset
// would otherwise fail at runtime in pgx, on whichever request first used it.
func TestRosterQueryPlaceholdersMatchTheArgsTheirPagerSends(t *testing.T) {
	// pager args: (q if filtered) + created_at + id + limit.
	const plainPagerArgs, filteredPagerArgs = 3, 4
	for _, c := range []struct {
		name     string
		query    string
		leadArgs int
		pager    int
	}{
		{"listUsers", listUsersQuery, 1, plainPagerArgs},
		{"listUsersFiltered", listUsersFilteredQuery, 1, filteredPagerArgs},
		{"listUsersAll", listUsersAllQuery, 1, plainPagerArgs},
		{"listUsersAllFiltered", listUsersAllFilteredQuery, 1, filteredPagerArgs},
		{"listTeams", listTeamsQuery, 0, plainPagerArgs},
		{"listTeamsFiltered", listTeamsFilteredQuery, 0, filteredPagerArgs},
	} {
		want := c.leadArgs + c.pager
		if got := highestPlaceholder(c.query); got != want {
			t.Errorf("%s: highest placeholder $%d, but its spec sends %d args (%d lead + %d pager)",
				c.name, got, want, c.leadArgs, c.pager)
		}
	}
}

// highestPlaceholder reports the largest $n in a query, which is the number of
// arguments Postgres will expect.
func highestPlaceholder(query string) int {
	highest := 0
	for _, m := range regexp.MustCompile(`\$(\d+)`).FindAllStringSubmatch(query, -1) {
		n, err := strconv.Atoi(m[1])
		if err != nil {
			continue // $ followed by digits cannot fail Atoi; guard for the linter
		}
		if n > highest {
			highest = n
		}
	}
	return highest
}

func TestWireTeam(t *testing.T) {
	id := ids.NewV7()
	ws := ids.NewV7()
	created := time.Date(2026, 6, 2, 9, 30, 0, 0, time.UTC)
	got := wireTeam(teamRow{
		ID:          id,
		WorkspaceID: ws,
		Name:        "Deal Desk",
		MemberCount: 3,
		CreatedAt:   created,
	})

	if got.Id != openapi_types.UUID(id) {
		t.Errorf("Id = %v, want %v", got.Id, id)
	}
	if got.WorkspaceId != openapi_types.UUID(ws) {
		t.Errorf("WorkspaceId = %v, want %v", got.WorkspaceId, ws)
	}
	if got.Name != "Deal Desk" {
		t.Errorf("Name = %q, want Deal Desk", got.Name)
	}
	if got.MemberCount == nil || *got.MemberCount != 3 {
		t.Errorf("MemberCount = %v, want 3", got.MemberCount)
	}
	if got.CreatedAt == nil || !got.CreatedAt.Equal(created) {
		t.Errorf("CreatedAt = %v, want %v", got.CreatedAt, created)
	}
}
