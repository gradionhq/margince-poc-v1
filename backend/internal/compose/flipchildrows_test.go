// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

import (
	"reflect"
	"testing"

	"github.com/gradionhq/margince/backend/internal/modules/overlay"
	"github.com/gradionhq/margince/backend/internal/modules/people"
)

// twoEmailRowsMapping declares a contact whose work and personal addresses are
// separate rows of one collection — the shape a mapping reaches for the moment
// an incumbent carries more than one address, and the shape the wire already
// publishes whole.
var twoEmailRowsMapping = overlay.ObjectMapping{
	Source: "contacts", Target: "person", ExternalKey: "hs_object_id",
	Fields: []overlay.FieldMapping{
		{
			From: []string{"email"}, To: "person_email.email",
			Kind: overlay.TargetChild, Transform: "lowercase",
			Child: &overlay.ChildRow{Attrs: map[string]any{"email_type": "work", "is_primary": true}, Position: 0},
		},
		{
			From: []string{"hs_additional_emails"}, To: "person_email.email",
			Kind: overlay.TargetChild, Transform: "lowercase",
			Child: &overlay.ChildRow{Attrs: map[string]any{"email_type": "personal", "is_primary": false}, Position: 1},
		},
	},
}

// twoDomainRowsMapping is twoEmailRowsMapping's counterpart for a company that
// answers on a second domain.
var twoDomainRowsMapping = overlay.ObjectMapping{
	Source: "companies", Target: "organization", ExternalKey: "hs_object_id",
	Fields: []overlay.FieldMapping{
		{
			From: []string{"domain"}, To: "organization_domain.domain",
			Kind: overlay.TargetChild, Transform: "lowercase",
			Child: &overlay.ChildRow{Attrs: map[string]any{"is_primary": true}, Position: 0},
		},
		{
			From: []string{"secondarydomain"}, To: "organization_domain.domain",
			Kind: overlay.TargetChild, Transform: "lowercase",
			Child: &overlay.ChildRow{Attrs: map[string]any{"is_primary": false}, Position: 1},
		},
	},
}

// The flip import carries EVERY row of a child collection, as the read wire
// publishes every row of the same canonical payload. The two are readers of one
// payload, and the flip's are durable: it writes native rows and freezes the
// mirror behind them, so an address or a domain a user can see on the overlay
// and the import silently drops is gone for good. Each row keeps the type,
// primary flag and position its mapping declared rather than inheriting the
// leading row's.
func TestFlipCarriesEveryRowOfAChildCollection(t *testing.T) {
	canonical, _, err := overlay.Apply(twoEmailRowsMapping, map[string]any{
		"hs_object_id": "1", "email": "Ada@Example.TEST", "hs_additional_emails": "Ada@Home.TEST",
	})
	if err != nil {
		t.Fatalf("Apply(contacts with two email rows): %v", err)
	}
	emails := flipPersonEmails(canonical)
	wantEmails := []people.PersonEmailInput{
		{Email: "ada@example.test", EmailType: "work", IsPrimary: true, Position: 0},
		{Email: "ada@home.test", EmailType: "personal", IsPrimary: false, Position: 1},
	}
	if !reflect.DeepEqual(emails, wantEmails) {
		t.Errorf("emails = %+v, want %+v — every declared row lands, with its own attributes", emails, wantEmails)
	}

	canonical, _, err = overlay.Apply(twoDomainRowsMapping, map[string]any{
		"hs_object_id": "2", "domain": "Acme.IO", "secondarydomain": "Acme.TEST",
	})
	if err != nil {
		t.Fatalf("Apply(companies with two domain rows): %v", err)
	}
	domains := flipOrgDomains(canonical)
	wantDomains := []people.OrgDomainInput{
		{Domain: "acme.io", IsPrimary: true},
		{Domain: "acme.test", IsPrimary: false},
	}
	if !reflect.DeepEqual(domains, wantDomains) {
		t.Errorf("domains = %+v, want %+v — every declared row lands, with its own primary claim", domains, wantDomains)
	}
}

// A row holding no value is skipped, never imported as a blank address or host
// — and skipping it must not cost the rows around it, which is what a reader
// that stops at the first empty row would do.
func TestFlipSkipsAValuelessChildRowAndKeepsTheRest(t *testing.T) {
	emails := flipPersonEmails(map[string]any{"person_email": []any{
		map[string]any{"email": "   ", "email_type": "work", "position": 0},
		map[string]any{"email_type": "personal", "position": 1},
		map[string]any{"email": "ada@home.test", "email_type": "personal", "position": 2},
	}})
	want := []people.PersonEmailInput{{Email: "ada@home.test", EmailType: "personal", Position: 2}}
	if !reflect.DeepEqual(emails, want) {
		t.Errorf("emails = %+v, want %+v — the blank rows drop out and the address behind them survives", emails, want)
	}

	domains := flipOrgDomains(map[string]any{"organization_domain": []any{
		map[string]any{"is_primary": true, "position": 0},
		map[string]any{"domain": "acme.test", "position": 1},
	}})
	want2 := []people.OrgDomainInput{{Domain: "acme.test"}}
	if !reflect.DeepEqual(domains, want2) {
		t.Errorf("domains = %+v, want %+v — the valueless row drops out and the host behind it survives", domains, want2)
	}
}

// person_email.email_type is CHECK-constrained, so a type the contract does not
// know must never reach the store: it would abort the WHOLE import with a raw
// constraint error rather than cost one contact its address. The guard belongs
// to every row — a mapping's second row declares its type as independently as
// its first — and it replaces the type alone, never the address.
func TestFlipHoldsEveryEmailRowsTypeToTheContractEnum(t *testing.T) {
	emails := flipPersonEmails(map[string]any{"person_email": []any{
		map[string]any{"email": "ada@example.test", "email_type": "work", "is_primary": true, "position": 0},
		map[string]any{"email": "ada@home.test", "email_type": "billing", "position": 1},
	}})
	want := []people.PersonEmailInput{
		{Email: "ada@example.test", EmailType: "work", IsPrimary: true, Position: 0},
		{Email: "ada@home.test", EmailType: "work", Position: 1},
	}
	if !reflect.DeepEqual(emails, want) {
		t.Errorf("emails = %+v, want %+v — an off-enum type on any row reads as the work address a mapped address means", emails, want)
	}
}
