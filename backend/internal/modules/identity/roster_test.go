// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package identity

import (
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
