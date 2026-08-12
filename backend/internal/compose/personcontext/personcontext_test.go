// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package personcontext_test

import (
	"testing"
	"time"

	"github.com/gradionhq/margince/backend/internal/compose/personcontext"
	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
)

// The employer reading, including the two cases that make it a refusal rather
// than a lookup: a row that is not the current primary, and one with no
// organization name. "Used to work at" is a different sentence from "works at",
// and a draft that gets it wrong tells somebody about a job they have left.
func TestCurrentEmployerRefusesAnythingButTheCurrentPrimary(t *testing.T) {
	name := "Beispiel Maschinenbau GmbH"

	cases := []struct {
		name string
		rows []crmcontracts.Person360Employment
		want string
	}{
		{name: "no employments at all", rows: nil, want: ""},
		{
			name: "the current primary",
			rows: []crmcontracts.Person360Employment{{IsCurrentPrimary: true, OrganizationName: &name}},
			want: name,
		},
		{
			name: "a past role is not where they work now",
			rows: []crmcontracts.Person360Employment{{IsCurrentPrimary: false, OrganizationName: &name}},
			want: "",
		},
		{
			name: "a current row with no organization name",
			rows: []crmcontracts.Person360Employment{{IsCurrentPrimary: true}},
			want: "",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			view := crmcontracts.Person360{}
			if c.rows != nil {
				view.Employments = &struct {
					Data []crmcontracts.Person360Employment `json:"data"`
					Page crmcontracts.PageInfo              `json:"page"`
				}{Data: c.rows}
			}
			if got := personcontext.CurrentEmployer(view); got != c.want {
				t.Errorf("CurrentEmployer = %q, want %q", got, c.want)
			}
		})
	}
}

// A section missing because the reader lacks the grant is different from one
// that is empty, and every prose surface carries the distinction so a writer
// stays silent about the subject rather than inferring around the gap.
func TestOmittedNamesCarriesTheSectionsTheReaderCouldNotSee(t *testing.T) {
	if got := personcontext.OmittedNames(nil); got != nil {
		t.Errorf("nothing omitted should answer nil, got %+v", got)
	}
	got := personcontext.OmittedNames([]crmcontracts.Person360SectionsOmitted{
		crmcontracts.Person360SectionsOmittedNextMeeting,
	})
	if len(got) != 1 || got[0] != "next_meeting" {
		t.Errorf("OmittedNames = %+v, want [next_meeting]", got)
	}
}

// Empty means the thing never happened, which the prose surfaces state honestly
// — "we have never written to them" rather than a zero date reading as year one.
func TestStampAnswersEmptyForSomethingThatNeverHappened(t *testing.T) {
	if got := personcontext.Stamp(nil); got != "" {
		t.Errorf("Stamp(nil) = %q, want empty", got)
	}
	at := time.Date(2026, 8, 12, 9, 0, 0, 0, time.UTC)
	if got := personcontext.Stamp(&at); got != "2026-08-12T09:00:00Z" {
		t.Errorf("Stamp = %q", got)
	}
}
