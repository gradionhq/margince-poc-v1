// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package capture

import (
	"context"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/ports/connector"
)

type fakeChannelConnector struct{ name string }

func (f fakeChannelConnector) Descriptor() connector.Descriptor {
	return connector.Descriptor{Name: f.name}
}
func (f fakeChannelConnector) Authenticate(context.Context, connector.AuthRequest) (connector.Auth, error) {
	return nil, nil
}
func (f fakeChannelConnector) Sync(context.Context, connector.Auth, connector.Cursor, connector.Sink) (connector.Cursor, error) {
	return connector.Cursor{}, nil
}
func (f fakeChannelConnector) Normalize(context.Context, connector.RawRecord) ([]connector.NormalizedRecord, error) {
	return nil, nil
}
func (f fakeChannelConnector) HealthCheck(context.Context, connector.Auth) error { return nil }
func (f fakeChannelConnector) SendMessage(context.Context, connector.Auth, connector.ChannelMessage) (connector.SendReceipt, error) {
	return connector.SendReceipt{}, nil
}

type fakeMailConnector struct{ name string }

func (f fakeMailConnector) Descriptor() connector.Descriptor {
	return connector.Descriptor{Name: f.name}
}
func (f fakeMailConnector) Authenticate(context.Context, connector.AuthRequest) (connector.Auth, error) {
	return nil, nil
}
func (f fakeMailConnector) Sync(context.Context, connector.Auth, connector.Cursor, connector.Sink) (connector.Cursor, error) {
	return connector.Cursor{}, nil
}
func (f fakeMailConnector) Normalize(context.Context, connector.RawRecord) ([]connector.NormalizedRecord, error) {
	return nil, nil
}
func (f fakeMailConnector) HealthCheck(context.Context, connector.Auth) error { return nil }
func (f fakeMailConnector) SendEmail(context.Context, connector.Auth, connector.EmailMessage) (connector.SendReceipt, error) {
	return connector.SendReceipt{}, nil
}

// A connector that implements connector.MessageSender (a channel transport)
// is reported; one that only implements connector.EmailSender (mail) is not —
// the two are distinct method sets, so the type assertion alone is the
// answer, with no second marker to keep in sync.
func TestChannelProvidersReportsOnlyMessageSenderConnectors(t *testing.T) {
	r := NewRegistry(nil, nil, nil, nil)
	r.Register(fakeChannelConnector{name: "fake-channel"})
	r.Register(fakeMailConnector{name: "fake-mail"})

	got := r.ChannelProviders()

	if len(got) != 1 || got[0] != "fake-channel" {
		t.Fatalf("ChannelProviders() = %v, want [fake-channel]", got)
	}
}

// Stably sorted, so a boot reconcile that diffs against a prior run never sees
// spurious churn from Go's randomized map iteration order.
func TestChannelProvidersIsSorted(t *testing.T) {
	r := NewRegistry(nil, nil, nil, nil)
	r.Register(fakeChannelConnector{name: "zzz-channel"})
	r.Register(fakeChannelConnector{name: "aaa-channel"})

	got := r.ChannelProviders()

	if len(got) != 2 || got[0] != "aaa-channel" || got[1] != "zzz-channel" {
		t.Fatalf("ChannelProviders() = %v, want sorted [aaa-channel zzz-channel]", got)
	}
}

// No channel-capable connector registered at all reports an empty, non-nil-
// panicking result — a boot reconcile with nothing to do must not be a
// special case.
func TestChannelProvidersOnAnEmptyRegistry(t *testing.T) {
	r := NewRegistry(nil, nil, nil, nil)

	if got := r.ChannelProviders(); len(got) != 0 {
		t.Fatalf("ChannelProviders() on an empty registry = %v, want empty", got)
	}
}
