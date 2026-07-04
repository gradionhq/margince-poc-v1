package capture

import "time"

// The typed Fields payloads a NormalizedRecord carries (the port keeps
// Fields as any so the seam stays leaf-pure; the sink switches on these
// concrete shapes and a wrong mapping fails loudly, not silently).

// ActivityFields is a captured interaction bound for the timeline.
type ActivityFields struct {
	Kind       string // email | call | meeting | note | whatsapp | telegram
	Subject    string
	Body       string
	OccurredAt time.Time
	Direction  string // inbound | outbound | "" (not directional)
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
