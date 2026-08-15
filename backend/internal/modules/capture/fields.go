// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package capture

import "time"

// The typed Fields payloads a NormalizedRecord carries (the port keeps
// Fields as any so the seam stays leaf-pure; the sink switches on these
// concrete shapes and a wrong mapping fails loudly, not silently).

// ActivityFields is a captured interaction bound for the timeline.
type ActivityFields struct {
	Kind string // email | call | meeting | note | task | message

	// ChannelProvider names the messaging transport that carried this record —
	// a channel_provider row — and is empty for anything that did not arrive on
	// one (a mail capture, a meeting, a note).
	//
	// It is a separate field from Kind because they answer separate questions:
	// Kind is what sort of interaction happened, ChannelProvider is how it
	// travelled. They were one column for as long as the only channels were
	// named as kinds, and the send path recovered the transport by reading a
	// kind back as a provider name — which stops being possible the moment a
	// provider that is not also a kind exists.
	ChannelProvider string

	Subject    string
	Body       string
	OccurredAt time.Time
	Direction  string // connector.DirectionInbound | DirectionOutbound | "" (not directional)
}

// LeadFields is a captured prospect bound for the lead pool — never
// directly a person/organization (ADR-0008: leads graduate, raw
// capture does not mint clean-core rows).
type LeadFields struct {
	FullName    string
	Email       string
	CompanyName string
	Title       string
}
