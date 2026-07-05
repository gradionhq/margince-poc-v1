// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package capture

import (
	"context"
	"testing"
	"time"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/connector"
)

// The sink's admission guards run before any database work: a caller
// that is not a connector, carries no natural key, or claims another
// connector's identity is refused at the door.
func TestUpsertAdmissionGuards(t *testing.T) {
	sink := NewSink(nil) // guards must refuse before the pool is ever touched
	record := func() connector.NormalizedRecord {
		return connector.NormalizedRecord{
			EntityType: "lead",
			NaturalKey: connector.NaturalKey{SourceSystem: "apollo", SourceID: "a-1"},
			Fields:     LeadFields{FullName: "Dana", Email: "dana@example.test"},
			CapturedBy: "connector:apollo",
		}
	}
	connectorCtx := principal.WithActor(context.Background(), principal.Principal{
		Type: principal.PrincipalConnector, ID: "connector:apollo",
	})

	cases := []struct {
		name string
		ctx  context.Context
		rec  func() connector.NormalizedRecord
	}{
		{name: "no principal at all", ctx: context.Background(), rec: record},
		{
			name: "human principal cannot pose as a connector",
			ctx: principal.WithActor(context.Background(), principal.Principal{
				Type: principal.PrincipalHuman, ID: "human:1",
			}),
			rec: record,
		},
		{
			name: "missing natural key cannot be idempotent",
			ctx:  connectorCtx,
			rec: func() connector.NormalizedRecord {
				r := record()
				r.NaturalKey = connector.NaturalKey{}
				return r
			},
		},
		{
			name: "captured_by must match the acting connector",
			ctx:  connectorCtx,
			rec: func() connector.NormalizedRecord {
				r := record()
				r.CapturedBy = "connector:hubspot"
				return r
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := sink.Upsert(tc.ctx, tc.rec()); err == nil {
				t.Fatal("admission guard let the record through")
			}
		})
	}
}

func TestDefaultOccurredAt(t *testing.T) {
	provided := time.Date(2026, 3, 14, 9, 0, 0, 0, time.UTC)
	if got := defaultOccurredAt(provided); !got.Equal(provided) {
		t.Errorf("a provided timestamp must pass through untouched, got %v", got)
	}
	before := time.Now().UTC()
	got := defaultOccurredAt(time.Time{})
	after := time.Now().UTC()
	if got.Before(before) || got.After(after) {
		t.Errorf("a zero timestamp must default to capture time, got %v", got)
	}
}

func TestCaptureSourceFallsBackToTheNaturalKeySystem(t *testing.T) {
	cases := []struct {
		name   string
		record connector.NormalizedRecord
		want   string
	}{
		{
			name: "explicit source wins",
			record: connector.NormalizedRecord{
				Source:     "apollo:a-1",
				NaturalKey: connector.NaturalKey{SourceSystem: "apollo"},
			},
			want: "apollo:a-1",
		},
		{
			name: "empty source names the system",
			record: connector.NormalizedRecord{
				NaturalKey: connector.NaturalKey{SourceSystem: "hubspot"},
			},
			want: "hubspot",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := captureSource(tc.record); got != tc.want {
				t.Errorf("captureSource = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestConnectorPrincipalIDIsIdempotentOnThePrefix(t *testing.T) {
	for _, in := range []string{"booking", "connector:booking"} {
		if got := connectorPrincipalID(in); got != "connector:booking" {
			t.Errorf("connectorPrincipalID(%q) = %q, want connector:booking", in, got)
		}
	}
}
