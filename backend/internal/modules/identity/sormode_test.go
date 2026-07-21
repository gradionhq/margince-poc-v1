// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package identity

// /me reports the workspace system-of-record mode so the client can gate its
// list UI (an overlay mirror refuses sort/filter dials). These prove the
// mode lands in the response and that resolution degrades to native — never
// failing /me — when the resolver is absent or errors.

import (
	"context"
	"errors"
	"testing"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
)

func TestMeResponseCarriesSystemOfRecordMode(t *testing.T) {
	for _, mode := range []crmcontracts.MeResponseSystemOfRecordMode{
		crmcontracts.Native, crmcontracts.Overlay,
	} {
		got := meResponse(Identity{}, mode)
		if got.SystemOfRecord == nil {
			t.Fatalf("mode %q: system_of_record must always be present", mode)
		}
		if got.SystemOfRecord.Mode != mode {
			t.Errorf("system_of_record.mode = %q, want %q", got.SystemOfRecord.Mode, mode)
		}
	}
}

func TestResolveSorModeDefaultsAndDegradesToNative(t *testing.T) {
	tests := []struct {
		name    string
		resolve func(context.Context) (bool, error)
		want    crmcontracts.MeResponseSystemOfRecordMode
	}{
		{"nil resolver (no overlay wiring)", nil, crmcontracts.Native},
		{"native workspace", func(context.Context) (bool, error) { return false, nil }, crmcontracts.Native},
		{"overlay workspace", func(context.Context) (bool, error) { return true, nil }, crmcontracts.Overlay},
		{
			"resolver error degrades to native",
			func(context.Context) (bool, error) { return true, errors.New("mode probe failed") },
			crmcontracts.Native,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := Handlers{sorMode: tt.resolve}
			if got := h.resolveSorMode(context.Background()); got != tt.want {
				t.Errorf("resolveSorMode = %q, want %q", got, tt.want)
			}
		})
	}
}
