// Package prov carries write provenance (data-model §1.6). Provenance is
// not optional: every write path takes or stamps it, and the store APIs
// accept no overload without it — a missing provenance is a compile
// failure, not a runtime surprise.
package prov

import "errors"

// Provenance says where a value came from and which actor put it there.
type Provenance struct {
	// Source identifies the originating record or surface,
	// e.g. "gmail:msg-18c2…", "ui:person-form", "api".
	Source string

	// CapturedBy identifies the writing actor,
	// e.g. "human:<uuid>", "agent:overnight", "connector:gmail".
	CapturedBy string
}

// Validate rejects the empty stamps that would otherwise silently satisfy
// NOT NULL with "".
func (p Provenance) Validate() error {
	if p.Source == "" {
		return errors.New("prov: empty Source")
	}
	if p.CapturedBy == "" {
		return errors.New("prov: empty CapturedBy")
	}
	return nil
}
