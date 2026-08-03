// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package auth

import (
	"context"
	"errors"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

func TestRequireAdmin(t *testing.T) {
	admin := principal.Principal{Type: principal.PrincipalHuman, Permissions: principal.Permissions{RoleKeys: []string{"member", "admin"}}}
	nonAdmin := principal.Principal{Type: principal.PrincipalHuman, Permissions: principal.Permissions{RoleKeys: []string{"member"}}}
	system := principal.Principal{Type: principal.PrincipalSystem, ID: "system"}

	if err := RequireAdmin(principal.WithActor(context.Background(), admin)); err != nil {
		t.Fatalf("admin denied: %v", err)
	}
	if err := RequireAdmin(principal.WithActor(context.Background(), system)); err != nil {
		t.Fatalf("system denied: %v", err)
	}
	if err := RequireAdmin(principal.WithActor(context.Background(), nonAdmin)); !errors.Is(err, apperrors.ErrPermissionDenied) {
		t.Fatalf("non-admin: want ErrPermissionDenied, got %v", err)
	}
	if err := RequireAdmin(context.Background()); !errors.Is(err, apperrors.ErrPermissionDenied) {
		t.Fatalf("no principal: want ErrPermissionDenied, got %v", err)
	}
}
